package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

type fakeExec struct {
	ID, Kind, Subject, Label, Message string
	Finished                          bool
	ExitCode                          *int
	Success                           *bool
	Output                            string
}

type fakeProc struct {
	params     map[string]any
	running    bool
	terminated bool
	exitCode   *int
}

type publishedEvent struct {
	Event string
	Data  map[string]any
}

// fakeHost implements the host capabilities the plugin uses. Its semantics
// follow the real host: owner-scoped IDs, unknown IDs are not_found, and a
// terminated process stays "running" until its exit callback.
type fakeHost struct {
	t           *testing.T
	storage     map[string]json.RawMessage
	storageSets int
	schedules   map[string]map[string]any
	execs       []*fakeExec
	procs       map[string]*fakeProc
	procOrder   []string
	events      []publishedEvent
	calls       []string

	failStorageSet  bool
	failStorageGet  bool
	failStart       string
	registerErr     map[string]string
	validateErr     string
	exitOnTerminate bool
}

func newFakeHost(t *testing.T) *fakeHost {
	h := &fakeHost{t: t, storage: map[string]json.RawMessage{}, schedules: map[string]map[string]any{}, procs: map[string]*fakeProc{}, registerErr: map[string]string{}}
	callHost = h.call
	t.Cleanup(func() { callHost = pluginapi.CallHost })
	return h
}

func hostErr(code, message string) error { return &pluginapi.Error{Code: code, Message: message} }

func (h *fakeHost) decode(params any) map[string]any {
	raw, _ := json.Marshal(params)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (h *fakeHost) execution(id string) *fakeExec {
	for _, e := range h.execs {
		if e.ID == id {
			return e
		}
	}
	return nil
}

func (h *fakeHost) reply(result any, value any) error {
	if result == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, result)
}

func (h *fakeHost) executionJSON(e *fakeExec) map[string]any {
	m := map[string]any{"id": e.ID, "kind": e.Kind, "subject": e.Subject, "label": e.Label, "startedAt": "2026-01-01T00:00:00Z", "message": e.Message}
	if e.Finished {
		m["finishedAt"] = "2026-01-01T00:00:01Z"
	}
	if e.ExitCode != nil {
		m["exitCode"] = *e.ExitCode
	}
	if e.Success != nil {
		m["success"] = *e.Success
	}
	return m
}

func (h *fakeHost) call(method string, params any, result any) error {
	h.calls = append(h.calls, method)
	p := h.decode(params)
	str := func(key string) string { s, _ := p[key].(string); return s }
	switch method {
	case "storage.get":
		if h.failStorageGet {
			return hostErr("failed", "storage.get failed")
		}
		value, ok := h.storage[str("key")]
		if !ok {
			value = json.RawMessage("null")
		}
		return h.reply(result, json.RawMessage(value))
	case "storage.set":
		if h.failStorageSet {
			return hostErr("failed", "storage.set failed")
		}
		raw, _ := json.Marshal(p["value"])
		h.storage[str("key")] = raw
		h.storageSets++
		return nil
	case "events.publish":
		data, _ := p["data"].(map[string]any)
		h.events = append(h.events, publishedEvent{Event: str("event"), Data: data})
		return nil
	case "scheduler.validate":
		if h.validateErr != "" {
			return hostErr("invalid_argument", h.validateErr)
		}
		return nil
	case "scheduler.register":
		id := str("id")
		if message := h.registerErr[id]; message != "" {
			return hostErr("failed", message)
		}
		if _, exists := h.schedules[id]; exists {
			return hostErr("failed", "schedule \""+id+"\" already exists")
		}
		h.schedules[id] = p
		return nil
	case "scheduler.remove":
		if _, ok := h.schedules[str("id")]; !ok {
			return hostErr("failed", "unknown schedule")
		}
		delete(h.schedules, str("id"))
		return nil
	case "history.begin":
		e := &fakeExec{ID: "exec-" + strconv.Itoa(len(h.execs)+1), Kind: str("kind"), Subject: str("subject"), Label: str("label")}
		h.execs = append(h.execs, e)
		return h.reply(result, h.executionJSON(e))
	case "history.append":
		e := h.execution(str("id"))
		if e == nil {
			return hostErr("not_found", "unknown execution")
		}
		e.Output += str("text")
		return nil
	case "history.finish":
		e := h.execution(str("id"))
		if e == nil {
			return hostErr("not_found", "unknown execution")
		}
		if e.Finished {
			return h.reply(result, h.executionJSON(e))
		}
		e.Finished, e.Message = true, str("message")
		if code, ok := p["exitCode"].(float64); ok {
			c := int(code)
			e.ExitCode = &c
		}
		if success, ok := p["success"].(bool); ok {
			e.Success = &success
		}
		return h.reply(result, h.executionJSON(e))
	case "history.list":
		list := []map[string]any{}
		for i := len(h.execs) - 1; i >= 0; i-- {
			e := h.execs[i]
			if subject := str("subject"); subject == "" || subject == e.Subject {
				list = append(list, h.executionJSON(e))
			}
		}
		if limit, ok := p["limit"].(float64); ok && int(limit) > 0 && len(list) > int(limit) {
			list = list[:int(limit)]
		}
		return h.reply(result, map[string]any{"executions": list})
	case "history.get":
		e := h.execution(str("id"))
		if e == nil {
			return hostErr("not_found", "unknown execution")
		}
		return h.reply(result, h.executionJSON(e))
	case "history.output":
		e := h.execution(str("id"))
		if e == nil {
			return hostErr("not_found", "unknown execution")
		}
		return h.reply(result, map[string]any{"id": e.ID, "output": e.Output, "size": len(e.Output), "truncated": false})
	case "process.start":
		if h.failStart != "" {
			return hostErr("invalid_argument", h.failStart)
		}
		if h.execution(str("historyId")) == nil {
			return hostErr("invalid_argument", "historyId: unknown execution")
		}
		id := "proc-" + strconv.Itoa(len(h.procOrder)+1)
		h.procs[id] = &fakeProc{params: p, running: true}
		h.procOrder = append(h.procOrder, id)
		return h.reply(result, map[string]any{"id": id, "pid": 1000 + len(h.procOrder), "running": true})
	case "process.status", "process.terminate":
		proc := h.procs[str("id")]
		if proc == nil {
			return hostErr("not_found", "unknown process")
		}
		if method == "process.terminate" && proc.running {
			proc.terminated = true
			if h.exitOnTerminate {
				code := -1
				proc.running, proc.exitCode = false, &code
			}
		}
		status := map[string]any{"id": str("id"), "running": proc.running, "terminated": proc.terminated}
		if proc.exitCode != nil {
			status["exitCode"] = *proc.exitCode
		}
		return h.reply(result, status)
	}
	h.t.Fatalf("unexpected host call %s", method)
	return nil
}

