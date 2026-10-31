package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegacyTerminalEndpointsAreRemoved(t *testing.T) {
	ctrl, err := openReadyController(t, t.TempDir())
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
