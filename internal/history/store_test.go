package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

func TestStoreMigratesLegacyHistoryIdempotently(t *testing.T) {
	dataDir := t.TempDir()
	started := time.Date(2026, 9, 18, 1, 2, 3, 0, time.UTC)
	legacy := `{"id":"run-old","kind":"command","targetId":"job-one","targetName":"One","startedAt":"` + started.Format(time.RFC3339Nano) + `","finishedAt":"` + started.Add(time.Minute).Format(time.RFC3339Nano) + `","exitCode":0,"success":true,"logPath":"/old/path.log"}` + "\n" +
		"not json\n" +
		`{"id":"run-old","kind":"command","targetId":"job-one","targetName":"One","startedAt":"` + started.Format(time.RFC3339Nano) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dataDir, "history.jsonl"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.Recent("job-one", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != "run-old" {
		t.Fatalf("migrated runs = %#v", runs)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err = store.Recent("job-one", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("re-migrated runs = %d", len(runs))
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