func (h *fakeHost) eventNames() []string {
	names := make([]string, 0, len(h.events))
	for _, e := range h.events {
		names = append(names, e.Event)
	}
	return names
}

func (h *fakeHost) countEvents(name string) int {
	n := 0
	for _, e := range h.events {
		if e.Event == name {
			n++
		}
	}
	return n
}

func (h *fakeHost) lastProc() (string, *fakeProc) {
	id := h.procOrder[len(h.procOrder)-1]
	return id, h.procs[id]
}

// exit simulates the host's process.exit callback.
func (h *fakeHost) exit(pl *plugin, id string, code int, terminated, timedOut bool) {
	if proc := h.procs[id]; proc != nil {
		proc.running, proc.exitCode = false, &code
	}
	payload, _ := json.Marshal(map[string]any{"id": id, "exitCode": code, "success": code == 0 && !timedOut, "terminated": terminated, "timedOut": timedOut})
	pl.event("process.exit", payload)
}

func (h *fakeHost) fire(pl *plugin, id, callback string) {
	payload, _ := json.Marshal(map[string]any{"id": id, "callback": callback, "data": map[string]any{}})
	pl.event("scheduler.fired", payload)
}

// ---- helpers ---------------------------------------------------------------

func continuousTask(name string) Task {
	return Task{Name: name, Type: typeContinuous, Command: Command{Path: "/bin/app", Args: []string{"--x"}}, Restart: &Restart{Mode: restartOnFailure, InitialDelaySeconds: 2, MaxDelaySeconds: 60}}
}

func scheduledTask(name string) Task {
	return Task{Name: name, Type: typeScheduled, Enabled: true, Command: Command{Path: "echo", Interpreter: "direct"}, Schedule: &Schedule{Type: "interval", IntervalSeconds: 60}}
}

func boot(t *testing.T, h *fakeHost) *plugin {
	t.Helper()
	pl := newPlugin()
	pl.init()
	return pl
}

func mustCall(t *testing.T, pl *plugin, op string, request any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(request)
	result, err := pl.handle(op, raw)
	if err != nil {
		t.Fatalf("%s: %s: %s", op, err.Code, err.Message)
	}
	encoded, _ := json.Marshal(result)
	out := map[string]any{}
	if e := json.Unmarshal(encoded, &out); e != nil {
		t.Fatalf("%s result: %v", op, e)
	}
	return out
}

func callError(t *testing.T, pl *plugin, op string, request any) *rpcError {
	t.Helper()
	raw, _ := json.Marshal(request)
	_, err := pl.handle(op, raw)
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded", op)
	}
	return err
}

func createTask(t *testing.T, pl *plugin, task Task) string {
	t.Helper()
	result := mustCall(t, pl, "tasks.create", map[string]any{"task": task})
	return result["task"].(map[string]any)["id"].(string)
}

