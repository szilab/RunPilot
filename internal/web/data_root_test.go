package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/config"
)

func TestSettingsDataDirectorySwitchDoesNotMigrateData(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RUNPILOT_DATA_ROOT_FILE", filepath.Join(base, "bootstrap", "data-root"))
	t.Setenv("RUNPILOT_DATA_DIR", "")
	source := filepath.Join(base, "source")
	ctrl, err := openReadyController(t, source)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := config.WriteDataRoot(source); err != nil {
		t.Fatal(err)
	}
	// Existing Compose state must not prevent a directory selection without copying.
	if err := os.MkdirAll(filepath.Join(source, "compose", "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "prepared")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "keep"), []byte("prepared data"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan struct{}, 1)
	server.SetRestartHandler(func() { restarted <- struct{}{} })
	current := httptest.NewRecorder()
	server.handleDataRootGet(current, httptest.NewRequest(http.MethodGet, "/", nil))
	var state struct {
		DataDir       string `json:"dataDir"`
		ChangeAllowed bool   `json:"changeAllowed"`
	}
	if err := json.Unmarshal(current.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.DataDir != source || !state.ChangeAllowed {
		t.Fatalf("unexpected current settings: %+v", state)
	}
	body, _ := json.Marshal(map[string]string{"destination": destination})
	response := httptest.NewRecorder()
	server.handleDataRootChange(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("restart not requested")
	}
	if err := config.ApplyPendingDataRoot(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "runpilot.yaml")); err != nil {
		t.Fatal("source config changed:", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "runpilot.yaml")); !os.IsNotExist(err) {
		t.Fatal("config copied:", err)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "keep")); err != nil || string(data) != "prepared data" {
		t.Fatalf("prepared data changed: %q %v", data, err)
	}
	server.SetDataRootSwitchAllowed(false)
	blocked := httptest.NewRecorder()
	server.handleDataRootChange(blocked, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))))
	if blocked.Code != http.StatusConflict {
		t.Fatalf("override allowed a switch: %d", blocked.Code)
	}
}
