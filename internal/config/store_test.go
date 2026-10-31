package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestOpenWithConfigDirStoresConfigSeparatelyFromData(t *testing.T) {
	dataDir := t.TempDir()
	configDir := filepath.Join(t.TempDir(), "config")
	store, err := OpenWithConfigDir(dataDir, configDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(configDir, "runpilot.yaml"); store.Path() != want {
		t.Fatalf("config path = %q, want %q", store.Path(), want)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "runpilot.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected config file beside data: %v", err)
	}
}

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
	if s.Snapshot().Server.SetupComplete {
		t.Fatal("fresh config should require first-run setup")
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

func TestDefaultDataDirDoesNotFallBackToRelativePath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux default only")
	}
	t.Setenv("RUNPILOT_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	oldHome := userHomeDir
	userHomeDir = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { userHomeDir = oldHome })
	if got := DefaultDataDir(); got != "" {
		t.Fatalf("unresolvable Linux default = %q, want empty", got)
	}
	if _, err := Open(""); err == nil || !strings.Contains(err.Error(), "home directory is unavailable") {
		t.Fatalf("Open error = %v, want unavailable home error", err)
	}
}

func TestConfigureAdminConcurrentClaimIsSingleUse(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if store.ConfigureAdmin(fmt.Sprintf("admin%d", i), "argon-hash") == nil {
				successes.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful setup claims=%d", successes.Load())
	}
	cfg := store.Snapshot().Server
	if cfg.AdminUsername == "" || cfg.AdminPasswordHash != "argon-hash" || !cfg.SetupComplete {
		t.Fatalf("admin setup state was not persisted: %#v", cfg)
	}
}