func statusOf(t *testing.T, pl *plugin, id string) map[string]any {
	t.Helper()
	view := pl.view(pl.find(id))
	raw, _ := json.Marshal(view.Status)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// ---- persistence and validation --------------------------------------------

func TestCRUDPersistsAcrossRestartAndIDsAreNotReused(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	a := createTask(t, pl, continuousTask("Alpha"))
	b := createTask(t, pl, scheduledTask("Beta"))
	if a != "task-1" || b != "task-2" {
		t.Fatalf("ids = %s %s", a, b)
	}
	updated := scheduledTask("Beta renamed")
	updated.ID = b
	updated.Description = "  described  "
	mustCall(t, pl, "tasks.update", map[string]any{"task": updated})
	mustCall(t, pl, "tasks.delete", map[string]any{"id": a})
	c := createTask(t, pl, continuousTask("Gamma"))
	if c != "task-3" {
		t.Fatalf("deleted ID reused: %s", c)
	}

	h.schedules = map[string]map[string]any{} // a restart loses registrations.
	pl2 := boot(t, h)
	list := mustCall(t, pl2, "tasks.list", nil)["tasks"].([]any)
	if len(list) != 2 {
		t.Fatalf("tasks after restart = %d", len(list))
	}
	first := list[0].(map[string]any)["task"].(map[string]any)
	if first["name"] != "Beta renamed" || first["description"] != "described" {
		t.Fatalf("persisted task = %#v", first)
	}
	if _, ok := h.schedules[b]; !ok {
		t.Fatalf("enabled schedule not restored: %#v", h.schedules)
	}
	if id := createTask(t, pl2, continuousTask("Delta")); id != "task-4" {
		t.Fatalf("counter not persisted: %s", id)
	}
}

func TestValidationRejectsWithoutPersisting(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	bad := func(name string, mutate func(*Task), want string) {
		t.Helper()
		task := continuousTask("X")
		mutate(&task)
		err := callError(t, pl, "tasks.create", map[string]any{"task": task})
		if err.Code != "invalid_argument" || !strings.Contains(err.Message, want) {
			t.Errorf("%s: %s: %s", name, err.Code, err.Message)
		}
	}
	bad("name", func(t *Task) { t.Name = "  " }, "name is required")
	bad("path", func(t *Task) { t.Command.Path = "" }, "command is required")
	bad("interpreter", func(t *Task) { t.Command.Interpreter = "perl" }, "unsupported interpreter")
	bad("env equals", func(t *Task) { t.Command.Environment = map[string]string{"A=B": "x"} }, "must not contain =")
	bad("env case", func(t *Task) { t.Command.Environment = map[string]string{"Path": "x", "PATH": "y"} }, "conflict")
	bad("restart", func(t *Task) { t.Restart.Mode = "sometimes" }, "restart mode")
	bad("type", func(t *Task) { t.Type = "backup" }, "task type")
	bad("retries", func(t *Task) { t.Restart.MaxRetries = maxRetries + 1 }, "out of range")
	sched := scheduledTask("S")
	sched.Schedule = nil
	if err := callError(t, pl, "tasks.create", map[string]any{"task": sched}); !strings.Contains(err.Message, "requires a schedule") {
		t.Errorf("schedule missing: %s", err.Message)
	}
	sched = scheduledTask("S")
	sched.OverlapPolicy = "queue"
	if err := callError(t, pl, "tasks.create", map[string]any{"task": sched}); !strings.Contains(err.Message, "overlap") {
		t.Errorf("overlap: %s", err.Message)
	}
	sched = scheduledTask("S")
	sched.TimeoutSeconds = -1
	if err := callError(t, pl, "tasks.create", map[string]any{"task": sched}); !strings.Contains(err.Message, "timeoutSeconds") {
		t.Errorf("timeout: %s", err.Message)
	}
	h.validateErr = "invalid timeZone \"Mars/Base\""
	sched = scheduledTask("S")
	if err := callError(t, pl, "tasks.create", map[string]any{"task": sched}); err.Code != "invalid_argument" || !strings.Contains(err.Message, "Mars/Base") {
		t.Errorf("host schedule error = %#v", err)
	}
	if h.storageSets != 0 || len(pl.tasks) != 0 || len(h.schedules) != 1 { // only the reconcile safety net.
		t.Fatalf("failed creates leaked state: sets=%d tasks=%d schedules=%v", h.storageSets, len(pl.tasks), h.schedules)
	}
	pl.tasks = nil
	old := continuousTask("Keep")
	id := createTask(t, pl, old)
	changed := continuousTask("Keep")
	changed.ID, changed.Type = id, typeScheduled
	changed.Schedule = &Schedule{Type: "interval", IntervalSeconds: 5}
	if err := callError(t, pl, "tasks.update", map[string]any{"task": changed}); !strings.Contains(err.Message, "cannot be changed") {
		t.Errorf("type change: %s", err.Message)
	}
	if err := callError(t, pl, "tasks.update", map[string]any{"task": Task{ID: "task-999", Name: "n", Type: typeContinuous, Command: Command{Path: "x"}}}); err.Code != "not_found" {
		t.Errorf("unknown update: %#v", err)
	}
}

func TestDefaultsMatchLegacySemantics(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	c := continuousTask("C")
	c.Restart = nil
	c.Command.Interpreter = " AUTO "
	view := mustCall(t, pl, "tasks.create", map[string]any{"task": c})["task"].(map[string]any)
	restart := view["restart"].(map[string]any)
	if restart["mode"] != "on-failure" || restart["initialDelaySeconds"] != float64(2) || restart["maxDelaySeconds"] != float64(60) {
		t.Fatalf("restart defaults = %#v", restart)
	}
	s := scheduledTask("S")
	s.OverlapPolicy = ""
	s.Command.Interpreter = ""
	s.Autostart = true
	sv := mustCall(t, pl, "tasks.create", map[string]any{"task": s})["task"].(map[string]any)
	if sv["overlapPolicy"] != "skip" || sv["autostart"] != nil {
		t.Fatalf("scheduled defaults = %#v", sv)
	}
	for i, want := range []int{2, 4, 8, 16, 32, 60, 60} {
		if got := restartDelay(&Restart{InitialDelaySeconds: 2, MaxDelaySeconds: 60}, i); got != want {
			t.Errorf("delay(%d) = %d, want %d", i, got, want)
		}
	}
}

func TestCreateRollsBackScheduleWhenStorageFails(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	h.failStorageSet = true
	if err := callError(t, pl, "tasks.create", map[string]any{"task": scheduledTask("S")}); err.Code != "failed" {
		t.Fatalf("error = %#v", err)
	}
	if _, ok := h.schedules["task-1"]; ok || len(pl.tasks) != 0 {
		t.Fatalf("registration or task leaked: %v %d", h.schedules, len(pl.tasks))
	}
	h.failStorageSet = false
	id := createTask(t, pl, scheduledTask("S"))
	h.failStorageSet = true
	upd := scheduledTask("S2")
	upd.ID = id
	upd.Schedule = &Schedule{Type: "interval", IntervalSeconds: 5}
	callError(t, pl, "tasks.update", map[string]any{"task": upd})
	if pl.find(id).Name != "S" || h.schedules[id]["schedule"].(map[string]any)["intervalSeconds"] != float64(60) {
		t.Fatalf("update not rolled back: %v", h.schedules[id])
	}
	callError(t, pl, "tasks.delete", map[string]any{"id": id})
	if pl.find(id) == nil {
		t.Fatal("delete was applied although persistence failed")
	}
}

func TestSchedulerRegistrationFailureKeepsPreviousSchedule(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	id := createTask(t, pl, scheduledTask("S"))
	upd := scheduledTask("S")
	upd.ID = id
	upd.Schedule = &Schedule{Type: "interval", IntervalSeconds: 5}
	h.registerErr[id] = "scheduler exploded"
	err := callError(t, pl, "tasks.update", map[string]any{"task": upd})
	if err.Code != "failed" || !strings.Contains(err.Message, "scheduler exploded") {
		t.Fatalf("error = %#v", err)
	}
	// The failing ID also blocks restoring the old registration in this fake,
	// so the important invariants are persistence and in-memory definition.
	if pl.find(id).Schedule.IntervalSeconds != 60 {
		t.Fatalf("definition changed despite registration failure")
	}
	var stored stateFile
	_ = json.Unmarshal(h.storage[storageKey], &stored)
	if stored.Tasks[0].Schedule.IntervalSeconds != 60 {
		t.Fatalf("stored definition changed: %#v", stored.Tasks[0].Schedule)
	}
}

// ---- initialization ---------------------------------------------------------

func seedState(h *fakeHost, tasks ...Task) {
	raw, _ := json.Marshal(stateFile{Version: stateVersion, Next: 10, Tasks: tasks})
	h.storage[storageKey] = raw
}

func TestInitRegistersEnabledSchedulesAndAutostartsOnly(t *testing.T) {
	h := newFakeHost(t)
	on, off := scheduledTask("On"), scheduledTask("Off")
	on.ID, off.ID = "task-1", "task-2"
	off.Enabled = false
	auto, manual := continuousTask("Auto"), continuousTask("Manual")
	auto.ID, manual.ID = "task-3", "task-4"
	auto.Autostart = true
	seedState(h, on, off, auto, manual)
	pl := boot(t, h)
	if _, ok := h.schedules["task-1"]; !ok {
		t.Fatal("enabled schedule not registered")
	}
	if _, ok := h.schedules["task-2"]; ok {
		t.Fatal("disabled schedule registered")
	}
	if len(h.procOrder) != 1 || h.execs[0].Subject != "task-3" || h.execs[0].Kind != "process" {
		t.Fatalf("autostart processes = %v execs=%v", h.procOrder, h.execs)
	}
	_, proc := h.lastProc()
	if proc.params["interpreter"] != "auto" || proc.params["historyId"] != "exec-1" || proc.params["command"] != "/bin/app" {
		t.Fatalf("process.start = %#v", proc.params)
	}
	if got := statusOf(t, pl, "task-3"); got["state"] != "running" || got["pid"] != float64(1001) {
		t.Fatalf("status = %#v", got)
	}
	if got := statusOf(t, pl, "task-4"); got["state"] != "stopped" {
		t.Fatalf("manual status = %#v", got)
	}
	if pl.next != 10 {
		t.Fatalf("next = %d", pl.next)
	}
	if h.storageSets != 0 {
		t.Fatal("init rewrote stored state")
	}
}

func TestInitRestoresLastRunFromHistory(t *testing.T) {
	h := newFakeHost(t)
	task := scheduledTask("S")
	task.ID = "task-1"
	seedState(h, task)
	code, ok := 7, false
	h.execs = []*fakeExec{{ID: "exec-9", Kind: "command", Subject: "task-1", Label: "S", Finished: true, ExitCode: &code, Success: &ok, Message: "exit code 7"}}
	pl := boot(t, h)
	got := statusOf(t, pl, "task-1")
	if got["state"] != "failure" || got["lastExitCode"] != float64(7) || got["message"] != "exit code 7" || got["lastRunId"] != "exec-9" {
		t.Fatalf("status = %#v", got)
	}
}

func TestMalformedStoredStateIsPreservedAndReported(t *testing.T) {
	cases := map[string]string{
		"not json":       `{"version":1,"tasks":`,
		"wrong type":     `{"version":1,"tasks":"nope"}`,
		"future version": `{"version":2,"next":3,"tasks":[]}`,
		"no version":     `{"tasks":[]}`,
		"duplicate ids":  `{"version":1,"next":3,"tasks":[{"id":"task-1","name":"a","type":"continuous","command":{"path":"x"}},{"id":"task-1","name":"b","type":"continuous","command":{"path":"y"}}]}`,
		"missing id":     `{"version":1,"next":3,"tasks":[{"name":"a","type":"continuous","command":{"path":"x"}}]}`,
	}
	for name, stored := range cases {
		t.Run(name, func(t *testing.T) {
			h := newFakeHost(t)
			h.storage[storageKey] = json.RawMessage(stored)
			pl := boot(t, h)
			list := mustCall(t, pl, "tasks.list", nil)
			if list["loadError"] == nil || len(list["tasks"].([]any)) != 0 {
				t.Fatalf("list = %#v", list)
			}
			for _, op := range []string{"tasks.create", "tasks.update", "tasks.delete", "tasks.start", "tasks.stop", "tasks.restart", "tasks.run"} {
				err := callError(t, pl, op, map[string]any{"task": continuousTask("n"), "id": "task-1"})
				if err.Code != "state_unreadable" {
					t.Errorf("%s = %#v", op, err)
				}
			}
			if string(h.storage[storageKey]) != stored || h.storageSets != 0 {
				t.Fatalf("stored state was modified: %s", h.storage[storageKey])
			}
			if len(h.procOrder) != 0 {
				t.Fatal("started processes from unreadable state")
			}
		})
	}
}

func TestStorageReadFailureIsReportedWithoutWriting(t *testing.T) {
	h := newFakeHost(t)
	h.failStorageGet = true
	pl := boot(t, h)
	if !strings.Contains(mustCall(t, pl, "tasks.list", nil)["loadError"].(string), "could not read") {
		t.Fatal("read failure not reported")
	}
	callError(t, pl, "tasks.create", map[string]any{"task": continuousTask("n")})
	if h.storageSets != 0 {
		t.Fatal("wrote after read failure")
	}
}

func TestPerTaskRestoreFailuresAreIsolated(t *testing.T) {
	h := newFakeHost(t)
	bad, good, broken := scheduledTask("Bad schedule"), continuousTask("Good"), continuousTask("Broken")
	bad.ID, good.ID, broken.ID = "task-1", "task-2", "task-3"
	good.Autostart, broken.Autostart = true, true
	broken.Command.Path = ""
	seedState(h, bad, good, broken)
	h.registerErr["task-1"] = "cron parser exploded"
	pl := boot(t, h)
	if got := statusOf(t, pl, "task-1"); !strings.Contains(got["error"].(string), "cron parser exploded") {
		t.Fatalf("schedule error = %#v", got)
	}
	if got := statusOf(t, pl, "task-2"); got["state"] != "running" {
		t.Fatalf("good task = %#v", got)
	}
	if got := statusOf(t, pl, "task-3"); !strings.Contains(got["error"].(string), "command is required") || got["state"] != "stopped" {
		t.Fatalf("broken task = %#v", got)
	}
	// Definitions stay intact on disk.
	var stored stateFile
	_ = json.Unmarshal(h.storage[storageKey], &stored)
	if len(stored.Tasks) != 3 {
		t.Fatalf("stored tasks = %d", len(stored.Tasks))
	}
}

func TestAutostartFailureIsReportedAndRecorded(t *testing.T) {
	h := newFakeHost(t)
	task := continuousTask("Auto")
	task.ID, task.Autostart = "task-1", true
	seedState(h, task)
	h.failStart = "unsupported interpreter \"cmd\""
	pl := boot(t, h)
	got := statusOf(t, pl, "task-1")
	if got["state"] != "stopped" || !strings.Contains(got["error"].(string), "autostart failed") || got["lastSuccess"] != false {
		t.Fatalf("status = %#v", got)
	}
	if len(h.execs) != 1 || !h.execs[0].Finished || !strings.Contains(h.execs[0].Output, "start error") {
		t.Fatalf("failure not recorded in history: %#v", h.execs)
	}
}

// ---- continuous tasks -------------------------------------------------------

func TestContinuousStartStopDoesNotRestart(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	id := createTask(t, pl, continuousTask("C"))
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	if err := callError(t, pl, "tasks.start", map[string]any{"id": id}); err.Code != "already_running" {
		t.Fatalf("double start = %#v", err)
	}
	pid, _ := h.lastProc()
	view := mustCall(t, pl, "tasks.stop", map[string]any{"id": id})
	if view["status"].(map[string]any)["state"] != "stopping" {
		t.Fatalf("after stop = %#v", view["status"])
	}
	h.exit(pl, pid, -1, true, false)
	got := statusOf(t, pl, id)
	if got["state"] != "stopped" || got["lastSuccess"] != false || got["message"] != "stopped" {
		t.Fatalf("stopped status = %#v", got)
	}
	if len(h.procOrder) != 1 {
		t.Fatalf("manual stop restarted the task: %v", h.procOrder)
	}
	if _, ok := h.schedules[restartPrefix+id]; ok {
		t.Fatal("restart timer registered after manual stop")
	}
	if !h.execs[0].Finished || h.execs[0].Message != "stopped" {
		t.Fatalf("execution = %#v", h.execs[0])
	}
}

func TestRestartOnFailureBackoffAndMaxRetries(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	task := continuousTask("C")
	task.Restart = &Restart{Mode: restartOnFailure, InitialDelaySeconds: 2, MaxDelaySeconds: 60, MaxRetries: 3}
	id := createTask(t, pl, task)
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	for i, wantDelay := range []float64{2, 4, 8} {
		pid, _ := h.lastProc()
		h.exit(pl, pid, 1, false, false)
		got := statusOf(t, pl, id)
		if got["state"] != "restarting" || got["restartCount"] != float64(i+1) {
			t.Fatalf("after failure %d: %#v", i+1, got)
		}
		timer := h.schedules[restartPrefix+id]
		if timer == nil || timer["callback"] != "restart" || timer["schedule"].(map[string]any)["intervalSeconds"] != wantDelay {
			t.Fatalf("restart timer %d = %#v", i+1, timer)
		}
		h.fire(pl, restartPrefix+id, "restart")
		if _, ok := h.schedules[restartPrefix+id]; ok {
			t.Fatal("restart timer not removed after firing")
		}
		if got := statusOf(t, pl, id); got["state"] != "running" {
			t.Fatalf("not restarted: %#v", got)
		}
	}
	pid, _ := h.lastProc()
	h.exit(pl, pid, 1, false, false)
	got := statusOf(t, pl, id)
	if got["state"] != "stopped" || !strings.Contains(got["message"].(string), "restart limit") {
		t.Fatalf("after max retries: %#v", got)
	}
	if len(h.procOrder) != 4 {
		t.Fatalf("processes = %d", len(h.procOrder))
	}
	// A late second firing of the interval timer is harmless.
	h.fire(pl, restartPrefix+id, "restart")
	if len(h.procOrder) != 4 {
		t.Fatal("stale restart timer started a process")
	}
	// Manual start resets the retry counter.
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	if statusOf(t, pl, id)["restartCount"] != nil {
		t.Fatal("manual start did not reset restartCount")
	}
}

func TestRestartModes(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		code     int
		restarts bool
	}{{restartNever, 1, false}, {restartOnFailure, 0, false}, {restartOnFailure, 1, true}, {restartAlways, 0, true}, {restartAlways, 3, true}} {
		h := newFakeHost(t)
		pl := boot(t, h)
		task := continuousTask("C")
		task.Restart.Mode = tc.mode
		id := createTask(t, pl, task)
		mustCall(t, pl, "tasks.start", map[string]any{"id": id})
		pid, _ := h.lastProc()
		h.exit(pl, pid, tc.code, false, false)
		state := statusOf(t, pl, id)["state"]
		if tc.restarts != (state == "restarting") {
			t.Errorf("mode=%s code=%d state=%v", tc.mode, tc.code, state)
		}
	}
}

