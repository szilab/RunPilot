package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
)

func TestOverviewEndpointProvidesHostAndServiceStatus(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
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
	if overview.ProcessCount != 0 || overview.JobCount != 0 {
		t.Fatalf("empty controller counts = %d processes, %d jobs", overview.ProcessCount, overview.JobCount)
	}
}

func TestSystemEndpointProvidesCapabilities(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
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
