package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/plugins"
	"github.com/szilab/RunPilot/internal/websocketsecure"
)

const applicationTicketTTL = time.Minute

const (
	applicationStreamChunk = 32 << 10
	applicationStreamFrame = 38 + applicationStreamChunk
	applicationStreamCount = 64
)

var applicationStreamMagic = [4]byte{'R', 'P', 'S', '1'}

type applicationStreamAttachment struct {
	plugin  string
	conn    net.Conn
	release func()
}

type applicationStreamControl struct {
	Type     string `json:"type"`
	Plugin   string `json:"plugin"`
	StreamID string `json:"streamId"`
}

func (s *Server) handleApplicationTicket(w http.ResponseWriter, r *http.Request) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ticket := hex.EncodeToString(raw[:])
	s.applicationTicketMu.Lock()
	s.applicationTickets[ticket] = time.Now().Add(applicationTicketTTL)
	s.applicationTicketMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"ticket": ticket})
}

func (s *Server) consumeApplicationTicket(ticket string) bool {
	s.applicationTicketMu.Lock()
	defer s.applicationTicketMu.Unlock()
	expires, ok := s.applicationTickets[ticket]
	delete(s.applicationTickets, ticket)
	return ok && time.Now().Before(expires)
}

func (s *Server) handleApplicationWS(w http.ResponseWriter, r *http.Request) {
	grant, restricted := s.ctrl.ConsumeBrowserStreamTicket(r.URL.Query().Get("ticket"))
	if !restricted && !s.consumeApplicationTicket(r.URL.Query().Get("ticket")) {
		http.Error(w, "application connection expired", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(websocketsecure.MaxEncryptedMessageSize)
	mode := requestWebSocketPayloadMode(r, s.ctrl.Snapshot().Server.WebSocketPayloadMode)
	if restricted {
		mode = websocketsecure.ModeRequired
	}
	secure, err := websocketsecure.ServerHandshake(r.Context(), conn, mode)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "secure WebSocket negotiation failed")
		return
	}
	var writeMu sync.Mutex
	write := func(ctx context.Context, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return secure.Write(ctx, websocket.MessageText, data)
	}
	writeRaw := func(ctx context.Context, kind websocket.MessageType, value []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return secure.Write(ctx, kind, value)
	}
	var attachmentMu sync.Mutex
	attachments := map[string]applicationStreamAttachment{}
	defer func() {
		attachmentMu.Lock()
		remaining := make([]applicationStreamAttachment, 0, len(attachments))
		for _, attachment := range attachments {
			remaining = append(remaining, attachment)
		}
		attachments = map[string]applicationStreamAttachment{}
		attachmentMu.Unlock()
		for _, attachment := range remaining {
			attachment.release()
		}
	}()
	if !restricted {
		events, unsubscribe := s.ctrl.SubscribePluginEvents()
		defer unsubscribe()
		go func() {
			for event := range events {
				ctx, cancel := context.WithTimeout(r.Context(), time.Second)
				_ = write(ctx, event) // bounded channel drops for slow/disconnected clients.
				cancel()
			}
		}()
	}
	for {
		kind, data, err := secure.Read(r.Context())
		if err != nil {
			return
		}
		if kind == websocket.MessageBinary {
			streamID, payload, frameErr := decodeApplicationStreamFrame(data, 1)
			if frameErr != nil {
				_ = write(r.Context(), map[string]any{"type": "stream.error", "code": "bad_request"})
				continue
			}
			attachmentMu.Lock()
			attachment, ok := attachments[streamID]
			attachmentMu.Unlock()
			if !ok {
				_ = write(r.Context(), map[string]any{"type": "stream.error", "streamId": streamID, "code": "not_found"})
				continue
			}
			if err := writeNetworkStream(r.Context(), attachment.conn, payload); err != nil {
				attachmentMu.Lock()
				delete(attachments, streamID)
				attachmentMu.Unlock()
				attachment.release()
				_ = write(r.Context(), map[string]any{"type": "stream.closed", "streamId": streamID})
			}
			continue
		}
		if kind != websocket.MessageText {
			_ = write(r.Context(), plugins.Failure("", "bad_request", "application messages must be JSON text"))
			continue
		}
		var control applicationStreamControl
		if json.Unmarshal(data, &control) == nil && control.Type != "" {
			if restricted && (control.Plugin != grant.Owner && control.Type == "stream.attach" || control.StreamID != grant.StreamID) {
				_ = write(r.Context(), map[string]any{"type": "stream.error", "code": "forbidden", "streamId": control.StreamID})
				continue
			}
			switch control.Type {
			case "stream.attach":
				if control.Plugin == "" || control.StreamID == "" {
					_ = write(r.Context(), map[string]any{"type": "stream.error", "streamId": control.StreamID, "code": "invalid_argument"})
					continue
				}
				attachmentMu.Lock()
				_, duplicate := attachments[control.StreamID]
				atLimit := len(attachments) >= applicationStreamCount
				attachmentMu.Unlock()
				if duplicate || atLimit {
					_ = write(r.Context(), map[string]any{"type": "stream.error", "streamId": control.StreamID, "code": "failed_precondition"})
					continue
				}
				var stream net.Conn
				var release func()
				var attachErr error
				if restricted {
					stream, release, attachErr = s.ctrl.AttachBrowserStream(grant.Owner, grant.StreamID)
				} else {
					stream, release, attachErr = s.ctrl.AttachPluginNetworkStream(control.Plugin, control.StreamID)
				}
				if attachErr != nil {
					code := "not_found"
					var failure *plugins.HostFailure
					if errors.As(attachErr, &failure) {
						code = failure.Code
					}
					_ = write(r.Context(), map[string]any{"type": "stream.error", "streamId": control.StreamID, "code": code})
					continue
				}
				attachment := applicationStreamAttachment{plugin: control.Plugin, conn: stream, release: release}
				attachmentMu.Lock()
				attachments[control.StreamID] = attachment
				attachmentMu.Unlock()
				if err := write(r.Context(), map[string]any{"type": "stream.attached", "streamId": control.StreamID}); err != nil {
					return
				}
				forwardWrite := write
				if restricted {
					forwardWrite = func(ctx context.Context, value any) error {
						err := write(ctx, value)
						_ = conn.Close(websocket.StatusNormalClosure, "browser stream closed")
						return err
					}
				}
				go s.forwardNetworkStream(r.Context(), control.StreamID, stream, release, attachments, &attachmentMu, writeRaw, forwardWrite)
				continue
			case "stream.close":
				attachmentMu.Lock()
				attachment, ok := attachments[control.StreamID]
				delete(attachments, control.StreamID)
				attachmentMu.Unlock()
				if ok {
					attachment.release()
					_ = write(r.Context(), map[string]any{"type": "stream.closed", "streamId": control.StreamID})
				} else {
					_ = write(r.Context(), map[string]any{"type": "stream.error", "streamId": control.StreamID, "code": "not_found"})
				}
				continue
			default:
				_ = write(r.Context(), map[string]any{"type": "stream.error", "streamId": control.StreamID, "code": "bad_request"})
				continue
			}
		}
		if restricted {
			_ = write(r.Context(), plugins.Failure("", "forbidden", "connection is restricted to one browser stream"))
			continue
		}
		request, err := plugins.ParseRequest(data)
		if err != nil {
			_ = write(r.Context(), plugins.Failure("", "bad_request", err.Error()))
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), plugins.DefaultCallTimeout)
		result, protocolErr := s.ctrl.PluginCall(ctx, request.Plugin, request.Method, request.Params)
		cancel()
		response := plugins.Response{ID: request.ID, Result: result}
		if protocolErr != nil {
			response = plugins.Failure(request.ID, protocolErr.Code, protocolErr.Message)
		}
		if err := write(r.Context(), response); err != nil {
			return
		}
		// A read operation may publish a fresh snapshot as an event as well as a
		// correlated response. This generic convention lets a plugin share one
		// data update with pages/cards without a feature-specific socket.
		if protocolErr == nil && strings.HasSuffix(request.Method, ".get") {
			_ = write(r.Context(), plugins.Event{Plugin: request.Plugin, Event: strings.TrimSuffix(request.Method, ".get") + ".changed", Data: result})
		}
	}
}

