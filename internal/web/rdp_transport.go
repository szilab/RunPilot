package web

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/remote"
	"github.com/szilab/RunPilot/internal/remote/guacd"
)

// A ticket is short-lived and single-use. It authorizes only the already
// snapshotted session, never a client-selected guacd or RDP destination.
func (s *Server) handleRemoteTransportTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	client, err := s.ctrl.Remote().Client(id)
	if err != nil {
		remoteError(w, err)
		return
	}
	if client.Kind != remote.ClientGuacamole {
		writeError(w, http.StatusConflict, errors.New("remote session does not use Guacamole"))
		return
	}
	ticket, err := secureTicket()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.rdpTicketMu.Lock()
	s.rdpTickets[ticket] = remoteTransportTicket{SessionID: id, Expires: time.Now().Add(time.Minute)}
	s.rdpTicketMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"ticket": ticket})
}

func (s *Server) handleRemoteTransport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.consumeRDPTransportTicket(r.URL.Query().Get("ticket"), id) {
		http.Error(w, "remote transport authorization required", http.StatusUnauthorized)
		return
	}
	// These markers distinguish an Azure/front-proxy WebSocket failure from a
	// guacd failure. If neither marker is recorded, the request never reached
	// RunPilot. They intentionally contain no ticket or credential data.
	s.ctrl.Remote().AddDiagnostic(id, "browser tunnel request received")
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.ctrl.Remote().Stop(stopCtx, id)
	}()
	// Azure Container Apps and other TLS-terminating proxies may forward an
	// upstream Host that differs from the public browser Origin. This endpoint
	// is protected by a short-lived, single-use, high-entropy transport ticket,
	// so the normal Host/Origin comparison would add no meaningful protection
	// while incorrectly rejecting that valid proxied WebSocket upgrade.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"guacamole"},
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.ctrl.Remote().AddDiagnostic(id, "browser tunnel upgrade failed: "+strings.TrimSpace(err.Error()))
		return
	}
	s.ctrl.Remote().AddDiagnostic(id, "browser tunnel upgraded")
	// Prefer a WebSocket close handshake. CloseNow() immediately tears down the
	// transport and can discard a useful pre-guacd error reason, which the
	// Guacamole browser client otherwise reduces to its generic status 519.
	defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()
	outbound := &guacamoleWSWriter{ws: ws}
	credentials, ok := s.takeRDPCredentials(id)
	if !ok {
		credentials = rdpCredentials{}
	}
	defer func() { credentials.Username, credentials.Domain, credentials.Password = "", "", "" }()
	session, err := s.ctrl.Remote().Get(id)
	if err != nil || session.RDP == nil {
		_ = ws.Close(websocket.StatusInternalError, "RDP session unavailable")
		return
	}
	config := s.ctrl.GuacdConfig()
	s.ctrl.Remote().AddDiagnostic(id, rdpSetupDiagnostics(id, config.Host, config.Port, *session.RDP, credentials.Password != ""))
	width, _ := strconv.Atoi(r.URL.Query().Get("width"))
	height, _ := strconv.Atoi(r.URL.Query().Get("height"))
	dpi, _ := strconv.Atoi(r.URL.Query().Get("dpi"))
	if width < 1 || width > 16384 {
		width = 1024
	}
	if height < 1 || height > 16384 {
		height = 768
	}
	if dpi < 50 || dpi > 400 {
		dpi = 96
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	s.ctrl.Remote().AddDiagnostic(id, "connecting to guacd")
	upstream, err := s.ctrl.Remote().Dial(ctx, id)
	if err != nil {
		s.failRDPTransport(ws, id, err)
		return
	}
	defer s.ctrl.Remote().ReleaseDial(id, upstream)
	s.ctrl.Remote().AddDiagnostic(id, "guacd connected")
	reader, err := guacd.ConnectRDPWithStages(ctx, upstream, *session.RDP, guacd.Credentials{Username: credentials.Username, Domain: credentials.Domain, Password: credentials.Password}, guacd.ClientInfo{Width: width, Height: height, DPI: dpi, TimeZone: r.URL.Query().Get("timezone"), ImageMimetypes: []string{"image/png", "image/jpeg"}}, func(stage string) { s.ctrl.Remote().AddDiagnostic(id, stage) })
	if err != nil {
		s.failRDPTransport(ws, id, err)
		return
	}
	// Width, height, and DPI were negotiated as guacd connect arguments. Once
	// the WebSocket tunnel is open, Guacamole.Client sends its ordinary size
	// instruction. Do not inject an extra size instruction here: that would
	// race the browser client and differs from Guacamole's normal tunnel flow.
	tunnelReady, err := guacd.TunnelReady(id)
	if err != nil {
		s.failRDPTransport(ws, id, err)
		return
	}
	if err := outbound.Write(ctx, tunnelReady); err != nil {
		s.ctrl.Remote().AddDiagnostic(id, "browser tunnel setup failed: "+strings.TrimSpace(err.Error()))
		return
	}
	s.ctrl.Remote().AddDiagnostic(id, "browser tunnel attached")
	done := make(chan struct{})
	go func() {
		keepaliveSeen := false
		browserDiagnostics := browserInstructionDiagnostics(func(line string) {
			s.ctrl.Remote().AddDiagnostic(id, line)
		})
		for {
			messageType, data, err := ws.Read(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.ctrl.Remote().AddDiagnostic(id, "browser tunnel read ended: "+strings.TrimSpace(err.Error()))
				}
				break
			}
			if messageType != websocket.MessageText {
				s.ctrl.Remote().AddDiagnostic(id, "browser tunnel received a non-text message")
				break
			}
			responded, err := forwardBrowserInstructionsWithObserver(ctx, data, upstream, outbound, browserDiagnostics)
			if err != nil {
				s.ctrl.Remote().AddDiagnostic(id, "browser -> guacd forwarding failed: "+strings.TrimSpace(err.Error()))
				break
			}
			if responded {
				if !keepaliveSeen {
					s.ctrl.Remote().AddDiagnostic(id, "browser tunnel keepalive active")
					keepaliveSeen = true
				}
			}
		}
		// Stop guacd when the browser half closes. This also unblocks the
		// opposite copy direction if guacd has no pending display output.
		_ = upstream.Close()
		close(done)
	}()
	if err := forwardGuacdInstructions(ctx, reader, outbound, guacdInstructionDiagnostics(func(line string) {
		s.ctrl.Remote().AddDiagnostic(id, line)
	})); err != nil {
		s.ctrl.Remote().AddDiagnostic(id, "guacd RDP output ended: "+strings.TrimSpace(err.Error()))
	} else {
		s.ctrl.Remote().AddDiagnostic(id, "guacd RDP output ended cleanly")
	}
	cancel()
	<-done
}