func TestStopWhileWaitingToRestartCancelsTheTimer(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	id := createTask(t, pl, continuousTask("C"))
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	pid, _ := h.lastProc()
	h.exit(pl, pid, 1, false, false)
	mustCall(t, pl, "tasks.stop", map[string]any{"id": id})
	if _, ok := h.schedules[restartPrefix+id]; ok || statusOf(t, pl, id)["state"] != "stopped" {
		t.Fatalf("stop did not cancel restart: %v %v", h.schedules, statusOf(t, pl, id))
	}
	h.fire(pl, restartPrefix+id, "restart")
	if len(h.procOrder) != 1 {
		t.Fatal("cancelled restart still started a process")
	}
}

func TestRestartActionStopsThenStartsFresh(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	task := continuousTask("C")
	task.Restart.Mode = restartNever
	id := createTask(t, pl, task)
	mustCall(t, pl, "tasks.restart", map[string]any{"id": id}) // not running: plain start.
	if len(h.procOrder) != 1 {
		t.Fatal("restart of a stopped task did not start it")
	}
	first, _ := h.lastProc()
	mustCall(t, pl, "tasks.restart", map[string]any{"id": id})
	if statusOf(t, pl, id)["state"] != "stopping" || len(h.procOrder) != 1 {
		t.Fatalf("restart must wait for the exit: %#v", statusOf(t, pl, id))
	}
	h.exit(pl, first, -1, true, false)
	if got := statusOf(t, pl, id); got["state"] != "running" || len(h.procOrder) != 2 {
		t.Fatalf("after exit = %#v procs=%d", got, len(h.procOrder))
	}
	if h.execs[0].Message != "stopped" || h.execs[1].Finished {
		t.Fatalf("executions = %#v %#v", h.execs[0], h.execs[1])
	}
}

