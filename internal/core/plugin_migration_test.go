package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
)

func TestOpenCleansObsoleteSystemFixtureAndPreservesPluginData(t *testing.T) {
	dataDir := t.TempDir()
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if err := store.Update(func(cfg *model.Config) error {
		cfg.Plugins = map[string]model.PluginSettings{"system": {Enabled: &enabled}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	legacy := filepath.Join(dataDir, "plugins", "system", "1.0.0")
	if err := os.MkdirAll(filepath.Join(legacy, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "plugin.yaml"), []byte("apiVersion: runpilot.plugin/v1\nid: system\nname: System\ndescription: Host status and resource usage\nversion: 1.0.0\nrequires:\n  runpilotApi: 1\nplatforms: [linux, windows]\nbackend:\n  module: backend/plugin.wasm\nfrontend:\n  module: web/plugin.js\n  stylesheet: web/plugin.css\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dataDir, "plugin-data", "system", "settings.json")
	if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, []byte(`{"kept":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctrl, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	if got := ctrl.Plugins().DiscoveryErrors(); len(got) != 0 {
		t.Fatalf("discovery errors after migration = %v", got)
	}
	if _, ok := ctrl.Plugins().Manifest("system"); ok {
		t.Fatal("obsolete System fixture remains in plugin discovery")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("obsolete System package remains: %v", err)
	}
	if got, err := os.ReadFile(data); err != nil || string(got) != `{"kept":true}` {
		t.Fatalf("System plugin data changed: %s, %v", got, err)
	}
	if setting := ctrl.config.Snapshot().Plugins["system"]; setting.Enabled == nil || *setting.Enabled {
		t.Fatalf("obsolete System fixture remained enabled: %#v", setting)
	}
}