func decodeApplicationStreamFrame(data []byte, expectedType byte) (string, []byte, error) {
	if len(data) < 6 || len(data) > applicationStreamFrame || string(data[:4]) != string(applicationStreamMagic[:]) || data[4] != expectedType {
		return "", nil, errors.New("invalid stream frame")
	}
	idLength := int(data[5])
	if idLength == 0 || len(data) <= 6+idLength {
		return "", nil, errors.New("invalid stream frame")
	}
	return string(data[6 : 6+idLength]), data[6+idLength:], nil
}

func encodeApplicationStreamFrame(streamID string, payload []byte) []byte {
	data := make([]byte, 6+len(streamID)+len(payload))
	copy(data[:4], applicationStreamMagic[:])
	data[4] = 2
	data[5] = byte(len(streamID))
	copy(data[6:], streamID)
	copy(data[6+len(streamID):], payload)
	return data
}

func writeNetworkStream(ctx context.Context, conn net.Conn, data []byte) error {
	deadline := time.Now().Add(5 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = conn.SetWriteDeadline(deadline)
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (s *Server) forwardNetworkStream(ctx context.Context, streamID string, conn net.Conn, release func(), attachments map[string]applicationStreamAttachment, attachmentMu *sync.Mutex, writeRaw func(context.Context, websocket.MessageType, []byte) error, write func(context.Context, any) error) {
	defer func() {
		attachmentMu.Lock()
		if current, ok := attachments[streamID]; ok && current.conn == conn {
			delete(attachments, streamID)
		}
		attachmentMu.Unlock()
		release()
		if ctx.Err() == nil {
			writeCtx, cancel := context.WithTimeout(ctx, time.Second)
			_ = write(writeCtx, map[string]any{"type": "stream.closed", "streamId": streamID})
			cancel()
		}
	}()
	buffer := make([]byte, applicationStreamChunk)
	for {
		n, err := conn.Read(buffer)
		if n > 0 {
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			writeErr := writeRaw(writeCtx, websocket.MessageBinary, encodeApplicationStreamFrame(streamID, buffer[:n]))
			cancel()
			if writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