func TestProcessStartFailureIsRecordedAndReturned(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	id := createTask(t, pl, continuousTask("C"))
	h.failStart = "unsupported interpreter \"cmd\""
	err := callError(t, pl, "tasks.start", map[string]any{"id": id})
	if err.Code != "invalid_argument" || !strings.Contains(err.Message, "interpreter") {
		t.Fatalf("error = %#v", err)
	}
	if got := statusOf(t, pl, id); got["state"] != "stopped" || got["lastSuccess"] != false || got["message"] == nil {
		t.Fatalf("status = %#v", got)
	}
	if !h.execs[0].Finished || *h.execs[0].Success || !strings.Contains(h.execs[0].Output, "start error") {
		t.Fatalf("execution = %#v", h.execs[0])
	}
	h.failStart = ""
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
}

func TestUpdateWhileRunningAppliesOnNextStart(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	id := createTask(t, pl, continuousTask("C"))
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	upd := continuousTask("C")
	upd.ID = id
	upd.Command.Path = "/bin/new"
	upd.Restart.Mode = restartAlways
	mustCall(t, pl, "tasks.update", map[string]any{"task": upd})
	if statusOf(t, pl, id)["state"] != "running" {
		t.Fatal("update stopped the running process")
	}
	pid, _ := h.lastProc()
	h.exit(pl, pid, 0, false, false)
	h.fire(pl, restartPrefix+id, "restart")
	if _, proc := h.lastProc(); proc.params["command"] != "/bin/new" {
		t.Fatalf("restart used stale definition: %#v", proc.params)
	}
}

