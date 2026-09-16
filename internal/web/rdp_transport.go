package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
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
		Subprotocols:         []string{"guacamole"},
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
	reader, err := guacd.ConnectRDPWithStages(ctx, upstream, *session.RDP, guacd.Credentials{Username: credentials.Username, Domain: credentials.Domain, Password: credentials.Password}, guacd.ClientInfo{Width: width, Height: height, DPI: dpi, TimeZone: r.URL.Query().Get("timezone"), ImageMimetypes: []string{"image/webp", "image/png", "image/jpeg"}}, func(stage string) { s.ctrl.Remote().AddDiagnostic(id, stage) })
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
	if err := ws.Write(ctx, websocket.MessageText, tunnelReady); err != nil {
		s.ctrl.Remote().AddDiagnostic(id, "browser tunnel setup failed: "+strings.TrimSpace(err.Error()))
		return
	}
	s.ctrl.Remote().AddDiagnostic(id, "browser tunnel attached")
	// Guacamole's browser client requires inbound protocol activity within 15
	// seconds. Its own ping is normally echoed below, but a proxy that drops a
	// browser-to-server frame would otherwise make a valid, slow RDP setup look
	// like status 514. This internal control instruction is safe and never
	// reaches guacd or contains connection data.
	keepaliveStop := make(chan struct{})
	keepaliveDone := make(chan struct{})
	defer func() {
		cancel()
		close(keepaliveStop)
		<-keepaliveDone
	}()
	go func() {
		defer close(keepaliveDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		sent := false
		for {
			select {
			case <-keepaliveStop:
				return
			case <-ticker.C:
				keepalive, err := guacd.TunnelKeepalive()
				if err != nil {
					return
				}
				if err := ws.Write(ctx, websocket.MessageText, keepalive); err != nil {
					s.ctrl.Remote().AddDiagnostic(id, "browser tunnel server keepalive failed: "+strings.TrimSpace(err.Error()))
					return
				}
				if !sent {
					s.ctrl.Remote().AddDiagnostic(id, "browser tunnel server keepalive active")
					sent = true
				}
			}
		}
	}()
	// WebSocketTunnel exchanges raw Guacamole instruction text. NetConn keeps
	// that stream intact without creating a second protocol or accepting any
	// browser-controlled destination fields.
	stream := websocket.NetConn(ctx, ws, websocket.MessageText)
	done := make(chan struct{})
	go func() {
		keepaliveSeen := false
		for {
			messageType, data, err := ws.Read(ctx)
			if err != nil {
				s.ctrl.Remote().AddDiagnostic(id, "browser tunnel read ended: "+strings.TrimSpace(err.Error()))
				break
			}
			if messageType != websocket.MessageText {
				s.ctrl.Remote().AddDiagnostic(id, "browser tunnel received a non-text message")
				break
			}
			controls, err := guacd.ForwardClientInstructions(data, upstream)
			if err != nil {
				s.ctrl.Remote().AddDiagnostic(id, "browser tunnel protocol error: "+strings.TrimSpace(err.Error()))
				break
			}
			if len(controls) > 0 {
				if !keepaliveSeen {
					s.ctrl.Remote().AddDiagnostic(id, "browser tunnel keepalive active")
					keepaliveSeen = true
				}
				if err := ws.Write(ctx, websocket.MessageText, controls); err != nil {
					s.ctrl.Remote().AddDiagnostic(id, "browser tunnel keepalive failed: "+strings.TrimSpace(err.Error()))
					break
				}
			}
		}
		// Stop guacd when the browser half closes. This also unblocks the
		// opposite copy direction if guacd has no pending display output.
		_ = upstream.Close()
		close(done)
	}()
	output := &rdpTunnelOutput{writer: stream, first: func() {
		s.ctrl.Remote().AddDiagnostic(id, "received guacd RDP output")
	}}
	if _, err := io.Copy(output, reader); err != nil {
		s.ctrl.Remote().AddDiagnostic(id, "guacd RDP output ended: "+strings.TrimSpace(err.Error()))
	} else {
		s.ctrl.Remote().AddDiagnostic(id, "guacd RDP output ended cleanly")
	}
	cancel()
	_ = stream.Close()
	<-done
}

type rdpTunnelOutput struct {
	writer io.Writer
	first  func()
	seen   bool
}

func (w *rdpTunnelOutput) Write(data []byte) (int, error) {
	if len(data) > 0 && !w.seen {
		w.seen = true
		w.first()
	}
	return w.writer.Write(data)
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
