package history

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestExecutionLifecycleAndOwnerIsolation(t *testing.T) {
	store := openTestStore(t)
	e, err := store.BeginExecution("tasks", "process", "task-1", "Task one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetExecution("other", e.ID); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("other owner read = %v", err)
	}
	if _, err := store.FinishExecution("other", e.ID, nil, nil, ""); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("other owner finish = %v", err)
	}
	if _, _, _, err := store.ReadExecutionOutput("other", e.ID, 10); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("other owner output = %v", err)
	}
	code, ok := 3, false
	finished, err := store.FinishExecution("tasks", e.ID, &code, &ok, "boom")
	if err != nil || finished.FinishedAt == nil || *finished.ExitCode != 3 || *finished.Success {
		t.Fatalf("finish = %#v, %v", finished, err)
	}
	okTrue := true
	again, err := store.FinishExecution("tasks", e.ID, nil, &okTrue, "later")
	if err != nil || again.Message != "boom" || *again.Success {
		t.Fatalf("second finish overwrote result: %#v, %v", again, err)
	}
	list, err := store.ListExecutions("tasks", "task-1", 10)
	if err != nil || len(list) != 1 || list[0].ID != e.ID {
		t.Fatalf("list = %#v, %v", list, err)
	}
	if other, _ := store.ListExecutions("other", "", 10); len(other) != 0 {
		t.Fatalf("other owner list = %#v", other)
	}
}

func TestExecutionsAreInvisibleToLegacyHistory(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.BeginExecution("tasks", "process", "task-1", "One"); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(model.RunRecord{ID: "run-legacy", Kind: "process", TargetID: "task-1", TargetName: "Legacy", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Recent("", 50)
	if err != nil || len(runs) != 1 || runs[0].ID != "run-legacy" {
		t.Fatalf("legacy runs = %#v, %v", runs, err)
	}
}

func TestExecutionInputValidation(t *testing.T) {
	store := openTestStore(t)
	for _, tc := range [][3]string{{"", "process", "s"}, {"../x", "process", "s"}, {"a/b", "process", "s"}, {"tasks", "bad kind", "s"}, {"tasks", "process", ""}, {"tasks", "process", "a/../b"}, {"tasks", "process", strings.Repeat("x", 200)}, {"..", "process", "s"}} {
		if _, err := store.BeginExecution(tc[0], tc[1], tc[2], "l"); !errors.Is(err, ErrInvalidExecution) {
			t.Errorf("BeginExecution(%q) = %v", tc, err)
		}
	}
	if _, err := store.GetExecution("tasks", "../../etc/passwd"); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("path id = %v", err)
	}
	e, _ := store.BeginExecution("tasks", "process", "s", strings.Repeat("é", 400))
	if len(e.Label) > maxLabelLength || !strings.HasSuffix(e.Label, "é") {
		t.Fatalf("label not truncated on a rune boundary: %d", len(e.Label))
	}
	if err := store.AppendExecutionOutput("tasks", e.ID, strings.Repeat("x", MaxExecutionAppend+1)); !errors.Is(err, ErrInvalidExecution) {
		t.Fatalf("oversize append = %v", err)
	}
}

func TestExecutionLogCaptureCapAndTail(t *testing.T) {
	store := openTestStore(t)
	e, _ := store.BeginExecution("tasks", "process", "s", "l")
	var mu sync.Mutex
	var last int64
	log, err := store.OpenExecutionLog("tasks", e.ID, func(size int64) { mu.Lock(); last = size; mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = log.Write([]byte("line\n"))
			}
		}()
	}
	wg.Wait()
	if log.Size() != 8*50*5 {
		t.Fatalf("size = %d", log.Size())
	}
	mu.Lock()
	if last == 0 {
		t.Fatal("write callback not invoked")
	}
	mu.Unlock()
	chunk := make([]byte, 1<<20)
	for i := 0; i < 20; i++ {
		if n, err := log.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("write past cap must be swallowed: %d %v", n, err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(store.dataDir, "runs", "plugins", "tasks", e.ID+".log"))
	if err != nil || info.Size() > MaxExecutionLogBytes+int64(len(truncationMarker)) {
		t.Fatalf("log size = %v, %v", info, err)
	}
	text, size, truncated, err := store.ReadExecutionOutput("tasks", e.ID, 100)
	if err != nil || !truncated || len(text) > 100 || size != info.Size() || !strings.Contains(text, "truncated") {
		t.Fatalf("tail = %q size=%d truncated=%v err=%v", text, size, truncated, err)
	}
	if err := store.AppendExecutionOutput("tasks", e.ID, "ignored"); err != nil {
		t.Fatalf("append at cap must be a silent no-op: %v", err)
	}
}

func TestExecutionLogRefusesFinishedAndAppendAndTail(t *testing.T) {
	store := openTestStore(t)
	e, _ := store.BeginExecution("tasks", "process", "s", "l")
	if err := store.AppendExecutionOutput("tasks", e.ID, "héllo wörld\n"); err != nil {
		t.Fatal(err)
	}
	text, _, truncated, err := store.ReadExecutionOutput("tasks", e.ID, 5)
	if err != nil || !truncated || text == "" {
		t.Fatalf("tail = %q %v %v", text, truncated, err)
	}
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("tail should end with newline: %q", text)
	}
	if _, err := store.FinishExecution("tasks", e.ID, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenExecutionLog("tasks", e.ID, nil); !errors.Is(err, ErrInvalidExecution) {
		t.Fatalf("open log of finished execution = %v", err)
	}
}

func TestExecutionRetentionPrunesFinishedOnly(t *testing.T) {
	store := openTestStore(t)
	running, _ := store.BeginExecution("tasks", "process", "s", "running")
	var first Execution
	for i := 0; i < ExecutionRetention+3; i++ {
		e, err := store.BeginExecution("tasks", "process", "s", "l")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = e
		}
		if _, err := store.FinishExecution("tasks", e.ID, nil, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.GetExecution("tasks", first.ID); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("oldest not pruned: %v", err)
	}
	if _, err := os.Stat(store.executionLogPath("tasks", first.ID)); !os.IsNotExist(err) {
		t.Fatalf("pruned log remains: %v", err)
	}
	if _, err := store.GetExecution("tasks", running.ID); err != nil {
		t.Fatalf("unfinished execution pruned: %v", err)
	}
}

func TestAbandonExecutionsOnReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := store.BeginExecution("tasks", "process", "s", "l")
	_ = store.Close()
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetExecution("tasks", e.ID)
	if err != nil || got.FinishedAt == nil || got.Success == nil || *got.Success || !strings.Contains(got.Message, "interrupted") {
		t.Fatalf("abandoned = %#v, %v", got, err)
	}
}
