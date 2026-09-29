package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/plugins"
	"github.com/szilab/RunPilot/internal/websocketsecure"
)

const applicationTicketTTL = time.Minute

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
	if !s.consumeApplicationTicket(r.URL.Query().Get("ticket")) {
		http.Error(w, "application connection expired", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(websocketsecure.MaxEncryptedMessageSize)
	secure, err := websocketsecure.ServerHandshake(r.Context(), conn, requestWebSocketPayloadMode(r, s.ctrl.Snapshot().Server.WebSocketPayloadMode))
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
	events, unsubscribe := s.ctrl.SubscribePluginEvents()
	defer unsubscribe()
	go func() {
		for event := range events {
			ctx, cancel := context.WithTimeout(r.Context(), time.Second)
			_ = write(ctx, event) // bounded channel drops for slow/disconnected clients.
			cancel()
		}
	}()
	for {
		kind, data, err := secure.Read(r.Context())
		if err != nil {
			return
		}
		if kind != websocket.MessageText {
			_ = write(r.Context(), plugins.Failure("", "bad_request", "application messages must be JSON text"))
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
