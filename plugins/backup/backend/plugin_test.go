package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

// fakeHost records capability calls and simulates storage, schedules,
// processes, history and a tiny file system.
type fakeHost struct {
	t         *testing.T
	os        string
	stored    json.RawMessage
	schedules map[string]map[string]any
	started   []map[string]any
	runs      map[string]map[string]any
	appended  map[string]string
	files     map[string]string // "root|path" -> content; "" content marks a folder
	run       func(args map[string]any) (map[string]any, error)
	published []map[string]any
	nextProc  int
}

func newFakeHost(t *testing.T, hostOS string) *fakeHost {
	return &fakeHost{t: t, os: hostOS, schedules: map[string]map[string]any{}, runs: map[string]map[string]any{}, appended: map[string]string{}, files: map[string]string{}}
}

func assign(result any, value any) error {
	if result == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, result)
}

func capFailure(code string) error { return &pluginapi.Error{Code: code, Message: code} }

func (h *fakeHost) call(method string, params any, result any) error {
	raw, _ := json.Marshal(params)
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	str := func(key string) string { value, _ := args[key].(string); return value }
	switch method {
	case "system.status":
		return assign(result, map[string]any{"os": h.os, "disks": []map[string]any{{"path": `D:\`}, {"path": `C:\`}}})
	case "storage.get":
		return assign(result, h.stored)
	case "storage.set":
		h.stored, _ = json.Marshal(args["value"])
		return nil
	case "scheduler.register":
		if _, exists := h.schedules[str("id")]; exists {
			return capFailure("already_exists")
		}
		h.schedules[str("id")] = args
		return nil
	case "scheduler.remove":
		delete(h.schedules, str("id"))
		return nil
	case "scheduler.validate":
		schedule, _ := args["schedule"].(map[string]any)
		if schedule["type"] == "cron" && schedule["cron"] == "bad" {
			return capFailure("invalid_argument")
		}
		return nil
	case "scheduler.list":
		list := []map[string]any{}
		for id := range h.schedules {
			list = append(list, map[string]any{"id": id, "nextRunAt": "2026-11-01T03:00:00Z"})
		}
		return assign(result, map[string]any{"schedules": list})
	case "history.begin":
		id := "run-" + itoa(len(h.runs)+1)
		h.runs[id] = map[string]any{"id": id, "kind": args["kind"], "subject": args["subject"], "startedAt": "2026-10-31T10:00:0" + itoa(len(h.runs)) + "Z"}
		return assign(result, h.runs[id])
	case "history.append":
		h.appended[str("id")] += str("text")
		return nil
	case "history.finish":
		run := h.runs[str("id")]
		run["finishedAt"] = "done"
		for _, key := range []string{"success", "exitCode", "message"} {
			if value, ok := args[key]; ok {
				run[key] = value
			}
		}
		return assign(result, run)
	case "history.get":
		return assign(result, h.runs[str("id")])
	case "history.list":
		return assign(result, map[string]any{"executions": []any{}})
	case "process.start":
		h.nextProc++
		args["processId"] = "proc-" + itoa(h.nextProc)
		h.started = append(h.started, args)
		return assign(result, map[string]any{"id": args["processId"], "pid": 100 + h.nextProc})
	case "process.terminate":
		return assign(result, map[string]any{"running": false, "exitCode": -1, "terminated": true})
	case "process.status":
		return assign(result, map[string]any{"running": true})
	case "process.run":
		if h.run != nil {
			out, err := h.run(args)
			if err != nil {
				return err
			}
			return assign(result, out)
		}
		return assign(result, map[string]any{"exitCode": 0, "stdout": "restic 0.17.3 compiled with go1.23\n"})
	case "events.publish":
		h.published = append(h.published, args)
		return nil
	case "workspace.stat", "workspace.list", "workspace.read":
		key := str("root") + "|" + str("path")
		content, exists := h.files[key]
		if !exists {
			return capFailure("not_found")
		}
		switch method {
		case "workspace.stat":
			kind := "file"
			if content == "" {
				kind = "directory"
			}
			return assign(result, map[string]any{"type": kind})
		case "workspace.read":
			return assign(result, map[string]any{"data": base64.StdEncoding.EncodeToString([]byte(content))})
		}
		entries := []map[string]any{}
		prefix := key + "/"
		if str("path") == "" {
			prefix = key
		}
		for name, value := range h.files {
			if rest, ok := strings.CutPrefix(name, prefix); ok && rest != "" && !strings.Contains(rest, "/") {
				kind := "file"
				if value == "" {
					kind = "directory"
				}
				entries = append(entries, map[string]any{"name": rest, "type": kind})
			}
		}
		return assign(result, map[string]any{"entries": entries})
	}
	h.t.Fatalf("unexpected host call %s", method)
	return nil
}

func setup(t *testing.T, hostOS string) (*plugin, *fakeHost) {
	t.Helper()
	host := newFakeHost(t, hostOS)
	original := callHost
	callHost = host.call
	t.Cleanup(func() { callHost = original })
	p := newPlugin()
	p.init()
	return p, host
}

func call(t *testing.T, p *plugin, method string, params any) map[string]any {
	t.Helper()
	result, failure := callRaw(p, method, params)
	if failure != nil {
		t.Fatalf("%s: %+v", method, failure)
	}
	return result
}

func callRaw(p *plugin, method string, params any) (map[string]any, *rpcError) {
	raw, _ := json.Marshal(params)
	result, failure := p.handle(method, raw)
	if failure != nil {
		return nil, failure
	}
	encoded, _ := json.Marshal(result)
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	return decoded, nil
}

func resticJob() map[string]any {
	return map[string]any{"name": " Photos ", "enabled": true, "engine": "restic", "schedule": map[string]any{"type": "daily", "timeOfDay": "03:00"},
		"restic": map[string]any{"repository": "/backup/repo", "passwordFile": "/etc/restic/password", "sources": []string{"/srv/photos", " "}, "excludes": []string{"*.tmp"}, "tags": []string{"photos"},
			"retention": map[string]any{"keepDaily": 7, "keepMonthly": 6, "prune": true}, "checkAfterBackup": true}}
}

func exit(p *plugin, process string, code int) {
	p.event("process.exit", json.RawMessage(`{"id":"`+process+`","exitCode":`+itoa(code)+`}`))
}

func TestResticJobRunsItsStepsInOrder(t *testing.T) {
	p, host := setup(t, "linux")
	created := call(t, p, "backup.create", map[string]any{"job": resticJob()})
	job := created["job"].(map[string]any)
	if job["id"] != "job-1" || job["name"] != "Photos" || created["status"].(map[string]any)["nextRunAt"] != "2026-11-01T03:00:00Z" {
		t.Fatalf("created = %v", created)
	}
	if sources := job["restic"].(map[string]any)["sources"].([]any); len(sources) != 1 {
		t.Fatalf("sources = %v", sources)
	}
	if host.schedules["job-1"]["callback"] != "job" {
		t.Fatalf("schedules = %v", host.schedules)
	}
	p.event("scheduler.fired", json.RawMessage(`{"id":"job-1","callback":"job"}`))
	if len(host.started) != 1 {
		t.Fatalf("started = %v", host.started)
	}
	backup := strings.Join(toStrings(host.started[0]["args"]), " ")
	if backup != "--repo /backup/repo --password-file /etc/restic/password backup --exclude *.tmp --tag photos /srv/photos" || host.started[0]["historyId"] != "run-1" || host.started[0]["interpreter"] != "direct" {
		t.Fatalf("backup step = %v", host.started[0])
	}
	// A schedule firing while the job runs is recorded as skipped.
	p.event("scheduler.fired", json.RawMessage(`{"id":"job-1","callback":"job"}`))
	if skipped := host.runs["run-2"]; skipped["message"] != "skipped: the previous run was still running" || skipped["success"] != nil || len(host.started) != 1 {
		t.Fatalf("overlap = %v", skipped)
	}
	exit(p, "proc-1", 0)
	if forget := strings.Join(toStrings(host.started[1]["args"]), " "); forget != "--repo /backup/repo --password-file /etc/restic/password forget --tag photos --keep-daily 7 --keep-monthly 6 --prune" {
		t.Fatalf("retention step = %s", forget)
	}
	exit(p, "proc-2", 0)
	exit(p, "proc-2", 0) // duplicate exits are ignored
	if check := toStrings(host.started[2]["args"]); check[len(check)-1] != "check" || len(host.started) != 3 {
		t.Fatalf("check step = %v", host.started)
	}
	exit(p, "proc-3", 0)
	if run := host.runs["run-1"]; run["success"] != true {
		t.Fatalf("run = %v", run)
	}
	if !strings.Contains(host.appended["run-1"], "Step 3/3: check") {
		t.Fatalf("log = %q", host.appended["run-1"])
	}
	listed := call(t, p, "backup.list", nil)
	status := listed["jobs"].([]any)[0].(map[string]any)["status"].(map[string]any)
	if status["state"] != "idle" || status["lastSuccess"] != true || status["lastRunId"] != "run-1" {
		t.Fatalf("status = %v", status)
	}
	if tools := listed["tools"].([]any); len(tools) != 2 || tools[0].(map[string]any)["version"] != "restic 0.17.3 compiled with go1.23" {
		t.Fatalf("tools = %v", tools)
	}
}

func toStrings(value any) []string {
	out := []string{}
	for _, item := range value.([]any) {
		out = append(out, item.(string))
	}
	return out
}

func TestFailedStepStopsTheRun(t *testing.T) {
	p, host := setup(t, "linux")
	call(t, p, "backup.create", map[string]any{"job": resticJob()})
	call(t, p, "backup.run", map[string]any{"id": "job-1"})
	if _, failure := callRaw(p, "backup.run", map[string]any{"id": "job-1"}); failure == nil || failure.Code != "already_running" {
		t.Fatalf("second run = %+v", failure)
	}
	if _, failure := callRaw(p, "backup.delete", map[string]any{"id": "job-1"}); failure == nil {
		t.Fatal("a running job was deleted")
	}
	exit(p, "proc-1", 3)
	if run := host.runs["run-1"]; run["success"] != false || run["message"] != "backup failed with exit code 3" || len(host.started) != 1 {
		t.Fatalf("run = %v", run)
	}
	completed := host.published[len(host.published)-1]
	if completed["event"] != "backup.run.completed" {
		t.Fatalf("published = %v", completed)
	}
	call(t, p, "backup.run", map[string]any{"id": "job-1"})
	call(t, p, "backup.stop", map[string]any{"id": "job-1"})
	if run := host.runs["run-2"]; run["message"] != "stopped" {
		t.Fatalf("stopped run = %v", run)
	}
	call(t, p, "backup.delete", map[string]any{"id": "job-1"})
	if _, exists := host.schedules["job-1"]; exists {
		t.Fatal("schedule kept after delete")
	}
}

func TestMirrorEnginesFollowTheHost(t *testing.T) {
	p, host := setup(t, "linux")
	job := map[string]any{"name": "Docs", "engine": "rsync", "mirror": map[string]any{"source": "/srv/docs/", "destination": "backup-host:/backup/docs", "delete": true, "excludes": []string{".cache"}}}
	call(t, p, "backup.create", map[string]any{"job": job})
	call(t, p, "backup.run", map[string]any{"id": "job-1"})
	if args := strings.Join(toStrings(host.started[0]["args"]), " "); args != "--archive --human-readable --stats --delete --exclude .cache /srv/docs/ backup-host:/backup/docs" || host.started[0]["command"] != "rsync" {
		t.Fatalf("rsync = %v", host.started[0])
	}
	exit(p, "proc-1", 24) // vanished files are fine
	if host.runs["run-1"]["success"] != true {
		t.Fatalf("run = %v", host.runs["run-1"])
	}
	if _, exists := host.schedules["job-1"]; exists {
		t.Fatal("a job without a schedule was registered")
	}
	for _, bad := range []map[string]any{
		{"name": "x", "engine": "robocopy", "mirror": map[string]any{"source": "/a", "destination": "/b"}},
		{"name": "x", "engine": "rsync", "mirror": map[string]any{"source": "/srv", "destination": "/srv/backup"}},
		{"name": "x", "engine": "rsync", "mirror": map[string]any{"source": "relative", "destination": "/b"}},
		{"name": "x", "engine": "rdiff-backup"},
		{"name": "x", "engine": "restic", "restic": map[string]any{"repository": "/r", "sources": []string{"/a"}}},
		{"name": "x", "engine": "restic", "schedule": map[string]any{"type": "cron", "cron": "bad"}, "restic": map[string]any{"repository": "/r", "passwordFile": "/p", "sources": []string{"/a"}}},
		{"name": "x", "engine": "restic", "restic": map[string]any{"repository": "/r", "passwordFile": "/p", "sources": []string{"/a"}, "retention": map[string]any{"keepWithin": "7 days"}}},
	} {
		if _, failure := callRaw(p, "backup.create", map[string]any{"job": bad}); failure == nil {
			t.Errorf("job %v was accepted", bad)
		}
	}
	update := map[string]any{"id": "job-1", "name": "Docs", "engine": "restic", "restic": map[string]any{"repository": "/r", "passwordFile": "/p", "sources": []string{"/a"}}}
	if _, failure := callRaw(p, "backup.update", map[string]any{"job": update}); failure == nil {
		t.Fatal("the engine was changed")
	}

	p, host = setup(t, "windows")
	job = map[string]any{"name": "Users", "engine": "robocopy", "mirror": map[string]any{"source": `C:\Users`, "destination": `\\server\backup`, "excludes": []string{"AppData"}, "excludeFiles": []string{"*.tmp"}}}
	call(t, p, "backup.create", map[string]any{"job": job})
	call(t, p, "backup.run", map[string]any{"id": "job-1"})
	if args := strings.Join(toStrings(host.started[0]["args"]), " "); args != `C:\Users \\server\backup /E /COPY:DAT /DCOPY:DAT /XJ /R:0 /W:5 /NP /XD AppData /XF *.tmp` || host.started[0]["command"] != "robocopy.exe" {
		t.Fatalf("robocopy = %v", host.started[0])
	}
	exit(p, "proc-1", 3)
	if host.runs["run-1"]["success"] != true {
		t.Fatalf("robocopy exit 3 = %v", host.runs["run-1"])
	}
}

func TestRestoreNeverOverwrites(t *testing.T) {
	p, host := setup(t, "linux")
	call(t, p, "backup.create", map[string]any{"job": resticJob()})
	host.files["/|restore"] = ""
	host.files["/|restore/old.txt"] = "x"
	host.files["/|empty"] = ""
	for _, params := range []map[string]any{
		{"id": "job-1", "snapshot": "latest", "target": "/restore"},
		{"id": "job-1", "snapshot": "latest", "target": "/srv/photos/restored"},
		{"id": "job-1", "snapshot": "latest", "target": "relative"},
		{"id": "job-1", "snapshot": "../x", "target": "/new"},
		{"id": "job-1", "snapshot": "latest", "target": "/"},
	} {
		if _, failure := callRaw(p, "backup.restore", params); failure == nil {
			t.Errorf("restore %v was accepted", params)
		}
	}
	call(t, p, "backup.restore", map[string]any{"id": "job-1", "snapshot": "4f2a9c1e", "target": "/empty", "include": []string{"/srv/photos/2024"}})
	if args := strings.Join(toStrings(host.started[0]["args"]), " "); !strings.HasSuffix(args, "restore 4f2a9c1e --target /empty --include /srv/photos/2024") || host.runs["run-1"]["kind"] != "restore" {
		t.Fatalf("restore = %v", args)
	}
	exit(p, "proc-1", 0)
	call(t, p, "backup.restore", map[string]any{"id": "job-1", "snapshot": "latest", "target": "/missing/new"})
	if _, failure := callRaw(p, "backup.maintenance", map[string]any{"id": "job-1", "kind": "forget"}); failure == nil {
		t.Fatal("unknown maintenance accepted")
	}
}

func TestEnvironmentFileAndSnapshots(t *testing.T) {
	p, host := setup(t, "linux")
	job := resticJob()
	restic := job["restic"].(map[string]any)
	delete(restic, "passwordFile")
	restic["environmentFile"] = "/etc/restic/b2.env"
	call(t, p, "backup.create", map[string]any{"job": job})
	if _, failure := callRaw(p, "backup.run", map[string]any{"id": "job-1"}); failure == nil {
		t.Fatal("a run without its environment file started")
	}
	if run := host.runs["run-1"]; run["success"] != false || !strings.Contains(run["message"].(string), "environment file /etc/restic/b2.env") {
		t.Fatalf("missing environment file = %v", run)
	}
	host.files["/|etc/restic/b2.env"] = "# B2\nexport RESTIC_PASSWORD='secret value'\nB2_ACCOUNT_ID=abc\n\n"
	call(t, p, "backup.run", map[string]any{"id": "job-1"})
	env := host.started[0]["environment"].(map[string]any)
	if env["RESTIC_PASSWORD"] != "secret value" || env["B2_ACCOUNT_ID"] != "abc" {
		t.Fatalf("environment = %v", env)
	}
	if strings.Contains(host.appended["run-2"], "secret") {
		t.Fatal("a secret reached the log")
	}
	host.run = func(args map[string]any) (map[string]any, error) {
		if strings.Join(toStrings(args["args"]), " ") != "--repo /backup/repo --no-lock snapshots --json --tag photos" || args["environment"].(map[string]any)["RESTIC_PASSWORD"] != "secret value" {
			t.Fatalf("snapshots call = %v", args)
		}
		return map[string]any{"exitCode": 0, "stdout": `[{"id":"aa","short_id":"a","time":"2026-10-01T03:00:00Z","paths":["/srv/photos"],"summary":{"total_files_processed":3,"total_bytes_processed":42}},{"id":"bb","short_id":"b","time":"2026-10-30T03:00:00Z","paths":["/srv/photos"]}]`}, nil
	}
	listed := call(t, p, "backup.snapshots", map[string]any{"id": "job-1"})
	snapshots := listed["snapshots"].([]any)
	if len(snapshots) != 2 || snapshots[0].(map[string]any)["id"] != "bb" || snapshots[1].(map[string]any)["bytes"] != float64(42) {
		t.Fatalf("snapshots = %v", snapshots)
	}
	host.run = func(map[string]any) (map[string]any, error) {
		return map[string]any{"exitCode": 1, "stderr": "Fatal: unable to open config file: no such file\n"}, nil
	}
	if _, failure := callRaw(p, "backup.snapshots", map[string]any{"id": "job-1"}); failure == nil || failure.Message != "Fatal: unable to open config file: no such file" {
		t.Fatalf("failed snapshots = %+v", failure)
	}
	host.run = func(map[string]any) (map[string]any, error) { return nil, capFailure("not_found") }
	if tools := call(t, p, "backup.tools", map[string]any{"refresh": true})["tools"].([]any); tools[0].(map[string]any)["available"] != false {
		t.Fatalf("tools = %v", tools)
	}
	if _, err := parseEnvironment("NOT VALID\n"); err == nil {
		t.Fatal("invalid environment line accepted")
	}
}

func TestBrowseListsFolders(t *testing.T) {
	p, host := setup(t, "linux")
	host.files["/|"] = ""
	host.files["/|srv"] = ""
	host.files["/|etc"] = ""
	host.files["/|vmlinuz"] = "x"
	top := call(t, p, "backup.browse", map[string]any{"path": ""})
	if folders := toStrings(top["folders"]); strings.Join(folders, ",") != "etc,srv" || top["top"] != true {
		t.Fatalf("top = %v", top)
	}
	if _, failure := callRaw(p, "backup.browse", map[string]any{"path": "srv/../.."}); failure == nil {
		t.Fatal("a parent path was accepted")
	}
	p, _ = setup(t, "windows")
	if drives := toStrings(call(t, p, "backup.browse", map[string]any{"path": ""})["folders"]); strings.Join(drives, ",") != "C:,D:" {
		t.Fatalf("drives = %v", drives)
	}
}

func TestStoredJobsSurviveARestart(t *testing.T) {
	p, host := setup(t, "linux")
	call(t, p, "backup.create", map[string]any{"job": resticJob()})
	stored := host.stored
	host = newFakeHost(t, "linux")
	host.stored = stored
	callHost = host.call
	restarted := newPlugin()
	restarted.init()
	if len(restarted.jobs) != 1 || restarted.next != 2 || host.schedules["job-1"] == nil || host.schedules["reconcile"] == nil {
		t.Fatalf("restarted = %+v schedules %v", restarted.jobs, host.schedules)
	}
	host.stored = json.RawMessage(`{"version":9}`)
	broken := newPlugin()
	broken.init()
	if _, failure := callRaw(broken, "backup.create", map[string]any{"job": resticJob()}); failure == nil || failure.Code != "state_unreadable" {
		t.Fatalf("broken state = %+v", failure)
	}
}
