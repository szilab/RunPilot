package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/core"
)

func TestRemoteTargetCRUDAndClientIsNotPublic(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	token := ctrl.Snapshot().Server.Token
	body := []byte(`{"name":"Firefox","provider":"xpra","type":"application","enabled":true,"command":{"path":"firefox","interpreter":"direct"}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/remote/targets", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/remote/targets", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("Firefox")) {
		t.Fatalf("list=%d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/remote/sessions/not-owned/client/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated client=%d", response.Code)
	}
}

func TestRemoteClientTicketStaysOnProxyPath(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl, "/runpilot")
	if err != nil {
		t.Fatal(err)
	}
	server.remoteTickets["ticket"] = remoteClientTicket{SessionID: "session", Expires: time.Now().Add(time.Minute)}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runpilot/api/v1/remote/sessions/session/client/?ticket=ticket", nil))
	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	want := "/runpilot/api/v1/remote/sessions/session/client/"
	if got := response.Header().Get("Location"); got != want {
		t.Fatalf("redirect = %q, want %q", got, want)
	}
}
