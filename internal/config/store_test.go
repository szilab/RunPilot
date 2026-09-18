package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	token := s.Snapshot().Server.Token
	if token == "" {
		t.Fatal("missing token")
	}
	if !s.TokenCreated() {
		t.Fatal("new configuration did not report a generated token")
	}
	if _, err := os.Stat(filepath.Join(dir, "runpilot.yaml")); err != nil {
		t.Fatalf("YAML configuration was not created: %v", err)
	}
	if err := s.Update(func(c *model.Config) error {
		c.Server.Bind = "127.0.0.1:9999"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Snapshot().Server.Bind; got != "127.0.0.1:9999" {
		t.Fatalf("bind = %q", got)
	}
	if s2.TokenCreated() {
		t.Fatal("existing configuration unexpectedly generated a token")
	}
}

func TestDefaultDataDirHonorsEnvironmentAndLinuxDefaults(t *testing.T) {
	t.Setenv("RUNPILOT_DATA_DIR", "/custom/runpilot")
	if got := DefaultDataDir(); got != "/custom/runpilot" {
		t.Fatalf("environment data directory = %q", got)
	}
	t.Setenv("RUNPILOT_DATA_DIR", "")
	if runtime.GOOS == "linux" {
		t.Setenv("XDG_DATA_HOME", "/custom/data")
		if got := DefaultDataDir(); got != filepath.Join("/custom/data", "runpilot") {
			t.Fatalf("XDG data directory = %q", got)
		}
		t.Setenv("XDG_DATA_HOME", "")
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		if got := DefaultDataDir(); got != filepath.Join(home, ".local", "share", "runpilot") {
			t.Fatalf("Linux default = %q", got)
		}
	}
}

func TestSoftwareProviderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	providers := s.Snapshot().Software.Providers
	if runtime.GOOS != "windows" {
		if len(providers) != 0 {
			t.Fatalf("default software = %#v, want no Linux providers", s.Snapshot().Software)
		}
		return
	}
	if len(providers) != 1 || providers[0].ID != "scoop" {
		t.Fatalf("default software = %#v", s.Snapshot().Software)
	}
	custom := filepath.Join(dir, "runpilot-software")
	if err := s.Update(func(c *model.Config) error { c.Software.Providers[0].Scoop.Root = custom; return nil }); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot().Software.Providers[0].Scoop.Root; got != custom {
		t.Fatalf("root = %q", got)
	}
}

func TestStorageIsNotPersisted(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if containsStorageSection(string(b)) {
		t.Fatalf("fresh config persists storage: %s", b)
	}
}

func TestLegacyStorageConfigurationIsRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runpilot.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nserver:\n  token: existing-token\nstorage:\n  - id: storage-local\n    name: Local filesystem\n    type: local\n    local:\n      scope: host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Server.Token != "existing-token" {
		t.Fatal("configuration data changed during migration")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if containsStorageSection(string(b)) {
		t.Fatalf("legacy storage was written back: %s", b)
	}
}

func TestLegacyRemoteForwardDBusMigratesToHostSessionMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runpilot.yaml")
	yaml := `version: 1
server: { bind: 127.0.0.1:9070, token: existing-token }
remoteTargets:
  - id: legacy
    name: Legacy application
    provider: xpra
    type: application
    enabled: true
    forwardDbus: true
    command: { path: xterm }
  - id: default
    name: Default application
    provider: xpra
    type: application
    enabled: true
    command: { path: xterm }
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	targets := s.Snapshot().RemoteTargets
	if targets[0].DBusMode != model.RemoteDBusHost || targets[1].DBusMode != model.RemoteDBusIsolated {
		t.Fatalf("migrated targets = %#v", targets)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "forwardDbus") {
		t.Fatalf("legacy D-Bus field was retained: %s", b)
	}
}

func containsStorageSection(value string) bool {
	for _, line := range strings.Split(value, "\n") {
		if line == "storage:" {
			return true
		}
	}
	return false
}
