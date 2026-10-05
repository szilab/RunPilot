package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/core"
)

func TestLegacyRemotePageAndProviderAPIsAreGone(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	token := ctrl.Snapshot().Server.Token
	for _, path := range []string{"/remote/session/example", "/api/v1/remote/providers", "/api/v1/remote/targets", "/api/v1/remote/sessions", "/api/v1/remote/sessions/example/transport"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("legacy path %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), `data-page="remote"`) || strings.Contains(string(index), `id="remotePage"`) {
		t.Fatal("combined Remote navigation or page remains")
	}
}

func TestXpraClientTicketRedirectStaysOnTheScopedProxyPath(t *testing.T) {
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
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got, want := response.Header().Get("Location"), "/runpilot/api/v1/remote/sessions/session/client/"; got != want {
		t.Fatalf("redirect=%q want=%q", got, want)
	}
}

func TestXpraProxyRedirectUsesOnlySessionSnapshotParameters(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	server.remoteTickets["ticket"] = remoteClientTicket{SessionID: "session", ClientParams: map[string]string{"encoding": "webp", "video": "no", "toolbar_position": "top-left"}, Expires: time.Now().Add(time.Minute)}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/remote/sessions/session/client/?ticket=ticket&encoding=rgb", nil))
	if response.Code != http.StatusFound {
		t.Fatalf("status=%d", response.Code)
	}
	location := response.Header().Get("Location")
	if !strings.Contains(location, "encoding=webp") || strings.Contains(location, "encoding=rgb") || !strings.Contains(location, "toolbar_position=top-left") {
		t.Fatalf("redirect did not use the Xpra session snapshot: %q", location)
	}
}
