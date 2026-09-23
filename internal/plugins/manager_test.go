package plugins

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManifestValidate(t *testing.T) {
	m := Manifest{
		APIVersion:      "runpilot/v1",
		ID:              "files.sftpgo",
		Name:            "SFTPGo",
		Version:         "1.0.0",
		ProtocolVersion: ProtocolVersion,
		Capabilities:    []string{"managed-service", "file-browser"},
		Executables:     map[string]string{"default": "plugin"},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestManifestRejectsUnsupportedProtocol(t *testing.T) {
	m := Manifest{
		APIVersion:      "runpilot/v1",
		ID:              "test",
		Name:            "Test",
		Version:         "1",
		ProtocolVersion: ProtocolVersion + 1,
		Capabilities:    []string{"test"},
		Executables:     map[string]string{"default": "plugin"},
	}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("Validate() error = %v, want protocol error", err)
	}
}

func TestResolveExecutableRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveExecutable(dir, filepath.Join("..", "outside")); err == nil {
		t.Fatal("resolveExecutable() accepted path traversal")
	}
}

func TestManagerReload(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "plugins", "example")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := "plugin"
	if runtime.GOOS == "windows" {
		executable = "plugin.exe"
	}
	manifest := "apiVersion: runpilot/v1\n" +
		"id: example\n" +
		"name: Example\n" +
		"version: 1.0.0\n" +
		"protocolVersion: 1\n" +
		"enabled: false\n" +
		"capabilities:\n  - test\n" +
		"executables:\n  default: " + executable + "\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := New(dataDir)
	if errs := manager.Reload(); len(errs) != 0 {
		t.Fatalf("Reload() errors = %v", errs)
	}
	statuses := manager.Statuses()
	if len(statuses) != 1 || statuses[0].Manifest.ID != "example" {
		t.Fatalf("Statuses() = %#v", statuses)
	}
}
