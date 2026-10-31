package core

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
)

// TestRemoteXpraPluginWASMRunsPrivateServer drives the compiled Remote plugin
// against a real Xpra server: the server runs in the plugin workspace, the
// stream attaches through its Unix socket, and closing stops and cleans it up.
func TestRemoteXpraPluginWASMRunsPrivateServer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Xpra servers are Linux-only")
	}
	if _, err := exec.LookPath("xpra"); err != nil {
		t.Skip("xpra is not installed")
	}
	dataDir := t.TempDir()
	copyRemoteVNCPlugin(t, dataDir)
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error {
		enabled := true
		cfg.Plugins = map[string]model.PluginSettings{remoteVNCPluginID: {Enabled: &enabled}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	controller, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	call := func(method string, params any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(params)
		response, err := controller.PluginCall(context.Background(), remoteVNCPluginID, method, raw)
		if err != nil || pluginResponseHasError(response) {
			t.Fatalf("%s: %s %v", method, response, err)
		}
		var decoded map[string]any
		_ = json.Unmarshal(response, &decoded)
		return decoded
	}

	if status := call("xpra.status", nil); status["available"] != true {
		t.Fatalf("status = %v", status)
	}
	// A plain long-running command keeps the application session alive
	// without needing any X client installed.
	saved := call("xpra.targets.save", map[string]any{"target": map[string]any{"name": "Sleeper", "command": "sleep 120"}})
	targetID := saved["target"].(map[string]any)["id"]
	session := call("xpra.session.open", map[string]any{"targetId": targetID})["session"].(map[string]any)
	sessionID := session["id"].(string)
	sessionDir := filepath.Join(dataDir, "plugins", remoteVNCPluginID, "data", "workspace", "xpra", sessionID)

	var streamID string
	deadline := time.Now().Add(30 * time.Second)
	for streamID == "" {
		attached := call("xpra.session.attach", map[string]any{"id": sessionID})
		if attached["ready"] == true {
			streamID = attached["streamId"].(string)
			break
		}
		if time.Now().After(deadline) {
			diagnostics := call("xpra.session.diagnostics", map[string]any{"id": sessionID})
			t.Fatalf("Xpra server did not accept connections: %v", diagnostics["log"])
		}
		time.Sleep(300 * time.Millisecond)
	}
	if streamID == "" {
		t.Fatal("no stream ID")
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Mode()&os.ModeSocket != 0 && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("Xpra socket %s is accessible by other users: %v", entry.Name(), info.Mode())
		}
	}

	call("xpra.session.close", map[string]any{"id": sessionID})
	deadline = time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session directory was not removed after the server stopped")
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The plugin keeps working after the server's output and exit events.
	if listed := call("xpra.targets.list", nil); len(listed["targets"].([]any)) != 1 {
		t.Fatalf("targets after session = %v", listed)
	}
}
