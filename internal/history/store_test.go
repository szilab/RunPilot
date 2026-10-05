package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

func TestStoreUsesSeparateLegacyDirectory(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "history.db"), []byte("old database"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.Recent("", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("old database was imported: %#v", runs)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "legacy", "history.db")); err != nil {
		t.Fatal(err)
	}
	if got := store.RunLogPath("run-one"); got != filepath.Join(dataDir, "legacy", "runs", "run-one.log") {
		t.Fatalf("legacy log path = %s", got)
	}
}

func TestStoreRecentFiltersAndCapsLimit(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i, target := range []string{"job-one", "job-two", "job-one"} {
		success := i != 1
		if err := store.Append(model.RunRecord{ID: "run-" + string(rune('a'+i)), Kind: "command", TargetID: target, TargetName: target, StartedAt: time.Now().Add(time.Duration(i) * time.Second), Success: &success}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := store.Recent("job-one", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || !strings.HasPrefix(runs[0].TargetID, "job-") {
		t.Fatalf("filtered runs = %#v", runs)
	}
}