// guacamoleWSWriter serializes all server-to-browser WebSocket messages. A
// guacd instruction and a tunnel ping response must never be interleaved.
type guacamoleWSWriter struct {
	mu sync.Mutex
	ws *websocket.Conn
}

func (w *guacamoleWSWriter) Write(ctx context.Context, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ws.Write(ctx, websocket.MessageText, data)
}

type guacamoleOutbound interface {
	Write(context.Context, []byte) error
}

func forwardBrowserInstructions(ctx context.Context, data []byte, upstream net.Conn, outbound guacamoleOutbound) (bool, error) {
	return forwardBrowserInstructionsWithObserver(ctx, data, upstream, outbound, nil)
}

func forwardBrowserInstructionsWithObserver(ctx context.Context, data []byte, upstream net.Conn, outbound guacamoleOutbound, observe func(guacd.Instruction, bool)) (bool, error) {
	controls, err := guacd.ForwardClientInstructionsWithObserver(data, upstream, observe)
	if err != nil {
		return false, err
	}
	if len(controls) == 0 {
		return false, nil
	}
	if err := outbound.Write(ctx, controls); err != nil {
		return false, err
	}
	return true, nil
}

func browserInstructionDiagnostics(add func(string)) func(guacd.Instruction, bool) {
	deduplicated := map[string]map[bool]bool{}
	return func(instruction guacd.Instruction, forwarded bool) {
		phase := "received"
		if forwarded {
			phase = "forwarded"
		}
		line := ""
		switch instruction.Opcode {
		case "sync":
			if len(instruction.Args) == 1 && safeGuacamoleDiagnosticValue(instruction.Args[0]) {
				line = "browser -> sync " + phase + " timestamp=" + instruction.Args[0]
			}
		case "size":
			if len(instruction.Args) >= 2 && safeGuacamoleDiagnosticValue(instruction.Args[0]) && safeGuacamoleDiagnosticValue(instruction.Args[1]) {
				line = "browser -> size " + phase + " " + instruction.Args[0] + "x" + instruction.Args[1]
			}
		case "nop", "disconnect":
			line = "browser -> " + instruction.Opcode + " " + phase
		case "ack":
			if len(instruction.Args) >= 3 && safeGuacamoleDiagnosticValue(instruction.Args[0]) && safeGuacamoleDiagnosticValue(instruction.Args[len(instruction.Args)-1]) {
				line = "browser -> ack " + phase + " stream=" + instruction.Args[0] + " code=" + instruction.Args[len(instruction.Args)-1]
			}
		case "mouse", "key":
			if deduplicated[instruction.Opcode] == nil {
				deduplicated[instruction.Opcode] = map[bool]bool{}
			}
			if !deduplicated[instruction.Opcode][forwarded] {
				deduplicated[instruction.Opcode][forwarded] = true
				line = "browser -> " + instruction.Opcode + " " + phase
			}
		}
		if line != "" {
			add(line)
		}
	}
}

