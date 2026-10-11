package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