func TestDeleteRunningTaskStopsItAndKeepsHistory(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	id := createTask(t, pl, continuousTask("C"))
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	pid, proc := h.lastProc()
	mustCall(t, pl, "tasks.delete", map[string]any{"id": id})
	if !proc.terminated || pl.find(id) != nil || pl.rt[id] != nil {
		t.Fatalf("delete left state: terminated=%v", proc.terminated)
	}
	h.exit(pl, pid, -1, true, false) // late exit for a deleted task.
	if !h.execs[0].Finished || len(h.procOrder) != 1 {
		t.Fatalf("late exit mishandled: %#v", h.execs[0])
	}
	runs := mustCall(t, pl, "tasks.history", map[string]any{"id": id})["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("history after delete = %d", len(runs))
	}
}

// ---- scheduled tasks --------------------------------------------------------

func TestScheduleLifecycleFollowsDefinition(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	task := scheduledTask("S")
	task.Schedule = &Schedule{Type: "cron", Cron: "0 * * * *", TimeZone: "Europe/Budapest"}
	id := createTask(t, pl, task)
	reg := h.schedules[id]
	if reg["callback"] != "task" || reg["schedule"].(map[string]any)["cron"] != "0 * * * *" || reg["schedule"].(map[string]any)["timeZone"] != "Europe/Budapest" {
		t.Fatalf("registration = %#v", reg)
	}
	upd := task
	upd.ID, upd.Enabled = id, false
	mustCall(t, pl, "tasks.update", map[string]any{"task": upd})
	if _, ok := h.schedules[id]; ok {
		t.Fatal("disabled task still registered")
	}
	upd.Enabled, upd.Schedule = true, &Schedule{Type: "daily", TimeOfDay: "03:00"}
	mustCall(t, pl, "tasks.update", map[string]any{"task": upd})
	if h.schedules[id]["schedule"].(map[string]any)["timeOfDay"] != "03:00" {
		t.Fatalf("schedule not replaced: %#v", h.schedules[id])
	}
	mustCall(t, pl, "tasks.delete", map[string]any{"id": id})
	if _, ok := h.schedules[id]; ok {
		t.Fatal("deleted task still registered")
	}
}

