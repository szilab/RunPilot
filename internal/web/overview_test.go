package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestOverviewEndpointProvidesHostAndServiceStatus(t *testing.T) {
	ctrl, err := openReadyController(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
	response := httptest.NewRecorder()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var overview model.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if overview.Host.OS == "" {
		t.Fatal("overview is missing host operating system")
	}
	if overview.JobCount != 0 {
		t.Fatalf("empty controller jobs = %d", overview.JobCount)
	}
}

func TestSystemEndpointProvidesCapabilities(t *testing.T) {
	ctrl, err := openReadyController(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
	request.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
	response := httptest.NewRecorder()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Capabilities struct {
			OS string `json:"os"`
		} `json:"capabilities"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Capabilities.OS == "" {
		t.Fatal("system response is missing capabilities")
	}
}

func TestHiddenLegacyFeaturesAreNotRegistered(t *testing.T) {
	ctrl, err := openReadyController(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	token := ctrl.Snapshot().Server.Token
	get := func(handler http.Handler, path string, authorized bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if authorized {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	production := server.Handler()
	for _, path := range []string{"/api/v1/processes", "/api/v1/runs"} {
		if response := get(production, path, true); response.Code != http.StatusNotFound {
			t.Errorf("removed API %s = %d", path, response.Code)
		}
	}
	legacyPaths := []string{"/api/v1/jobs", "/api/v1/storage", "/api/v1/software/providers", "/api/v1/docker", "/api/v1/docker/attach", "/api/v1/remote/sessions/example/client/"}
	for _, path := range legacyPaths {
		if response := get(production, path, true); response.Code != http.StatusNotFound {
			t.Errorf("unregistered legacy API %s = %d: %s", path, response.Code, response.Body.String())
		}
	}
	if response := get(production, "/api/v1/overview", true); response.Code != http.StatusOK {
		t.Errorf("/api/v1/overview = %d: %s", response.Code, response.Body.String())
	}

	// The handlers remain in the codebase until plugin parity; keep them
	// authenticated and working when the routes are mounted.
	server.legacyRoutes = true
	legacy := server.Handler()
	for _, path := range []string{"/api/v1/jobs", "/api/v1/storage", "/api/v1/software/providers"} {
		if response := get(legacy, path, true); response.Code != http.StatusOK {
			t.Errorf("legacy %s = %d: %s", path, response.Code, response.Body.String())
		}
	}
	if response := get(legacy, "/api/v1/docker", false); response.Code != http.StatusUnauthorized {
		t.Errorf("Docker legacy API authentication = %d: %s", response.Code, response.Body.String())
	}
}
