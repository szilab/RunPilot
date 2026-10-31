package core

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
)

// fakeRestic records its arguments and answers like restic. It requires the
// password from the environment file, so a missing variable fails the run.
const fakeRestic = `#!/bin/sh
[ "$RESTIC_PASSWORD" = "s3cret value" ] || { echo "Fatal: no password" >&2; exit 9; }
echo "$@" >> "$(dirname "$0")/calls.log"
case " $* " in
  *" snapshots "*) echo '[{"id":"0123456789abcdef","short_id":"01234567","time":"2026-10-31T03:00:00Z","hostname":"host-1","paths":["/data"]}]' ;;
  *" backup "*) echo "snapshot 01234567 saved" ;;
esac
`

// TestBackupPluginWASMRunsJobs drives the compiled Backup plugin through
// the real scheduler, process, history and workspace capabilities.
func TestBackupPluginWASMRunsJobs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as restic")
	}
	dataDir := t.TempDir()
	copySourcePlugin(t, dataDir, "backup", "backup")
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error {
		enabled := true
		cfg.Plugins = map[string]model.PluginSettings{"backup": {Enabled: &enabled}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	controller, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	call := func(method string, params any) (map[string]any, string) {
		t.Helper()
		raw, _ := json.Marshal(params)
		response, err := controller.PluginCall(context.Background(), "backup", method, raw)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		var decoded map[string]any
		_ = json.Unmarshal(response, &decoded)
		if failure, ok := decoded["error"].(map[string]any); ok {
			return nil, failure["message"].(string)
		}
		return decoded, ""
	}
	must := func(method string, params any) map[string]any {
		t.Helper()
		value, message := call(method, params)
		if message != "" {
			t.Fatalf("%s failed: %s", method, message)
		}
		return value
	}
	status := func(id string) map[string]any {
		for _, item := range must("backup.list", nil)["jobs"].([]any) {
			view := item.(map[string]any)
			if view["job"].(map[string]any)["id"] == id {
				return view["status"].(map[string]any)
			}
		}
		t.Fatalf("job %s not listed", id)
		return nil
	}
	finished := func(id string) map[string]any {
		t.Helper()
		var current map[string]any
		waitUntil(t, 20*time.Second, id+" to finish", func() bool {
			current = status(id)
			return current["state"] != "running" && current["lastRunId"] != nil
		})
		return current
	}

	tools := t.TempDir()
	restic := filepath.Join(tools, "restic")
	if err := os.WriteFile(restic, []byte(fakeRestic), 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(tools, "restic.env")
	if err := os.WriteFile(envFile, []byte("export RESTIC_PASSWORD='s3cret value'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	created := must("backup.create", map[string]any{"job": map[string]any{"name": "Data", "engine": "restic", "enabled": true,
		"schedule": map[string]any{"type": "daily", "timeOfDay": "03:00"},
		"restic":   map[string]any{"executable": restic, "repository": filepath.Join(tools, "repo"), "environmentFile": envFile, "sources": []string{source}, "retention": map[string]any{"keepLast": 3}}}})
	id := created["job"].(map[string]any)["id"].(string)
	next, err := time.Parse(time.RFC3339Nano, created["status"].(map[string]any)["nextRunAt"].(string))
	if err != nil || next.Before(time.Now()) || next.After(time.Now().Add(25*time.Hour)) {
		t.Fatalf("next run = %v (%v)", created["status"], err)
	}
	must("backup.run", map[string]any{"id": id})
	if state := finished(id); state["lastSuccess"] != true {
		t.Fatalf("restic run = %v", state)
	}
	calls, _ := os.ReadFile(filepath.Join(tools, "calls.log"))
	if lines := strings.Split(strings.TrimSpace(string(calls)), "\n"); len(lines) != 2 || !strings.HasSuffix(lines[0], "backup "+source) || !strings.Contains(lines[1], "forget --keep-last 3") {
		t.Fatalf("restic calls = %q", calls)
	}
	runs := must("backup.history", map[string]any{"id": id})["runs"].([]any)
	output := must("backup.output", map[string]any{"runId": runs[0].(map[string]any)["id"]})["output"].(string)
	if !strings.Contains(output, "snapshot 01234567 saved") || !strings.Contains(output, "Step 2/2: retention") || strings.Contains(output, "s3cret") {
		t.Fatalf("output = %q", output)
	}
	snapshots := must("backup.snapshots", map[string]any{"id": id})["snapshots"].([]any)
	if len(snapshots) != 1 || snapshots[0].(map[string]any)["shortId"] != "01234567" {
		t.Fatalf("snapshots = %v", snapshots)
	}
	target := filepath.Join(t.TempDir(), "restored")
	must("backup.restore", map[string]any{"id": id, "snapshot": "0123456789abcdef", "target": target})
	if state := finished(id); state["lastKind"] != "restore" || state["lastSuccess"] != true {
		t.Fatalf("restore = %v", state)
	}
	if err := os.WriteFile(filepath.Join(source, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, message := call("backup.restore", map[string]any{"id": id, "snapshot": "latest", "target": source}); message == "" {
		t.Fatal("a restore into the backed-up folder was accepted")
	}
	if folders := must("backup.browse", map[string]any{"path": strings.TrimPrefix(filepath.ToSlash(tools), "/")})["folders"].([]any); len(folders) != 0 {
		t.Fatalf("browse = %v", folders)
	}

	// A mirror job with the real rsync, when it is installed.
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Log("rsync is not installed; skipping the mirror run")
		return
	}
	destination := filepath.Join(t.TempDir(), "copy")
	if err := os.WriteFile(filepath.Join(source, "skip.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mirror := must("backup.create", map[string]any{"job": map[string]any{"name": "Copy", "engine": "rsync", "mirror": map[string]any{"source": source, "destination": destination, "excludes": []string{"*.tmp"}}}})
	mirrorID := mirror["job"].(map[string]any)["id"].(string)
	must("backup.run", map[string]any{"id": mirrorID})
	if state := finished(mirrorID); state["lastSuccess"] != true {
		t.Fatalf("rsync run = %v", state)
	}
	if _, err := os.Stat(filepath.Join(destination, "keep.txt")); err != nil {
		t.Fatalf("rsync did not copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "skip.tmp")); err == nil {
		t.Fatal("rsync copied an excluded file")
	}
}
