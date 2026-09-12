package config

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestDefaultDataDirHonorsEnvironmentAndLinuxDefault(t *testing.T) {
	t.Setenv("RUNPILOT_DATA_DIR", "/custom/runpilot")
	if got := DefaultDataDir(); got != "/custom/runpilot" {
		t.Fatalf("environment data directory = %q", got)
	}
	t.Setenv("RUNPILOT_DATA_DIR", "")
	if runtime.GOOS == "linux" && DefaultDataDir() != "/var/lib/runpilot" {
		t.Fatalf("Linux default = %q", DefaultDataDir())
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

func TestDefaultStorageIsUnrestrictedLocalFilesystem(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	providers := s.Snapshot().Storage
	if len(providers) != 1 {
		t.Fatalf("default storage providers = %#v, want one", providers)
	}
	provider := providers[0]
	if provider.ID != "storage-local" || provider.Name != "Local filesystem" || provider.Type != model.StorageLocal || provider.Local == nil || provider.Local.Scope != model.LocalStorageScopeHost || provider.Local.Root != "" {
		t.Fatalf("default storage provider = %#v", provider)
	}
	if err := s.Update(func(c *model.Config) error {
		c.Storage = []model.StorageDefinition{{ID: "one", Name: "One", Type: model.StorageLocal, Local: &model.LocalStorageSpec{Scope: model.LocalStorageScopeHost}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(c *model.Config) error {
		c.Storage = []model.StorageDefinition{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.Snapshot().Storage); got != 0 {
		t.Fatalf("removed storage definition was recreated: %#v", reopened.Snapshot().Storage)
	}
}

func TestLegacyConfigWithoutStorageGetsDefaultProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runpilot.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nserver:\n  token: existing-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	providers := s.Snapshot().Storage
	if len(providers) != 1 || providers[0].ID != "storage-local" || providers[0].Local == nil || providers[0].Local.Scope != model.LocalStorageScopeHost {
		t.Fatalf("migrated storage providers = %#v", providers)
	}
}
