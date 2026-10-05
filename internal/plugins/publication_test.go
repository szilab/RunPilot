package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationCatalog(t *testing.T) {
	c := catalogFixture()
	records := []PublicationRecord{}
	for _, v := range c.Plugins[0].Versions {
		records = append(records, PublicationRecord{SchemaVersion: 1, Plugin: CatalogEntry{ID: "test", Name: "Test", Latest: v.Version, Versions: []CatalogVersion{v}}})
	}
	first, err := GenerateCatalog(records)
	if err != nil {
		t.Fatal(err)
	}
	records[0], records[2] = records[2], records[0]
	second, err := GenerateCatalog(records)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := CatalogJSON(first)
	b, _ := CatalogJSON(second)
	if !bytes.Equal(a, b) {
		t.Fatal("non-deterministic catalog")
	}
	if first.Plugins[0].Versions[1].Version != "1.10.0" {
		t.Fatalf("incorrect order: %s", a)
	}
	if _, err := GenerateCatalog(append(records, records[0])); err == nil {
		t.Fatal("duplicate publication accepted")
	}
	if _, err := GenerateCatalog([]PublicationRecord{{SchemaVersion: 2}}); err == nil {
		t.Fatal("bad schema accepted")
	}
}
func TestPublicationRecordMatchesArtifact(t *testing.T) {
	path := writePackage(t, map[string]string{"plugin.yaml": validManifest, "backend/plugin.wasm": "fixture"})
	policy := PublicationPolicy{Plugins: []PublicationRule{{ID: "remote.xpra", Source: "plugins/remote-xpra", Publish: true}, {ID: "system", Source: "plugins/system", Publish: false}}}
	record, err := RecordFromPackage(path, "szilab/RunPilot", "plugin-remote.xpra-v1.0.0", policy)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if record.Plugin.Versions[0].SHA256 != hex.EncodeToString(hash[:]) || record.Plugin.Latest != "1.0.0" {
		t.Fatalf("%+v", record)
	}
	if _, err := RecordFromPackage(path, "szilab/RunPilot", "plugin-remote.xpra-v2.0.0", policy); err == nil {
		t.Fatal("tag mismatch accepted")
	}
	policy.Plugins[0].Publish = false
	catalog, err := GeneratePublicCatalog([]PublicationRecord{record}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Plugins) != 0 {
		t.Fatal("nonpublishable record leaked into catalog")
	}
	if _, err := RecordFromPackage(path, "szilab/RunPilot", "plugin-remote.xpra-v1.0.0", policy); err == nil {
		t.Fatal("non-public plugin published")
	}
	if _, err := policy.Rule("system"); err == nil {
		t.Fatal("System is publishable")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "plugins", "publication.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Rule("system"); err == nil {
		t.Fatal("repository policy publishes System")
	}
	if _, err := policy.Rule("tasks"); err != nil {
		t.Fatalf("Tasks plugin is not publishable: %v", err)
	}
}
func TestManagerSemverAndRetiredAssets(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "plugins")
	for _, version := range []string{"1.9.0", "1.10.0"} {
		manifest := Manifest{APIVersion: PluginAPIVersion, ID: "test", Name: "Test", Version: version, Requires: Requires{Frontend: ">=1.0.0 <2.0.0"}, Frontend: &FrontendManifest{Module: "web/plugin.js"}}
		path := filepath.Join(root, "test", version)
		if err := os.MkdirAll(filepath.Join(path, "web"), 0755); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(manifest) // JSON is valid YAML.
		if err := os.WriteFile(filepath.Join(path, "plugin.yaml"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "web/plugin.js"), []byte(version), 0644); err != nil {
			t.Fatal(err)
		}
	}
	enabled := true
	manager := New(dataDir, func(string) (bool, bool) { return enabled, true })
	if errs := manager.Reload(); len(errs) > 0 {
		t.Fatal(errs)
	}
	if got, _ := manager.Manifest("test"); got.Version != "1.10.0" {
		t.Fatalf("lexical ordering: %s", got.Version)
	}
	manager.FreezeActivation()
	enabled = false
	if len(manager.FrontendExtensions()) != 1 {
		t.Fatal("disable hot unloaded frontend")
	}
	storage := filepath.Join(dataDir, "plugin-data", "test")
	if err := os.MkdirAll(storage, 0755); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall("test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Manifest("test"); ok {
		t.Fatal("uninstalled package still discovered")
	}
	asset, err := manager.AssetPath("test", "web/plugin.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.ReadFile(asset); err != nil {
		t.Fatal("active asset lost before restart", err)
	}
	next := New(dataDir)
	if errs := next.Reload(); len(errs) > 0 {
		t.Fatal(errs)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("retired packages not cleaned on restart")
	}
	if _, err := os.Stat(storage); err != nil {
		t.Fatal("plugin data deleted")
	}
}
