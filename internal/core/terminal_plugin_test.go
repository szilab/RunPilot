package core

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

const terminalPluginID = "terminal"

func packageTerminalForTest(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "plugins", terminalPluginID)
	path := filepath.Join(t.TempDir(), "terminal.rpplugin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	err = filepath.Walk(root, func(name string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		entry, err := archive.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		input, err := os.Open(name)
		if err != nil {
			return err
		}
		defer input.Close()
		_, err = io.Copy(entry, input)
		return err
	})
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func terminalCall(t *testing.T, c *Controller, method string, params any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(params)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, protocolErr := c.PluginCall(ctx, terminalPluginID, method, raw)
	if protocolErr != nil {
		t.Fatalf("%s: %s", method, protocolErr.Message)
	}
	var out map[string]any
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("%s result %s: %v", method, result, err)
	}
	if failure, ok := out["error"]; ok {
		t.Fatalf("%s failed: %v", method, failure)
	}
	return out
}

func TestTerminalPluginInstallEnableRestartAndInteractiveSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("interactive shell integration uses the Linux /bin/sh fixture")
	}
	dataDir := t.TempDir()
	if _, err := plugins.InstallPackage(filepath.Join(dataDir, "plugins"), packageTerminalForTest(t), ""); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetPluginEnabled(terminalPluginID, true); err != nil {
		t.Fatal(err)
	}
	if !c.Plugins().RestartRequired() {
		t.Fatal("enabling plugin did not require restart")
	}
	c.Close()
	c, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	loaded := false
	for _, status := range c.Plugins().Statuses() {
		if status.Manifest.ID == terminalPluginID {
			loaded = status.Loaded && status.Enabled
		}
	}
	if !loaded {
		t.Fatal("terminal backend was not activated after restart")
	}
	foundEntry := false
	for _, ext := range c.Plugins().FrontendExtensions() {
		if ext.ID == terminalPluginID {
			foundEntry = true
		}
	}
	if !foundEntry {
		t.Fatal("terminal frontend was not activated after restart")
	}

	events, unsubscribe := c.SubscribePluginEvents()
	defer unsubscribe()
	opened := terminalCall(t, c, "terminal.open", map[string]any{"rows": 24, "columns": 80})
	session := opened["session"].(map[string]any)
	id := session["id"].(string)
	terminalCall(t, c, "terminal.resize", map[string]any{"id": id, "rows": 31, "columns": 101})
	terminalCall(t, c, "terminal.status", map[string]any{"id": id})
	input := base64.StdEncoding.EncodeToString([]byte("printf 'RP_TERMINAL_PLUGIN_OK\\n'\n"))
	terminalCall(t, c, "terminal.write", map[string]any{"id": id, "data": input})
	seenOutput := false
	deadline := time.After(8 * time.Second)
	for !seenOutput {
		select {
		case event := <-events:
			if event.Plugin != terminalPluginID || event.Event != "process.session.output" {
				continue
			}
			var output struct {
				ID   string `json:"id"`
				Data string `json:"data"`
			}
			_ = json.Unmarshal(event.Data, &output)
			if output.ID == id {
				bytes, _ := base64.StdEncoding.DecodeString(output.Data)
				seenOutput = strings.Contains(string(bytes), "RP_TERMINAL_PLUGIN_OK")
			}
		case <-deadline:
			t.Fatal("timed out waiting for terminal output")
		}
	}
	terminalCall(t, c, "terminal.close", map[string]any{"id": id, "force": true})
}
