package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/core"
)

func TestTerminalTicketRequiresAuthAndIsSingleUse(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl, "/runpilot")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Handler()
	body := []byte(`{"shell":"","cols":100,"rows":30}`)
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/runpilot/api/v1/terminal/ticket", bytes.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized ticket request = %d", unauthorized.Code)
	}
	missingTicket := httptest.NewRecorder()
	handler.ServeHTTP(missingTicket, httptest.NewRequest(http.MethodGet, "/runpilot/api/v1/terminal/connect", nil))
	if missingTicket.Code != http.StatusUnauthorized {
		t.Fatalf("websocket request without ticket = %d", missingTicket.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/runpilot/api/v1/terminal/ticket", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("ticket status = %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Ticket string `json:"ticket"`
		URL    string `json:"url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Ticket == "" {
		t.Fatal("ticket response omitted ticket")
	}
	if result.URL != "" {
		t.Fatalf("ticket response must not include a connection URL: %q", result.URL)
	}
	if _, ok := server.consumeTerminalTicket(result.Ticket); !ok {
		t.Fatal("fresh ticket was rejected")
	}
	if _, ok := server.consumeTerminalTicket(result.Ticket); ok {
		t.Fatal("ticket was accepted twice")
	}
}

func TestTerminalTicketExpires(t *testing.T) {
	server := &Server{terminalTickets: map[string]terminalTicket{"expired": {Expires: time.Now().Add(-time.Second)}}}
	if _, ok := server.consumeTerminalTicket("expired"); ok {
		t.Fatal("expired ticket was accepted")
	}
}
