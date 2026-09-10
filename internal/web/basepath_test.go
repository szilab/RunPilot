package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/szilab/RunPilot/internal/core"
)

func TestBasePathRoutesUIAndAPI(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	server, err := New(ctrl, "/runpilot/")
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	redirect := httptest.NewRecorder()
	handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/runpilot", nil))
	if redirect.Code != http.StatusPermanentRedirect || redirect.Header().Get("Location") != "/runpilot/" {
		t.Fatalf("base path redirect = %d, location %q", redirect.Code, redirect.Header().Get("Location"))
	}

	request := httptest.NewRequest(http.MethodGet, "/runpilot/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("prefixed API status = %d, body = %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unprefixed API status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestBasePathValidation(t *testing.T) {
	if _, err := New(nil, "runpilot"); err == nil {
		t.Fatal("base path without leading slash was accepted")
	}
	if _, err := New(nil, "/runpilot?x=1"); err == nil {
		t.Fatal("base path with query was accepted")
	}
}
