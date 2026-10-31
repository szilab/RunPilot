package history

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDoesNotCreateLegacyHistoryLayout(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(filepath.Join(dataDir, "legacy")); !os.IsNotExist(err) {
		t.Fatalf("legacy directory exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "legacy", "history.db")); !os.IsNotExist(err) {
		t.Fatalf("legacy database exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "internal", "runs")); !os.IsNotExist(err) {
		t.Fatalf("obsolete Backup log directory created: %v", err)
	}
}
