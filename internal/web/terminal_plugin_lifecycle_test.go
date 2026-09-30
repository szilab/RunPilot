package web

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/plugins"
)

func terminalPluginPackage(t *testing.T) []byte {
	t.Helper()
	root := filepath.Join("..", "..", "plugins", "terminal")
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry, err := archive.Create(filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(entry, file)
		return err
	})
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestTerminalPluginUserLifecycleHTTP(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("lifecycle UI assertions run on the Linux integration host")
	}
	packageBytes := terminalPluginPackage(t)
	hash := sha256.Sum256(packageBytes)
	catalog := plugins.Catalog{SchemaVersion: 1, Plugins: []plugins.CatalogEntry{{
		ID: "terminal", Name: "Terminal (plugin)", Latest: "0.1.0",
		Versions: []plugins.CatalogVersion{{Version: "0.1.0", Platforms: []string{"linux", "windows"}, Requires: plugins.Requires{RunPilotAPI: 2, Backend: ">=1.0.0 <2.0.0", Frontend: ">=1.0.0 <2.0.0"}, URL: "PACKAGE", SHA256: hex.EncodeToString(hash[:])}},
	}}}
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog.json" {
			_ = json.NewEncoder(w).Encode(catalog)
			return
		}
		if r.URL.Path == "/terminal.rpplugin" {
			_, _ = w.Write(packageBytes)
			return
		}
		http.NotFound(w, r)
	}))
	defer registry.Close()
	catalog.Plugins[0].Versions[0].URL = registry.URL + "/terminal.rpplugin"
	dataDir := t.TempDir()
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error { cfg.PluginRegistry.URL = registry.URL + "/catalog.json"; return nil }); err != nil {
		t.Fatal(err)
	}
	otherState := filepath.Join(dataDir, "plugin-data", "tasks", "storage.json")
	if err := os.MkdirAll(filepath.Dir(otherState), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherState, []byte(`{"retained":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	terminalData := filepath.Join(dataDir, "plugin-data", "terminal", "value")
	if err := os.MkdirAll(filepath.Dir(terminalData), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(terminalData, []byte("retained"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctrl, err := core.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		return response
	}
	reopen := func() {
		t.Helper()
		ctrl.Close()
		var openErr error
		ctrl, openErr = core.Open(dataDir)
		if openErr != nil {
			t.Fatal(openErr)
		}
		server, openErr = New(ctrl)
		if openErr != nil {
			t.Fatal(openErr)
		}
	}
	defer func() { ctrl.Close() }()
	if response := request("GET", "/api/v1/plugins/catalog", "", http.StatusOK); !bytes.Contains(response.Body.Bytes(), []byte(`"id":"terminal"`)) {
		t.Fatal("Terminal was not discoverable in plugin catalog")
	}
	request("POST", "/api/v1/plugins/terminal/install", `{"version":"0.1.0"}`, http.StatusOK)
	status := ctrl.Plugins().Statuses()[0]
	if status.Enabled || status.Loaded {
		t.Fatalf("installed plugin should remain disabled until requested: %+v", status)
	}
	request("PUT", "/api/v1/plugins/terminal", `{"enabled":true}`, http.StatusOK)
	if len(ctrl.Plugins().FrontendExtensions()) != 0 {
		t.Fatal("enablement activated frontend without restart")
	}
	reopen()
	if len(ctrl.Plugins().FrontendExtensions()) != 1 || !ctrl.Plugins().Statuses()[0].Loaded {
		t.Fatal("restart did not activate Terminal plugin")
	}
	request("GET", "/plugins/terminal/web/plugin.js", "", http.StatusOK)
	for _, asset := range []string{"xterm.js", "addon-fit.js", "xterm.css", "LICENSE-xterm.txt", "LICENSE-addon-fit.txt"} {
		request("GET", "/plugins/terminal/web/vendor/"+asset, "", http.StatusOK)
	}
	if response := request("GET", "/api/v1/terminal", "", http.StatusOK); !bytes.Contains(response.Body.Bytes(), []byte(`"available":true`)) {
		t.Fatalf("legacy Terminal endpoint unavailable: %s", response.Body.String())
	}
	request("PUT", "/api/v1/plugins/terminal", `{"enabled":false}`, http.StatusOK)
	reopen()
	if len(ctrl.Plugins().FrontendExtensions()) != 0 {
		t.Fatal("disabled plugin UI survived restart")
	}
	request("PUT", "/api/v1/plugins/terminal", `{"enabled":true}`, http.StatusOK)
	reopen()
	if len(ctrl.Plugins().FrontendExtensions()) != 1 {
		t.Fatal("re-enabled plugin UI did not return after restart")
	}
	request("DELETE", "/api/v1/plugins/terminal", "", http.StatusOK)
	reopen()
	if len(ctrl.Plugins().FrontendExtensions()) != 0 {
		t.Fatal("uninstalled plugin remained active after restart")
	}
	if data, err := os.ReadFile(otherState); err != nil || string(data) != `{"retained":true}` {
		t.Fatalf("unrelated plugin state changed: %q (%v)", data, err)
	}
	if data, err := os.ReadFile(terminalData); err != nil || string(data) != "retained" {
		t.Fatalf("plugin data was unexpectedly removed on uninstall: %q (%v)", data, err)
	}
	if response := request("GET", "/api/v1/terminal", "", http.StatusOK); !bytes.Contains(response.Body.Bytes(), []byte(`"available":true`)) {
		t.Fatalf("legacy Terminal endpoint unavailable after uninstall: %s", response.Body.String())
	}
}
