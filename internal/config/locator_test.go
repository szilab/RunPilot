package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultConfigDirUsesPlatformConfigurationHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux configuration-directory conventions")
	}
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	if got := DefaultConfigDir(); got != "/custom/config" {
		t.Fatalf("XDG config directory=%q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	oldHome := userHomeDir
	userHomeDir = func() (string, error) { return "/home/runpilot-user", nil }
	t.Cleanup(func() { userHomeDir = oldHome })
	if got := DefaultConfigDir(); got != filepath.Join("/home/runpilot-user", ".config") {
		t.Fatalf("default config directory=%q", got)
	}
}

func TestDataRootPrecedenceAndPersistence(t *testing.T) {
	base := t.TempDir()
	locator := filepath.Join(base, "deployment", "data-root")
	t.Setenv("RUNPILOT_DATA_ROOT_FILE", locator)
	t.Setenv("RUNPILOT_DATA_DIR", "")
	persisted := filepath.Join(base, "persistent")
	if err := WriteDataRoot(persisted); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveDataDir("")
	if err != nil || got != persisted {
		t.Fatalf("persisted root=%q err=%v", got, err)
	}
	env := filepath.Join(base, "environment")
	t.Setenv("RUNPILOT_DATA_DIR", env)
	got, err = ResolveDataDir("")
	if err != nil || got != env {
		t.Fatalf("environment root=%q err=%v", got, err)
	}
	explicit := filepath.Join(base, "explicit")
	got, err = ResolveDataDir(explicit)
	if err != nil || got != explicit {
		t.Fatalf("explicit root=%q err=%v", got, err)
	}
}

func TestApplyPendingDataRootCopiesAndVerifiesBeforeSwitch(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RUNPILOT_DATA_ROOT_FILE", filepath.Join(base, "bootstrap", "data-root"))
	t.Setenv("RUNPILOT_DATA_DIR", "")
	source := filepath.Join(base, "old")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "runpilot.yaml"), []byte("server:\n  token: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteDataRoot(source); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "new")
	if err := QueueDataRootMigration(source, destination); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPendingDataRoot(); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveDataDir("")
	if err != nil || got != destination {
		t.Fatalf("active root=%q err=%v", got, err)
	}
	if err := VerifyDataRoot(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pendingDataRootPath()); !os.IsNotExist(err) {
		t.Fatalf("pending migration remains: %v", err)
	}
	if _, err := os.Stat(dataRootStatusPath()); err != nil {
		t.Fatal(err)
	}
}

func TestDataRootRejectsSymlinksAndOccupiedDestinations(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateDataRoot(filepath.Join(link, "data")); err == nil {
		t.Fatal("symlink path accepted")
	}
	occupied := filepath.Join(base, "occupied")
	if err := os.Mkdir(occupied, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNPILOT_DATA_ROOT_FILE", filepath.Join(base, "bootstrap", "root"))
	if err := QueueDataRootMigration(target, occupied); err == nil {
		t.Fatal("occupied destination accepted")
	}
	if _, err := os.Stat(filepath.Join(occupied, "keep")); err != nil {
		t.Fatal("unrelated destination data changed")
	}
}

func TestDataRootSwitchLeavesBothDirectoriesUntouched(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint("existing=", existing), func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("RUNPILOT_DATA_ROOT_FILE", filepath.Join(base, "bootstrap", "data-root"))
			t.Setenv("RUNPILOT_DATA_DIR", "")
			source := filepath.Join(base, "source")
			destination := filepath.Join(base, "destination")
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "source-only"), []byte("keep source"), 0o600); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(destination, "destination-only"), []byte("keep destination"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := WriteDataRoot(source); err != nil {
				t.Fatal(err)
			}
			if err := QueueDataRootSwitch(source, destination); err != nil {
				t.Fatal(err)
			}
			if got, err := ResolveDataDir(""); err != nil || got != source {
				t.Fatalf("changed before restart: %q, %v", got, err)
			}
			if err := ApplyPendingDataRoot(); err != nil {
				t.Fatal(err)
			}
			if got, err := ResolveDataDir(""); err != nil || got != destination {
				t.Fatalf("selected directory: %q, %v", got, err)
			}
			if value, err := os.ReadFile(filepath.Join(source, "source-only")); err != nil || string(value) != "keep source" {
				t.Fatalf("source changed: %q, %v", value, err)
			}
			if _, err := os.Stat(filepath.Join(destination, "source-only")); !os.IsNotExist(err) {
				t.Fatalf("source data copied: %v", err)
			}
			if existing {
				if value, err := os.ReadFile(filepath.Join(destination, "destination-only")); err != nil || string(value) != "keep destination" {
					t.Fatalf("destination changed: %q, %v", value, err)
				}
			} else if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("switch created destination: %v", err)
			}
		})
	}
}