func TestScheduledFireRunsWithTimeoutAndRecordsResult(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	task := scheduledTask("S")
	task.TimeoutSeconds = 30
	task.Command.Environment = map[string]string{"A": "1"}
	task.Command.WorkingDirectory = "/tmp"
	id := createTask(t, pl, task)
	h.fire(pl, id, "task")
	pid, proc := h.lastProc()
	if proc.params["timeoutSeconds"] != float64(30) || proc.params["workingDirectory"] != "/tmp" || proc.params["environment"].(map[string]any)["A"] != "1" || proc.params["interpreter"] != "direct" {
		t.Fatalf("process.start = %#v", proc.params)
	}
	if h.execs[0].Kind != "command" || statusOf(t, pl, id)["state"] != "running" {
		t.Fatalf("running state = %#v", statusOf(t, pl, id))
	}
	h.exit(pl, pid, 0, false, false)
	got := statusOf(t, pl, id)
	if got["state"] != "idle" || got["lastSuccess"] != true || got["lastExitCode"] != float64(0) || got["currentRunId"] != nil {
		t.Fatalf("after success = %#v", got)
	}
	if h.countEvents("tasks.run.completed") != 1 {
		t.Fatalf("events = %v", h.eventNames())
	}
	h.fire(pl, id, "task")
	pid, _ = h.lastProc()
	h.exit(pl, pid, -1, false, true)
	got = statusOf(t, pl, id)
	if got["state"] != "failure" || got["message"] != "timed out after 30 seconds" {
		t.Fatalf("after timeout = %#v", got)
	}
}

func TestOverlapPolicy(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	skip := createTask(t, pl, scheduledTask("Skip"))
	allowTask := scheduledTask("Allow")
	allowTask.OverlapPolicy = overlapAllow
	allow := createTask(t, pl, allowTask)

	h.fire(pl, skip, "task")
	h.fire(pl, skip, "task") // silently skipped.
	if len(h.procOrder) != 1 {
		t.Fatalf("skip policy started %d processes", len(h.procOrder))
	}
	if err := callError(t, pl, "tasks.run", map[string]any{"id": skip}); err.Code != "already_running" {
		t.Fatalf("manual overlap = %#v", err)
	}
	h.fire(pl, allow, "task")
	mustCall(t, pl, "tasks.run", map[string]any{"id": allow})
	if len(h.procOrder) != 3 || statusOf(t, pl, allow)["runningCount"] != float64(2) {
		t.Fatalf("allow policy: procs=%d status=%#v", len(h.procOrder), statusOf(t, pl, allow))
	}
	first := h.procOrder[1]
	h.exit(pl, first, 0, false, false)
	if got := statusOf(t, pl, allow); got["state"] != "running" || got["runningCount"] != float64(1) {
		t.Fatalf("one of two finished = %#v", got)
	}
	h.exit(pl, h.procOrder[2], 0, false, false)
	if got := statusOf(t, pl, allow); got["state"] != "idle" {
		t.Fatalf("all finished = %#v", got)
	}
	// After the skip-run completes a new run is accepted again.
	h.exit(pl, h.procOrder[0], 0, false, false)
	mustCall(t, pl, "tasks.run", map[string]any{"id": skip})
}

func TestRunNowWorksForDisabledScheduleAndRejectsWrongType(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	task := scheduledTask("S")
	task.Enabled = false
	id := createTask(t, pl, task)
	if result := mustCall(t, pl, "tasks.run", map[string]any{"id": id}); result["runId"] != "exec-1" {
		t.Fatalf("run = %#v", result)
	}
	cont := createTask(t, pl, continuousTask("C"))
	if err := callError(t, pl, "tasks.run", map[string]any{"id": cont}); err.Code != "invalid_argument" {
		t.Fatalf("run continuous = %#v", err)
	}
	if err := callError(t, pl, "tasks.start", map[string]any{"id": id}); err.Code != "invalid_argument" {
		t.Fatalf("start scheduled = %#v", err)
	}
	// A fire for a disabled task is ignored and its stale registration removed.
	h.schedules[id] = map[string]any{}
	before := len(h.procOrder)
	h.fire(pl, id, "task")
	if len(h.procOrder) != before {
		t.Fatal("disabled task ran on schedule")
	}
	if _, ok := h.schedules[id]; ok {
		t.Fatal("stale registration not removed")
	}
}

func TestStopTerminatesScheduledRuns(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	id := createTask(t, pl, scheduledTask("S"))
	mustCall(t, pl, "tasks.run", map[string]any{"id": id})
	_, proc := h.lastProc()
	mustCall(t, pl, "tasks.stop", map[string]any{"id": id})
	if !proc.terminated {
		t.Fatal("stop did not terminate the run")
	}
}

// ---- races and recovery -----------------------------------------------------

func TestExitEventsAreIdempotentAndIgnoreUnknownProcesses(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	task := continuousTask("C")
	task.Restart.Mode = restartAlways
	id := createTask(t, pl, task)
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	pid, _ := h.lastProc()
	h.exit(pl, pid, 1, false, false)
	h.exit(pl, pid, 1, false, false) // duplicate delivery.
	h.exit(pl, "proc-unknown", 1, false, false)
	if got := statusOf(t, pl, id); got["restartCount"] != float64(1) || h.countEvents("tasks.run.completed") != 1 {
		t.Fatalf("duplicate exit had effect: %#v", got)
	}
	pl.event("process.exit", json.RawMessage(`not json`))
	pl.event("process.stdout", json.RawMessage(`{"id":"x","text":"y"}`))
	pl.event("something.new", json.RawMessage(`{}`))
}

