package core

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func sourcePluginVersion(t *testing.T, source string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(source, "plugin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil || manifest.Version == "" {
		t.Fatalf("source plugin manifest version: %v", err)
	}
	return manifest.Version
}
