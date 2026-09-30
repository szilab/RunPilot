package web

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/plugins"
)

func TestPluginRegistryLifecycleHTTP(t *testing.T) {
	packages := map[string][]byte{}
	catalog := plugins.Catalog{SchemaVersion: 1, Plugins: []plugins.CatalogEntry{{ID: "example", Name: "Example", Latest: "1.1.0"}}}
	for _, version := range []string{"1.0.0", "1.1.0"} {
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		manifest := fmt.Sprintf("apiVersion: runpilot.plugin/v1\nid: example\nname: Example\nversion: %s\nrequires:\n  frontend: '>=1.0.0 <2.0.0'\nfrontend:\n  module: web/plugin.js\n  stylesheet: web/plugin.css\n", version)
		for name, contents := range map[string]string{"plugin.yaml": manifest, "web/plugin.js": "// " + version, "web/plugin.css": "/* " + version + " */"} {
			entry, err := writer.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = entry.Write([]byte(contents)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		packages["/"+version] = archive.Bytes()
	}
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog.json" {
			_ = json.NewEncoder(w).Encode(catalog)
			return
		}
		data, ok := packages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	defer registry.Close()
	for _, version := range []string{"1.0.0", "1.1.0"} {
		hash := sha256.Sum256(packages["/"+version])
		catalog.Plugins[0].Versions = append(catalog.Plugins[0].Versions, plugins.CatalogVersion{Version: version, Requires: plugins.Requires{Frontend: ">=1.0.0 <2.0.0"}, URL: registry.URL + "/" + version, SHA256: hex.EncodeToString(hash[:])})
	}
	dataDir := t.TempDir()
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error { cfg.PluginRegistry.URL = registry.URL + "/catalog.json"; return nil }); err != nil {
		t.Fatal(err)
	}
	ctrl, err := core.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ctrl.Close() }()
	var server *Server
	resetServer := func() {
		var err error
		server, err = New(ctrl)
		if err != nil {
			t.Fatal(err)
		}
	}
	resetServer()
	request := func(method, path, body string, expected int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		if response.Code != expected {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		return response
	}
	if len(ctrl.Plugins().Statuses()) != 0 {
		t.Fatal("fresh startup unexpectedly installed System")
	}
	response := request("GET", "/api/v1/plugins/catalog", "", 200)
	if !bytes.Contains(response.Body.Bytes(), []byte(`"latestCompatible":"1.1.0"`)) {
		t.Fatal(response.Body.String())
	}
	request("POST", "/api/v1/plugins/example/install", `{"version":"1.0.0"}`, 200)
	status := ctrl.Plugins().Statuses()[0]
	if status.Enabled || status.Loaded || status.Source == nil || !status.RestartRequired {
		t.Fatalf("installed state: %+v", status)
	}
	request("PUT", "/api/v1/plugins/example", `{"enabled":true}`, 200)
	request("POST", "/api/v1/plugins/rescan", `{}`, 200)
	if len(ctrl.Plugins().FrontendExtensions()) != 0 {
		t.Fatal("enable/rescan hot activated frontend")
	}
	ctrl.Close()
	ctrl, err = core.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	resetServer()
	if len(ctrl.Plugins().FrontendExtensions()) != 1 || !ctrl.Plugins().Statuses()[0].Loaded {
		t.Fatal("restart did not activate frontend")
	}
	mountedServer, err := New(ctrl, "/mount")
	if err != nil {
		t.Fatal(err)
	}
	mountedRequest := httptest.NewRequest("GET", "/mount/api/v1/plugins/runtime", nil)
	mountedRequest.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
	mountedResponse := httptest.NewRecorder()
	mountedServer.Handler().ServeHTTP(mountedResponse, mountedRequest)
	if mountedResponse.Code != http.StatusOK {
		t.Fatalf("mounted runtime extensions: %d %s", mountedResponse.Code, mountedResponse.Body.String())
	}
	var mountedExtensions []plugins.FrontendExtension
	if err := json.Unmarshal(mountedResponse.Body.Bytes(), &mountedExtensions); err != nil {
		t.Fatal(err)
	}
	if len(mountedExtensions) != 1 || mountedExtensions[0].Module != "/mount/plugins/example/web/plugin.js" || mountedExtensions[0].Stylesheet != "/mount/plugins/example/web/plugin.css" {
		t.Fatalf("mounted plugin URL: %+v", mountedExtensions)
	}
	mountedAssetRequest := httptest.NewRequest("GET", mountedExtensions[0].Module, nil)
	mountedAssetResponse := httptest.NewRecorder()
	mountedServer.Handler().ServeHTTP(mountedAssetResponse, mountedAssetRequest)
	if mountedAssetResponse.Code != http.StatusOK {
		t.Fatalf("mounted plugin asset: %d %s", mountedAssetResponse.Code, mountedAssetResponse.Body.String())
	}
	mountedStyleRequest := httptest.NewRequest("GET", mountedExtensions[0].Stylesheet, nil)
	mountedStyleResponse := httptest.NewRecorder()
	mountedServer.Handler().ServeHTTP(mountedStyleResponse, mountedStyleRequest)
	if mountedStyleResponse.Code != http.StatusOK {
		t.Fatalf("mounted plugin stylesheet: %d %s", mountedStyleResponse.Code, mountedStyleResponse.Body.String())
	}
	request("POST", "/api/v1/plugins/example/install", `{"version":"1.1.0"}`, 200)
	status = ctrl.Plugins().Statuses()[0]
	if status.Manifest.Version != "1.1.0" || status.LoadedVersion != "1.0.0" {
		t.Fatalf("update state: %+v", status)
	}
	response = request("GET", "/plugins/example/web/plugin.js", "", 200)
	if response.Body.String() != "// 1.0.0" {
		t.Fatal("update replaced active frontend")
	}
	request("POST", "/api/v1/plugins/example/install", `{"version":"1.0.0"}`, 409)
	storage := filepath.Join(dataDir, "plugin-data", "example", "value")
	if err := os.MkdirAll(filepath.Dir(storage), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storage, []byte("retained"), 0644); err != nil {
		t.Fatal(err)
	}
	request("DELETE", "/api/v1/plugins/example", "", 200)
	request("GET", "/plugins/example/web/plugin.js", "", 200)
	if len(ctrl.Plugins().Statuses()) != 0 || !ctrl.Plugins().RestartRequired() {
		t.Fatal("uninstall did not update discovery")
	}
	ctrl.Close()
	ctrl, err = core.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	resetServer()
	if len(ctrl.Plugins().FrontendExtensions()) != 0 {
		t.Fatal("uninstall survived restart")
	}
	if data, err := os.ReadFile(storage); err != nil || string(data) != "retained" {
		t.Fatal("mutable data lost", err)
	}
	request("POST", "/api/v1/plugins/example/install", `{"version":"1.1.0"}`, 200)
	if ctrl.Plugins().Statuses()[0].Enabled {
		t.Fatal("reinstall enabled without user action")
	}
}
