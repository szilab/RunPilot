package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/core"
)

func TestLegacyTerminalEndpointsAreRemoved(t *testing.T) {
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

	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/runpilot/api/v1/terminal"},
		{http.MethodPost, "/runpilot/api/v1/terminal/ticket"},
		{http.MethodGet, "/runpilot/api/v1/terminal/connect"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s status = %d, want 404", test.method, test.path, response.Code)
		}
	}
}

func TestDockerAttachTicketIsSingleUseAndExpires(t *testing.T) {
	server := &Server{dockerAttachTickets: map[string]dockerAttachTicket{
		"fresh":   {DockerExecID: "container", Expires: time.Now().Add(time.Minute)},
		"expired": {DockerExecID: "container", Expires: time.Now().Add(-time.Second)},
	}}
	if _, ok := server.consumeDockerAttachTicket("fresh"); !ok {
		t.Fatal("fresh Docker attach ticket was rejected")
	}
	if _, ok := server.consumeDockerAttachTicket("fresh"); ok {
		t.Fatal("Docker attach ticket was accepted twice")
	}
	if _, ok := server.consumeDockerAttachTicket("expired"); ok {
		t.Fatal("expired Docker attach ticket was accepted")
	}
}
