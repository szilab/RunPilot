package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/plugins"
)

func TestFirstRunGateAndTokenRotation(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	h := server.Handler()
	initial := ctrl.Snapshot().Server.Token
	for _, target := range []string{"/api/v1/system", "/api/v1/ws/ticket", "/api/v1/plugins", "/api/v1/storage", "/api/v1/software/providers", "/api/v1/web-apps", "/api/v1/terminal"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, target, nil))
		if r.Code != http.StatusPreconditionRequired {
			t.Errorf("%s status=%d body=%s", target, r.Code, r.Body.String())
		}
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "setup-token") {
		t.Fatal("focused setup UI missing")
	}
	bad := httptest.NewRecorder()
	h.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/api/v1/setup/token", bytes.NewBufferString(`{"current":"wrong","next":"012345678901234567890123"}`)))
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong initial token status=%d", bad.Code)
	}
	newToken := "brand-new-runpilot-token-0123456789"
	body, _ := json.Marshal(map[string]string{"current": initial, "next": newToken})
	changed := httptest.NewRecorder()
	h.ServeHTTP(changed, httptest.NewRequest(http.MethodPost, "/api/v1/setup/token", bytes.NewReader(body)))
	if changed.Code != http.StatusOK {
		t.Fatalf("change status=%d body=%s", changed.Code, changed.Body.String())
	}
	oldAuth := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
	oldAuth.Header.Set("Authorization", "Bearer "+initial)
	oldResponse := httptest.NewRecorder()
	h.ServeHTTP(oldResponse, oldAuth)
	if oldResponse.Code != http.StatusUnauthorized {
		t.Fatalf("old token remains valid: %d", oldResponse.Code)
	}
	newAuth := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
	newAuth.Header.Set("Authorization", "Bearer "+newToken)
	newResponse := httptest.NewRecorder()
	h.ServeHTTP(newResponse, newAuth)
	if newResponse.Code != http.StatusOK {
		t.Fatalf("new token rejected: %d %s", newResponse.Code, newResponse.Body.String())
	}
}

func TestPluginCallWorksAfterFirstRunSetup(t *testing.T) {
	dataDir := t.TempDir()
	if err := plugins.EnsureReferenceSystem(filepath.Join(dataDir, "plugins")); err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if err := store.Update(func(cfg *model.Config) error {
		cfg.Plugins = map[string]model.PluginSettings{}
		cfg.Plugins["system"] = model.PluginSettings{Enabled: &enabled}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctrl, err := core.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	initial := ctrl.Snapshot().Server.Token
	if err := ctrl.ChangeInitialToken(initial, "new-token-for-plugin-setup-test-123"); err != nil {
		t.Fatal(err)
	}
	result, protocolErr := ctrl.PluginCall(context.Background(), "system", "status.get", json.RawMessage(`{}`))
	if protocolErr != nil {
		t.Fatalf("plugin call after setup: %v", protocolErr)
	}
	var status map[string]any
	if err := json.Unmarshal(result, &status); err != nil {
		t.Fatal(err)
	}
	if status["hostname"] == nil {
		t.Fatalf("plugin returned no host status: %s", result)
	}
}