func safeGuacamoleDiagnosticValue(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func forwardGuacdInstructions(ctx context.Context, reader *bufio.Reader, outbound guacamoleOutbound, observe func(guacd.Instruction)) error {
	for {
		instruction, err := guacd.DecodeInstruction(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if observe != nil {
			observe(instruction)
		}
		encoded, err := guacd.EncodeTunnelInstruction(instruction)
		if err != nil {
			return err
		}
		if err := outbound.Write(ctx, encoded); err != nil {
			return err
		}
	}
}

func guacdInstructionDiagnostics(add func(string)) func(guacd.Instruction) {
	count := 0
	firstImageStream := ""
	firstImageRecorded := false
	awaitingFirstImageSync := false
	return func(instruction guacd.Instruction) {
		if count < 10 {
			count++
			line := "guacd -> " + instruction.Opcode
			if instruction.Opcode == "error" && len(instruction.Args) >= 2 && safeGuacamoleStatusCode(instruction.Args[1]) {
				line += " (code=" + instruction.Args[1] + ")"
			}
			add(line)
		}

		// The first image stream confirms that the complete Guacamole image
		// sequence crossed the guacd-to-browser relay. Record only structural
		// metadata: image payloads can be both large and sensitive.
		switch instruction.Opcode {
		case "img":
			if !firstImageRecorded && len(instruction.Args) >= 4 {
				firstImageStream = instruction.Args[1]
				firstImageRecorded = true
				add("guacd -> img stream=" + firstImageStream + " layer=" + instruction.Args[2] + " mimetype=" + instruction.Args[3])
			}
		case "blob":
			if firstImageStream != "" && len(instruction.Args) >= 2 && instruction.Args[0] == firstImageStream {
				add("guacd -> blob stream=" + firstImageStream + " chars=" + strconv.Itoa(len(instruction.Args[1])))
			}
		case "end":
			if firstImageStream != "" && len(instruction.Args) >= 1 && instruction.Args[0] == firstImageStream {
				add("guacd -> end stream=" + firstImageStream)
				firstImageStream = ""
				awaitingFirstImageSync = true
			}
		case "sync":
			if awaitingFirstImageSync {
				add("guacd -> sync after first img")
				awaitingFirstImageSync = false
			}
		}
	}
}

func safeGuacamoleStatusCode(value string) bool {
	if value == "" || len(value) > 6 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (s *Server) failRDPTransport(ws *websocket.Conn, id string, err error) {
	// Handshake errors are safe: the codec never includes connect argument
	// values. Record the useful cause server-side and avoid leaking it through
	// Guacamole's opaque numeric browser status. Its WebSocket client parses
	// the close reason as a Guacamole status code, so prefix the safe detail
	// with 512 and preserve it for the browser as well as diagnostics.
	detail := strings.TrimSpace(err.Error())
	s.ctrl.Remote().AddDiagnostic(id, "tunnel error: "+detail)
	_ = ws.Close(websocket.StatusInternalError, "512 Could not establish tunnel to guacd: "+detail)
}

func rdpSetupDiagnostics(id, host string, port int, options model.RDPRemoteOptions, supplied bool) string {
	credentialState := "omitted"
	if supplied {
		credentialState = "supplied"
	}
	layout := options.ServerLayout
	if layout == "" {
		layout = "server default"
	}
	return fmt.Sprintf("RDP session %s\nguacd endpoint: %s\ntarget: %s:%d\ncredentials: %s\nsecurity: %s\nserver-layout: %s", id, net.JoinHostPort(host, strconv.Itoa(port)), options.Host, options.Port, credentialState, options.SecurityMode, layout)
}

func (s *Server) consumeRDPTransportTicket(ticket, id string) bool {
	s.rdpTicketMu.Lock()
	defer s.rdpTicketMu.Unlock()
	value, ok := s.rdpTickets[ticket]
	if ok {
		delete(s.rdpTickets, ticket)
	}
	return ok && value.SessionID == id && time.Now().Before(value.Expires)
}
func (s *Server) putRDPCredentials(id string, value rdpCredentials) {
	s.rdpCredentialMu.Lock()
	s.rdpCredentials[id] = value
	s.rdpCredentialMu.Unlock()
}
func (s *Server) takeRDPCredentials(id string) (rdpCredentials, bool) {
	s.rdpCredentialMu.Lock()
	defer s.rdpCredentialMu.Unlock()
	value, ok := s.rdpCredentials[id]
	delete(s.rdpCredentials, id)
	return value, ok
}
func (s *Server) clearRDPCredentials(id string) {
	value, ok := s.takeRDPCredentials(id)
	if ok {
		value.Password = ""
	}
}
func rdpTransportError(err error) error { return fmt.Errorf("RDP transport failed: %w", err) }
