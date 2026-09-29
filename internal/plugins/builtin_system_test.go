package plugins

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReferenceSystemWASMMatchesSourceArtifact(t *testing.T) {
	root := filepath.Join("..", "..", "plugins", "system")
	sourceManifest, err := loadManifest(filepath.Join(root, "plugin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join(root, "backend", "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wasm, referenceSystemWASM) {
		t.Fatal("embedded System WASM differs from source-derived artifact")
	}
	for _, artifact := range []struct {
		source   string
		embedded []byte
	}{
		{filepath.Join(root, "plugin.yaml"), referenceSystemManifest},
		{filepath.Join(root, "web", "plugin.js"), referenceSystemJavaScript},
		{filepath.Join(root, "web", "plugin.css"), referenceSystemStylesheet},
	} {
		data, err := os.ReadFile(artifact.source)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, artifact.embedded) {
			t.Fatalf("embedded System asset differs from canonical source %s", artifact.source)
		}
	}

	installRoot := t.TempDir()
	if err := EnsureReferenceSystem(installRoot); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadManifest(filepath.Join(installRoot, sourceManifest.ID, sourceManifest.Version, "plugin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Requires.RunPilotAPI != PluginABIVersion2 {
		t.Fatalf("fresh-install System manifest ABI = %d", manifest.Requires.RunPilotAPI)
	}
}

func TestEnsureReferenceSystemUpgradesExistingABIV1Package(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "plugins")
	sourceManifest, err := loadManifest(filepath.Join("..", "..", "plugins", "system", "plugin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, sourceManifest.ID, "1.0.0")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "plugin.yaml"), []byte("apiVersion: runpilot.plugin/v1\nid: system\nname: System\nversion: 1.0.0\nrequires:\n  backend: '>=1.0.0 <2.0.0'\n  frontend: '>=1.0.0 <2.0.0'\n  runpilotApi: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureReferenceSystem(root); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadManifest(filepath.Join(root, sourceManifest.ID, sourceManifest.Version, "plugin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Requires.RunPilotAPI != PluginABIVersion2 {
		t.Fatalf("upgraded System ABI = %d", manifest.Requires.RunPilotAPI)
	}
	if _, err := os.Stat(filepath.Join(legacy, "plugin.yaml")); err != nil {
		t.Fatalf("legacy package was removed: %v", err)
	}
	manager := New(dataDir)
	if errs := manager.Reload(); len(errs) != 0 {
		t.Fatalf("discover installed System versions: %v", errs)
	}
	selected, ok := manager.Manifest(sourceManifest.ID)
	if !ok || selected.Version != sourceManifest.Version || selected.Requires.RunPilotAPI != PluginABIVersion2 {
		t.Fatalf("manager selected System package %#v (found=%v)", selected, ok)
	}
}