func TestReconcileSettlesExitWhoseEventWasLost(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	task := continuousTask("C")
	task.Restart.Mode = restartNever
	id := createTask(t, pl, task)
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	pid, proc := h.lastProc()
	code := 5
	proc.running, proc.exitCode = false, &code // exited, callback never delivered.
	list := mustCall(t, pl, "tasks.list", nil)["tasks"].([]any)
	status := list[0].(map[string]any)["status"].(map[string]any)
	if status["state"] != "stopped" || status["lastExitCode"] != float64(5) || status["message"] != "exit code 5" {
		t.Fatalf("status = %#v", status)
	}
	if !h.execs[0].Finished {
		t.Fatal("execution left unfinished")
	}
	h.exit(pl, pid, 5, false, false) // the late callback changes nothing.
	if h.countEvents("tasks.run.completed") != 1 {
		t.Fatal("late callback double-processed")
	}
	// The periodic safety net does the same without a browser request.
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	_, proc = h.lastProc()
	proc.running, proc.exitCode = false, &code
	h.fire(pl, reconcileID, callbackCheck)
	if statusOf(t, pl, id)["state"] != "stopped" {
		t.Fatal("scheduled reconcile did not settle the exit")
	}
}

func TestReconcileTreatsForgottenProcessAsFailed(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	task := continuousTask("C")
	task.Restart.Mode = restartNever
	id := createTask(t, pl, task)
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	pid, _ := h.lastProc()
	delete(h.procs, pid)
	mustCall(t, pl, "tasks.list", nil)
	if got := statusOf(t, pl, id); got["state"] != "stopped" || got["lastSuccess"] != false {
		t.Fatalf("status = %#v", got)
	}
}

func TestStopSettlesImmediatelyWhenProcessAlreadyExited(t *testing.T) {
	h := newFakeHost(t)
	h.exitOnTerminate = true
	pl := boot(t, h)
	id := createTask(t, pl, continuousTask("C"))
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	view := mustCall(t, pl, "tasks.stop", map[string]any{"id": id})
	if view["status"].(map[string]any)["state"] != "stopped" || len(h.procOrder) != 1 {
		t.Fatalf("status = %#v", view["status"])
	}
}

func TestShutdownTerminatesAndFinalizesEverything(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	a := createTask(t, pl, continuousTask("A"))
	mustCall(t, pl, "tasks.start", map[string]any{"id": a})
	pid, _ := h.lastProc()
	h.exit(pl, pid, 1, false, false) // A is waiting to restart.
	b := createTask(t, pl, scheduledTask("B"))
	mustCall(t, pl, "tasks.run", map[string]any{"id": b})
	c := createTask(t, pl, continuousTask("C"))
	mustCall(t, pl, "tasks.start", map[string]any{"id": c})
	pl.shutdown()
	for _, id := range h.procOrder[1:] {
		if !h.procs[id].terminated {
			t.Fatalf("%s not terminated", id)
		}
	}
	for _, e := range h.execs {
		if !e.Finished {
			t.Fatalf("execution %s left unfinished", e.ID)
		}
	}
	if _, ok := h.schedules[restartPrefix+a]; ok {
		t.Fatal("restart timer survived shutdown")
	}
	if len(pl.procs) != 0 {
		t.Fatal("process table not cleared")
	}
	pl.event("scheduler.fired", json.RawMessage(`{"id":"`+a+`","callback":"restart"}`))
	if len(h.procOrder) != 3 {
		t.Fatal("restart fired after shutdown")
	}
}

// ---- history RPC ------------------------------------------------------------

func TestHistoryAndOutputRPC(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	id := createTask(t, pl, scheduledTask("S"))
	mustCall(t, pl, "tasks.run", map[string]any{"id": id})
	h.execs[0].Output = "hello\n"
	pid, _ := h.lastProc()
	h.exit(pl, pid, 0, false, false)
	runs := mustCall(t, pl, "tasks.history", map[string]any{"id": id, "limit": 5})["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["id"] != "exec-1" {
		t.Fatalf("runs = %#v", runs)
	}
	if other := mustCall(t, pl, "tasks.history", map[string]any{"id": "task-other"})["runs"].([]any); len(other) != 0 {
		t.Fatalf("other subject = %#v", other)
	}
	out := mustCall(t, pl, "tasks.output", map[string]any{"runId": "exec-1", "maxBytes": 100})
	if out["output"] != "hello\n" || out["run"].(map[string]any)["success"] != true || out["truncated"] != false {
		t.Fatalf("output = %#v", out)
	}
	if err := callError(t, pl, "tasks.output", map[string]any{"runId": "exec-404"}); err.Code != "not_found" {
		t.Fatalf("unknown run = %#v", err)
	}
	if err := callError(t, pl, "tasks.nope", nil); err.Code != "unknown_method" {
		t.Fatalf("unknown method = %#v", err)
	}
	if _, err := pl.handle("tasks.delete", json.RawMessage(`{"id":`)); err == nil || err.Code != "invalid_argument" {
		t.Fatalf("bad json = %#v", err)
	}
}

func TestStatusEventsAreEmittedForStateChanges(t *testing.T) {
	h := newFakeHost(t)
	pl := boot(t, h)
	id := createTask(t, pl, continuousTask("C"))
	mustCall(t, pl, "tasks.start", map[string]any{"id": id})
	pid, _ := h.lastProc()
	mustCall(t, pl, "tasks.stop", map[string]any{"id": id})
	h.exit(pl, pid, -1, true, false)
	names := strings.Join(h.eventNames(), ",")
	if h.countEvents("tasks.changed") != 1 || h.countEvents("tasks.status") != 3 || h.countEvents("tasks.run.completed") != 1 {
		t.Fatalf("events = %s", names)
	}
	last := h.events[len(h.events)-1]
	if last.Event != "tasks.status" || last.Data["status"].(map[string]any)["state"] != "stopped" {
		t.Fatalf("last event = %#v", last)
	}
}
