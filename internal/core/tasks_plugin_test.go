package core

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
)

// These tests load the checked-in Tasks WASM (ABI v2) through the normal
// controller path, so they exercise storage, scheduler, process, history and
// event delivery exactly as a user-enabled plugin would.

const tasksID = "tasks"

func copyTasksPlugin(t *testing.T, dataDir string) {
	t.Helper()
	source := filepath.Join("..", "..", "plugins", tasksID)
	destination := filepath.Join(dataDir, "plugins", tasksID, "releases", "0.1.0")
	for _, name := range []string{"plugin.yaml", "backend/plugin.wasm", "web/plugin.js", "web/plugin.css"} {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func openTasksController(t *testing.T, dataDir string) *Controller {
	t.Helper()
	copyTasksPlugin(t, dataDir)
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error {
		enabled := true
		if cfg.Plugins == nil {
			cfg.Plugins = map[string]model.PluginSettings{}
		}
		cfg.Plugins[tasksID] = model.PluginSettings{Enabled: &enabled}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tasksRPC(t *testing.T, c *Controller, method string, params any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, protocolErr := c.PluginCall(ctx, tasksID, method, raw)
	if protocolErr != nil {
		t.Fatalf("%s: %s: %s", method, protocolErr.Code, protocolErr.Message)
	}
	out := map[string]any{}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("%s result %s: %v", method, result, err)
	}
	return out
}

func tasksOK(t *testing.T, c *Controller, method string, params any) map[string]any {
	t.Helper()
	out := tasksRPC(t, c, method, params)
	if e, failed := out["error"]; failed {
		t.Fatalf("%s failed: %v", method, e)
	}
	return out
}

func tasksErr(t *testing.T, c *Controller, method string, params any) map[string]any {
	t.Helper()
	out := tasksRPC(t, c, method, params)
	e, failed := out["error"].(map[string]any)
	if !failed {
		t.Fatalf("%s unexpectedly succeeded: %v", method, out)
	}
	return e
}

func helperTask(name, mode string) map[string]any {
	return map[string]any{
		"name": name,
		"command": map[string]any{
			"path":        os.Args[0],
			"args":        []string{"-test.run=^TestTasksHelperProcess$"},
			"environment": map[string]string{"RUNPILOT_TASKS_HELPER": mode},
			"interpreter": "direct",
		},
	}
}

func continuous(name, mode string, restart map[string]any, autostart bool) map[string]any {
	task := helperTask(name, mode)
	task["type"], task["restart"], task["autostart"] = "continuous", restart, autostart
	return task
}

func scheduled(name, mode string, seconds int, enabled bool) map[string]any {
	task := helperTask(name, mode)
	task["type"], task["enabled"] = "scheduled", enabled
	task["schedule"] = map[string]any{"type": "interval", "intervalSeconds": seconds}
	return task
}

func createTaskRPC(t *testing.T, c *Controller, task map[string]any) string {
	t.Helper()
	return tasksOK(t, c, "tasks.create", map[string]any{"task": task})["task"].(map[string]any)["id"].(string)
}

func taskStatus(t *testing.T, c *Controller, id string) map[string]any {
	t.Helper()
	for _, item := range tasksOK(t, c, "tasks.list", map[string]any{})["tasks"].([]any) {
		view := item.(map[string]any)
		if view["task"].(map[string]any)["id"] == id {
			return view["status"].(map[string]any)
		}
	}
	t.Fatalf("task %s not listed", id)
	return nil
}

// executions reads history directly (not through the plugin) so a test can
// observe event-driven completion without triggering the plugin's reconcile.
func executions(t *testing.T, c *Controller, id string) []map[string]any {
	t.Helper()
	list, err := c.history.ListExecutions(tasksID, id, 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(list)
	var out []map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func waitUntil(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func finishedCount(list []map[string]any) int {
	n := 0
	for _, e := range list {
		if e["finishedAt"] != nil {
			n++
		}
	}
	return n
}

func TestTasksHelperProcess(t *testing.T) {
	mode := os.Getenv("RUNPILOT_TASKS_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "exit0":
		os.Stdout.WriteString("done\n")
		os.Exit(0)
	case "exit1":
		os.Stdout.WriteString("failing\n")
		os.Stderr.WriteString("boom\n")
		os.Exit(1)
	case "output":
		os.Stdout.WriteString("out-line\n")
		os.Stderr.WriteString("err-line\n")
		os.Exit(0)
	case "flood":
		line := strings.Repeat("x", 1023) + "\n"
		for i := 0; i < 4096; i++ {
			os.Stdout.WriteString(line)
		}
		os.Exit(0)
	default: // wait
		os.Stdout.WriteString("waiting\n")
		select {}
	}
}

func TestTasksWASMIsCurrent(t *testing.T) {
	tinygo := os.Getenv("TINYGO")
	if tinygo == "" {
		var err error
		if tinygo, err = exec.LookPath("tinygo"); err != nil {
			t.Skip("TinyGo is not installed")
		}
	}
	dir := filepath.Join("..", "..", "plugins", tasksID, "backend")
	built := filepath.Join(t.TempDir(), "plugin.wasm")
	cmd := exec.Command(tinygo, "build", "-target=wasm-unknown", "-tags=runpilot_wasm", "-scheduler=none", "-gc=conservative", "-stack-size=64kb", "-no-debug", "-o", built, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	want, _ := os.ReadFile(built)
	got, err := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
	if err != nil || string(got) != string(want) {
		t.Fatal("plugins/tasks/backend/plugin.wasm is stale; run go generate ./plugins/tasks/backend")
	}
}

func TestTasksPluginCRUDAndScheduleSurviveControllerRestart(t *testing.T) {
	dataDir := t.TempDir()
	c := openTasksController(t, dataDir)
	on := createTaskRPC(t, c, scheduled("on", "exit0", 3600, true))
	off := createTaskRPC(t, c, scheduled("off", "exit0", 3600, false))
	cont := createTaskRPC(t, c, continuous("cont", "wait", map[string]any{"mode": "never"}, false))
	update := scheduled("off renamed", "exit0", 3600, false)
	update["id"] = off
	tasksOK(t, c, "tasks.update", map[string]any{"task": update})
	tasksOK(t, c, "tasks.delete", map[string]any{"id": cont})
	c.Close()

	c = openTasksController(t, dataDir)
	defer c.Close()
	tasks := tasksOK(t, c, "tasks.list", map[string]any{})["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("tasks after restart = %v", tasks)
	}
	registered := map[string]bool{}
	for _, item := range c.scheduler.ListPlugin(tasksID) {
		registered[item.ID] = true
	}
	if !registered[on] || registered[off] || registered[cont] {
		t.Fatalf("registered schedules = %v", registered)
	}
	if got := tasksOK(t, c, "tasks.create", map[string]any{"task": scheduled("next", "exit0", 3600, false)})["task"].(map[string]any)["id"]; got != "task-4" {
		t.Fatalf("ID counter after restart = %v", got)
	}
}

func TestTasksPluginScheduleFiresAndCapturesOutput(t *testing.T) {
	c := openTasksController(t, t.TempDir())
	defer c.Close()
	events, unsubscribe := c.SubscribePluginEvents()
	defer unsubscribe()
	id := createTaskRPC(t, c, scheduled("tick", "output", 1, true))
	waitUntil(t, 8*time.Second, "scheduled run", func() bool { return finishedCount(executions(t, c, id)) >= 1 })
	sawCompleted, sawOutput := false, false
	drain := time.After(500 * time.Millisecond)
	for !(sawCompleted && sawOutput) {
		select {
		case event := <-events:
			sawCompleted = sawCompleted || event.Event == "tasks.run.completed"
			sawOutput = sawOutput || event.Event == "history.output"
		case <-drain:
			t.Fatalf("browser events: completed=%v output=%v", sawCompleted, sawOutput)
		}
	}
	list := executions(t, c, id)
	runID := list[len(list)-1]["id"].(string)
	out := tasksOK(t, c, "tasks.output", map[string]any{"runId": runID})
	text := out["output"].(string)
	if !strings.Contains(text, "out-line") || !strings.Contains(text, "err-line") {
		t.Fatalf("captured output = %q", text)
	}
	if out["run"].(map[string]any)["success"] != true {
		t.Fatalf("run = %v", out["run"])
	}
	// Disabling stops further runs.
	update := scheduled("tick", "output", 1, false)
	update["id"] = id
	tasksOK(t, c, "tasks.update", map[string]any{"task": update})
	time.Sleep(200 * time.Millisecond)
	count := len(executions(t, c, id))
	time.Sleep(1600 * time.Millisecond)
	if got := len(executions(t, c, id)); got != count {
		t.Fatalf("disabled schedule still fired: %d -> %d", count, got)
	}
	if status := taskStatus(t, c, id); status["state"] != "idle" || status["lastSuccess"] != true {
		t.Fatalf("status = %v", status)
	}
}

func TestTasksPluginRunNowOverlapAndTimeout(t *testing.T) {
	c := openTasksController(t, t.TempDir())
	defer c.Close()
	task := scheduled("slow", "wait", 3600, false)
	task["timeoutSeconds"] = 1
	id := createTaskRPC(t, c, task)
	tasksOK(t, c, "tasks.run", map[string]any{"id": id})
	if e := tasksErr(t, c, "tasks.run", map[string]any{"id": id}); e["code"] != "already_running" {
		t.Fatalf("overlap = %v", e)
	}
	waitUntil(t, 6*time.Second, "timeout", func() bool { return finishedCount(executions(t, c, id)) == 1 })
	status := taskStatus(t, c, id)
	if status["state"] != "failure" || status["message"] != "timed out after 1 seconds" {
		t.Fatalf("status after timeout = %v", status)
	}
	tasksOK(t, c, "tasks.run", map[string]any{"id": id}) // accepted again after completion.
	tasksOK(t, c, "tasks.stop", map[string]any{"id": id})
	waitUntil(t, 5*time.Second, "stop", func() bool { return finishedCount(executions(t, c, id)) == 2 })
}

func TestTasksPluginContinuousAutostartRestartAndMaxRetries(t *testing.T) {
	dataDir := t.TempDir()
	c := openTasksController(t, dataDir)
	restart := map[string]any{"mode": "on-failure", "initialDelaySeconds": 1, "maxDelaySeconds": 2, "maxRetries": 2}
	id := createTaskRPC(t, c, continuous("flaky", "exit1", restart, true))
	if got := len(executions(t, c, id)); got != 0 {
		t.Fatalf("created task started immediately: %d", got)
	}
	c.Close()

	// Autostart happens while the plugin initializes; its first exit callback
	// can arrive before initialization has finished.
	c = openTasksController(t, dataDir)
	defer c.Close()
	waitUntil(t, 10*time.Second, "autostart, two restarts, then stop", func() bool {
		list := executions(t, c, id)
		return len(list) == 3 && finishedCount(list) == 3
	})
	time.Sleep(1500 * time.Millisecond)
	if got := len(executions(t, c, id)); got != 3 {
		t.Fatalf("restarted beyond max retries: %d", got)
	}
	status := taskStatus(t, c, id)
	if status["state"] != "stopped" || status["lastExitCode"] != float64(1) || !strings.Contains(status["message"].(string), "restart limit") {
		t.Fatalf("status = %v", status)
	}
	out := tasksOK(t, c, "tasks.output", map[string]any{"runId": executions(t, c, id)[0]["id"]})
	if !strings.Contains(out["output"].(string), "failing") || !strings.Contains(out["output"].(string), "boom") {
		t.Fatalf("output = %v", out["output"])
	}
}

func TestTasksPluginManualStopPreventsRestart(t *testing.T) {
	c := openTasksController(t, t.TempDir())
	defer c.Close()
	id := createTaskRPC(t, c, continuous("svc", "wait", map[string]any{"mode": "always", "initialDelaySeconds": 1}, false))
	tasksOK(t, c, "tasks.start", map[string]any{"id": id})
	if status := taskStatus(t, c, id); status["state"] != "running" || status["pid"] == nil {
		t.Fatalf("running status = %v", status)
	}
	tasksOK(t, c, "tasks.stop", map[string]any{"id": id})
	waitUntil(t, 5*time.Second, "stop", func() bool { return finishedCount(executions(t, c, id)) == 1 })
	time.Sleep(2200 * time.Millisecond)
	if got := len(executions(t, c, id)); got != 1 {
		t.Fatalf("stopped task was restarted: %d executions", got)
	}
	if status := taskStatus(t, c, id); status["state"] != "stopped" {
		t.Fatalf("status = %v", status)
	}
	// restart on a stopped task starts it; on a running task it replaces it.
	tasksOK(t, c, "tasks.restart", map[string]any{"id": id})
	tasksOK(t, c, "tasks.restart", map[string]any{"id": id})
	waitUntil(t, 5*time.Second, "restart", func() bool {
		list := executions(t, c, id)
		return len(list) == 3 && finishedCount(list) == 2
	})
	if status := taskStatus(t, c, id); status["state"] != "running" {
		t.Fatalf("after restart = %v", status)
	}
}

func TestTasksPluginProcessStartFailureIsReported(t *testing.T) {
	c := openTasksController(t, t.TempDir())
	defer c.Close()
	task := continuous("bad", "wait", map[string]any{"mode": "never"}, false)
	task["command"].(map[string]any)["path"] = filepath.Join(t.TempDir(), "does-not-exist")
	id := createTaskRPC(t, c, task)
	e := tasksErr(t, c, "tasks.start", map[string]any{"id": id})
	if !strings.Contains(e["message"].(string), "start") {
		t.Fatalf("error = %v", e)
	}
	list := executions(t, c, id)
	if len(list) != 1 || list[0]["success"] != false || list[0]["finishedAt"] == nil {
		t.Fatalf("history = %v", list)
	}
	if runtime.GOOS != "windows" {
		task["command"].(map[string]any)["interpreter"] = "cmd" // Windows-only interpreter.
		task["command"].(map[string]any)["path"] = "x"
		task["id"] = id
		tasksOK(t, c, "tasks.update", map[string]any{"task": task})
		if e := tasksErr(t, c, "tasks.start", map[string]any{"id": id}); !strings.Contains(e["message"].(string), "cmd interpreter") {
			t.Fatalf("interpreter error = %v", e)
		}
	}
}

func TestTasksPluginInterpretersUseLauncherSemantics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	c := openTasksController(t, t.TempDir())
	defer c.Close()
	inline := map[string]any{"name": "inline", "type": "scheduled", "schedule": map[string]any{"type": "interval", "intervalSeconds": 3600},
		"command": map[string]any{"path": `"echo $GREETING from sh"`, "interpreter": "sh-inline", "environment": map[string]string{"GREETING": "hello"}}}
	id := createTaskRPC(t, c, inline)
	tasksOK(t, c, "tasks.run", map[string]any{"id": id})
	waitUntil(t, 5*time.Second, "inline run", func() bool { return finishedCount(executions(t, c, id)) == 1 })
	out := tasksOK(t, c, "tasks.output", map[string]any{"runId": executions(t, c, id)[0]["id"]})
	if !strings.Contains(out["output"].(string), "hello from sh") {
		t.Fatalf("output = %q", out["output"])
	}
	// An empty interpreter is "auto": a .sh path runs through sh.
	script := filepath.Join(t.TempDir(), "job.sh")
	if err := os.WriteFile(script, []byte("echo auto-detected $1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auto := map[string]any{"name": "auto", "type": "scheduled", "schedule": map[string]any{"type": "interval", "intervalSeconds": 3600},
		"command": map[string]any{"path": script, "args": []string{"arg1"}}}
	id = createTaskRPC(t, c, auto)
	tasksOK(t, c, "tasks.run", map[string]any{"id": id})
	waitUntil(t, 5*time.Second, "auto run", func() bool { return finishedCount(executions(t, c, id)) == 1 })
	out = tasksOK(t, c, "tasks.output", map[string]any{"runId": executions(t, c, id)[0]["id"]})
	if !strings.Contains(out["output"].(string), "auto-detected arg1") {
		t.Fatalf("output = %q", out["output"])
	}
}

func processAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

func TestTasksPluginShutdownStopsRunningProcessesAndAutostartsAgain(t *testing.T) {
	dataDir := t.TempDir()
	c := openTasksController(t, dataDir)
	id := createTaskRPC(t, c, continuous("svc", "wait", map[string]any{"mode": "always", "initialDelaySeconds": 1}, true))
	tasksOK(t, c, "tasks.start", map[string]any{"id": id})
	pid := int(taskStatus(t, c, id)["pid"].(float64))
	if !processAlive(pid) && runtime.GOOS != "windows" {
		t.Fatal("process is not running")
	}
	c.Close()
	if runtime.GOOS != "windows" {
		waitUntil(t, 3*time.Second, "child reaped", func() bool { return !processAlive(pid) })
	}

	c = openTasksController(t, dataDir)
	defer c.Close()
	list := executions(t, c, id)
	if len(list) < 2 || list[len(list)-1]["finishedAt"] == nil {
		t.Fatalf("history after restart = %v", list)
	}
	if message, _ := list[len(list)-1]["message"].(string); !strings.Contains(message, "shutting down") {
		t.Fatalf("shutdown not recorded: %v", list[len(list)-1])
	}
	waitUntil(t, 5*time.Second, "autostart after restart", func() bool { return taskStatus(t, c, id)["state"] == "running" })
}

func TestTasksPluginMalformedStoredStateIsNotDestroyed(t *testing.T) {
	dataDir := t.TempDir()
	stored := `{"version":1,"tasks":[{"id":"task-1","name":`
	path := filepath.Join(dataDir, "plugins", tasksID, "data", "storage.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"tasks":`+strconvQuote(stored)+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := openTasksController(t, dataDir)
	list := tasksOK(t, c, "tasks.list", map[string]any{})
	if !strings.Contains(list["loadError"].(string), "unreadable") {
		t.Fatalf("list = %v", list)
	}
	if e := tasksErr(t, c, "tasks.create", map[string]any{"task": scheduled("x", "exit0", 60, false)}); e["code"] != "state_unreadable" {
		t.Fatalf("create = %v", e)
	}
	c.Close()
	after, _ := os.ReadFile(path)
	if string(after) != `{"tasks":`+strconvQuote(stored)+`}` {
		t.Fatalf("stored state changed: %s", after)
	}
}

func strconvQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func TestTasksPluginConcurrentRunsSettleWithoutLostExits(t *testing.T) {
	c := openTasksController(t, t.TempDir())
	defer c.Close()
	task := scheduled("burst", "exit0", 3600, false)
	task["overlapPolicy"] = "allow"
	id := createTaskRPC(t, c, task)
	flood := scheduled("flood", "flood", 3600, false)
	flood["overlapPolicy"] = "allow"
	floodID := createTaskRPC(t, c, flood)
	const runs = 24
	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := id
			if i%6 == 0 {
				target = floodID
			}
			tasksOK(t, c, "tasks.run", map[string]any{"id": target})
		}(i)
	}
	wg.Wait()
	waitUntil(t, 20*time.Second, "all runs to settle through events", func() bool {
		a, b := executions(t, c, id), executions(t, c, floodID)
		return len(a)+len(b) == runs && finishedCount(a)+finishedCount(b) == runs
	})
	for _, target := range []string{id, floodID} {
		if status := taskStatus(t, c, target); status["state"] != "idle" || status["runningCount"] != nil {
			t.Fatalf("status = %v", status)
		}
	}
	out := tasksOK(t, c, "tasks.output", map[string]any{"runId": executions(t, c, floodID)[0]["id"], "maxBytes": 2048})
	if out["truncated"] != true || len(out["output"].(string)) > 2048 || out["size"].(float64) < 4096*1024 {
		t.Fatalf("flood output truncated=%v len=%d size=%v", out["truncated"], len(out["output"].(string)), out["size"])
	}
}

func TestTasksPluginDoesNotAffectLegacyTasksOrHistory(t *testing.T) {
	c := openTasksController(t, t.TempDir())
	defer c.Close()
	id := createTaskRPC(t, c, scheduled("plugin task", "exit0", 3600, false))
	tasksOK(t, c, "tasks.run", map[string]any{"id": id})
	waitUntil(t, 5*time.Second, "plugin run", func() bool { return finishedCount(executions(t, c, id)) == 1 })
	job := model.JobDefinition{Name: "legacy", Enabled: false, Type: model.JobCommand, Command: &model.CommandSpec{Path: os.Args[0], Args: []string{"-test.run=^TestTasksHelperProcess$"}, Environment: map[string]string{"RUNPILOT_TASKS_HELPER": "exit0"}, Interpreter: "direct"}, Schedule: model.ScheduleSpec{Type: model.ScheduleInterval, IntervalSeconds: 3600}}
	saved, err := c.UpsertJob(job)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := c.RunJob(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "legacy run", func() bool {
		runs, _ := c.RecentRuns(saved.ID, 10)
		return len(runs) == 1 && runs[0].FinishedAt != nil
	})
	all, err := c.RecentRuns("", 100)
	if err != nil || len(all) != 1 || all[0].ID != runID {
		t.Fatalf("legacy history must not include plugin executions: %#v %v", all, err)
	}
	if log, err := c.RunLog(executions(t, c, id)[0]["id"].(string), 10); err == nil {
		t.Fatalf("legacy run log endpoint exposed a plugin execution: %q", log)
	}
	if len(c.JobViews()) != 1 || len(c.ProcessViews()) != 0 {
		t.Fatalf("legacy views changed: %d jobs %d processes", len(c.JobViews()), len(c.ProcessViews()))
	}
}
