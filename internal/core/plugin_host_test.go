package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/history"
	"github.com/szilab/RunPilot/internal/plugins"
)

type recordedPluginEvent struct {
	owner, name string
	data        map[string]any
}

func newCapturingProcessManager(t *testing.T) (*pluginProcessManager, *history.Store, chan recordedPluginEvent, chan recordedPluginEvent) {
	t.Helper()
	store, err := history.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events := make(chan recordedPluginEvent, 64)
	published := make(chan recordedPluginEvent, 64)
	m := newPluginProcessManager(func(owner, name string, data any) {
		item := recordedPluginEvent{owner: owner, name: name}
		item.data, _ = data.(map[string]any)
		events <- item
	})
	m.history = store
	m.publish = func(owner, name string, data any) {
		item := recordedPluginEvent{owner: owner, name: name}
		item.data, _ = data.(map[string]any)
		published <- item
	}
	return m, store, events, published
}

func helperStart(mode string) pluginProcessStart {
	return pluginProcessStart{Command: os.Args[0], Args: []string{"-test.run=^TestPluginProcessHelper$", "--"}, Environment: map[string]string{"RUNPILOT_PLUGIN_HELPER": mode}}
}

func waitExit(t *testing.T, events <-chan recordedPluginEvent) recordedPluginEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if event.name == "process.exit" {
				return event
			}
		case <-deadline:
			t.Fatal("no process.exit event")
		}
	}
}

func isHostFailure(err error, code string) bool {
	var failure *plugins.HostFailure
	return errors.As(err, &failure) && failure.Code == code
}

func TestPluginProcessCapturesOutputIntoOwnedHistory(t *testing.T) {
	m, store, events, published := newCapturingProcessManager(t)
	execution, err := store.BeginExecution("tasks", "process", "task-1", "One")
	if err != nil {
		t.Fatal(err)
	}
	input := helperStart("output")
	input.HistoryID = execution.ID
	status, err := m.start("tasks", input)
	if err != nil || status.PID <= 0 {
		t.Fatalf("start = %#v, %v", status, err)
	}
	exit := waitExit(t, events)
	if exit.data["historyId"] != execution.ID || exit.data["success"] != true || exit.data["timedOut"] != false {
		t.Fatalf("exit = %#v", exit.data)
	}
	text, _, _, err := store.ReadExecutionOutput("tasks", execution.ID, 0)
	if err != nil || !strings.Contains(text, "hello") {
		t.Fatalf("captured = %q, %v", text, err)
	}
	select {
	case event := <-published:
		if event.owner != "tasks" || event.name != "history.output" || event.data["id"] != execution.ID {
			t.Fatalf("browser event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("no history.output browser event")
	}
	// Captured output is not also delivered to the plugin.
	for len(events) > 0 {
		if event := <-events; strings.HasPrefix(event.name, "process.std") {
			t.Fatalf("captured process delivered %s", event.name)
		}
	}
	if m.captured["tasks\x00"+execution.ID] {
		t.Fatal("capture slot not released")
	}
}

func TestPluginProcessCaptureRejectsForeignFinishedUnknownAndBusyExecutions(t *testing.T) {
	m, store, events, _ := newCapturingProcessManager(t)
	own, _ := store.BeginExecution("tasks", "process", "t", "l")
	foreign, _ := store.BeginExecution("other", "process", "t", "l")
	finished, _ := store.BeginExecution("tasks", "process", "t", "l")
	_, _ = store.FinishExecution("tasks", finished.ID, nil, nil, "")
	for name, id := range map[string]string{"foreign": foreign.ID, "finished": finished.ID, "unknown": "exec-nope", "traversal": "../x"} {
		input := helperStart("wait")
		input.HistoryID = id
		if _, err := m.start("tasks", input); !isHostFailure(err, "invalid_argument") {
			t.Errorf("%s = %v", name, err)
		}
	}
	if len(m.items) != 0 {
		t.Fatal("a rejected start left a process running")
	}
	input := helperStart("wait")
	input.HistoryID = own.ID
	first, err := m.start("tasks", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.start("tasks", input); !isHostFailure(err, "invalid_argument") {
		t.Fatalf("second capture of one execution = %v", err)
	}
	if _, err := m.terminate("tasks", first.ID); err != nil {
		t.Fatal(err)
	}
	waitExit(t, events)
}

func TestPluginProcessTimeoutKillsTreeAndReports(t *testing.T) {
	m, _, events, _ := newCapturingProcessManager(t)
	input := helperStart("wait")
	input.TimeoutSeconds = 1
	started := time.Now()
	status, err := m.start("tasks", input)
	if err != nil {
		t.Fatal(err)
	}
	exit := waitExit(t, events)
	if exit.data["timedOut"] != true || exit.data["success"] != false || time.Since(started) > 4*time.Second {
		t.Fatalf("exit = %#v after %v", exit.data, time.Since(started))
	}
	after, err := m.status("tasks", status.ID)
	if err != nil || after.Running || !after.TimedOut {
		t.Fatalf("status = %#v, %v", after, err)
	}
}

func TestPluginProcessInterpreterAndValidation(t *testing.T) {
	m, _, events, _ := newCapturingProcessManager(t)
	if runtime.GOOS != "windows" {
		if _, err := m.start("tasks", pluginProcessStart{Command: "echo hi", Interpreter: "sh-inline"}); err != nil {
			t.Fatalf("sh-inline: %v", err)
		}
		if exit := waitExit(t, events); exit.data["success"] != true {
			t.Fatalf("sh-inline exit = %#v", exit.data)
		}
		if _, err := m.start("tasks", pluginProcessStart{Command: "x", Interpreter: "cmd"}); !isHostFailure(err, "invalid_argument") {
			t.Fatalf("cmd on unix = %v", err)
		}
	}
	tooMany := make([]string, maxPluginProcessArgs+1)
	for name, input := range map[string]pluginProcessStart{
		"empty":       {},
		"interpreter": {Command: "x", Interpreter: "perl"},
		"timeout":     {Command: "x", TimeoutSeconds: -1},
		"huge":        {Command: "x", TimeoutSeconds: maxPluginProcessTimeoutSeconds + 1},
		"args":        {Command: "x", Args: tooMany},
		"env":         {Command: "x", Environment: map[string]string{"A=B": "1"}},
		"long":        {Command: strings.Repeat("x", maxPluginProcessString+1)},
	} {
		if _, err := m.start("tasks", input); !isHostFailure(err, "invalid_argument") {
			t.Errorf("%s = %v", name, err)
		}
	}
	// The default is still a direct executable, as before interpreters existed.
	if _, err := m.start("tasks", pluginProcessStart{Command: "echo hi"}); err == nil {
		t.Fatal("empty interpreter must mean direct: a shell line is not an executable")
	}
}

func TestPluginProcessStatusReportsPIDAndOwnership(t *testing.T) {
	m, _, events, _ := newCapturingProcessManager(t)
	status, err := m.start("tasks", helperStart("wait"))
	if err != nil {
		t.Fatal(err)
	}
	running, err := m.status("tasks", status.ID)
	if err != nil || !running.Running || running.PID != status.PID || status.PID <= 0 {
		t.Fatalf("status = %#v, %v", running, err)
	}
	if _, err := m.status("other", status.ID); !isHostFailure(err, "not_found") {
		t.Fatalf("foreign status = %v", err)
	}
	if _, err := m.terminate("other", status.ID); !isHostFailure(err, "not_found") {
		t.Fatalf("foreign terminate = %v", err)
	}
	m.stopOwner("tasks")
	waitExit(t, events)
	m.close()
}

func TestOutputNotifierCoalescesAndFlushesOnStop(t *testing.T) {
	var published []int64
	n := newOutputNotifier(func(size int64) { published = append(published, size) })
	for i := int64(1); i <= 50; i++ {
		n.notify(i)
	}
	n.mu.Lock()
	first := append([]int64(nil), published...)
	n.mu.Unlock()
	if len(first) != 1 || first[0] != 1 {
		t.Fatalf("burst published %v, want only the first immediately", first)
	}
	n.stop()
	if len(published) != 2 || published[1] != 50 {
		t.Fatalf("stop did not flush the latest size: %v", published)
	}
	n.notify(99)
	if len(published) != 2 {
		t.Fatal("notifier published after stop")
	}
}

func newHostTestController(t *testing.T) *Controller {
	t.Helper()
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestHistoryCapabilityIsOwnerScopedAndBounded(t *testing.T) {
	c := newHostTestController(t)
	host := controllerPluginHost{controller: c}
	call := func(owner, operation, params string) (map[string]any, error) {
		raw, err := host.History(context.Background(), owner, operation, json.RawMessage(params))
		if err != nil {
			return nil, err
		}
		out := map[string]any{}
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
		}
		return out, nil
	}
	begun, err := call("tasks", "begin", `{"kind":"command","subject":"task-1","label":"One"}`)
	if err != nil {
		t.Fatal(err)
	}
	id := begun["id"].(string)
	if _, err := call("tasks", "append", `{"id":"`+id+`","text":"hello\n"}`); err != nil {
		t.Fatal(err)
	}
	out, err := call("tasks", "output", `{"id":"`+id+`","maxBytes":3}`)
	if err != nil || out["output"] != "lo\n" || out["truncated"] != true {
		t.Fatalf("output = %#v, %v", out, err)
	}
	finished, err := call("tasks", "finish", `{"id":"`+id+`","exitCode":2,"success":false,"message":"exit code 2"}`)
	if err != nil || finished["exitCode"] != float64(2) || finished["finishedAt"] == nil {
		t.Fatalf("finish = %#v, %v", finished, err)
	}
	list, err := call("tasks", "list", `{"subject":"task-1","limit":5}`)
	if err != nil || len(list["executions"].([]any)) != 1 {
		t.Fatalf("list = %#v, %v", list, err)
	}
	for _, operation := range []string{"get", "output", "finish", "append"} {
		if _, err := call("other", operation, `{"id":"`+id+`"}`); !isHostFailure(err, "not_found") {
			t.Errorf("%s by another owner = %v", operation, err)
		}
	}
	if other, _ := call("other", "list", `{}`); len(other["executions"].([]any)) != 0 {
		t.Fatalf("other owner list = %#v", other)
	}
	for name, params := range map[string]string{"unknown field": `{"kind":"x","subject":"s","path":"/etc"}`, "bad subject": `{"kind":"x","subject":"../s"}`, "not object": `[]`} {
		if _, err := call("tasks", "begin", params); !isHostFailure(err, "invalid_argument") {
			t.Errorf("begin %s = %v", name, err)
		}
	}
	if _, err := call("tasks", "get", `{"id":"../../history.db"}`); !isHostFailure(err, "not_found") {
		t.Fatalf("path-like id = %v", err)
	}
	if _, err := call("tasks", "append", `{"id":"`+id+`","text":"`+strings.Repeat("x", history.MaxExecutionAppend+1)+`"}`); !isHostFailure(err, "invalid_argument") {
		t.Fatalf("oversize append = %v", err)
	}
	if _, err := call("../etc", "begin", `{"kind":"x","subject":"s"}`); !isHostFailure(err, "invalid_argument") {
		t.Fatalf("hostile owner = %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.dataDir, "plugins", "tasks", "data", "runs", id+".log")); err != nil {
		t.Fatalf("log not in owner directory: %v", err)
	}
}

func TestScheduleValidateUsesSchedulerRules(t *testing.T) {
	host := controllerPluginHost{controller: newHostTestController(t)}
	for schedule, ok := range map[string]bool{
		`{"type":"interval","intervalSeconds":60}`:                    true,
		`{"type":"daily","timeOfDay":"03:00","timeZone":"UTC"}`:       true,
		`{"type":"cron","cron":"*/5 * * * *"}`:                        true,
		`{"type":"interval","intervalSeconds":0}`:                     false,
		`{"type":"daily","timeOfDay":"25:00"}`:                        false,
		`{"type":"cron","cron":"nope"}`:                               false,
		`{"type":"daily","timeOfDay":"03:00","timeZone":"Mars/Base"}`: false,
		`{"type":"weekly"}`:                                           false,
	} {
		err := host.ScheduleValidate(context.Background(), json.RawMessage(`{"schedule":`+schedule+`}`))
		if (err == nil) != ok || (err != nil && !isHostFailure(err, "invalid_argument")) {
			t.Errorf("%s = %v", schedule, err)
		}
	}
}

func TestEventQueueDoesNotBlockProducersOrGrowWithoutBound(t *testing.T) {
	c := newHostTestController(t)
	c.loading["stuck"] = make(chan struct{}) // a backend that never finishes loading.
	started := time.Now()
	for i := 0; i < 5*pluginEventQueueSize; i++ {
		c.deliverPluginEvent("stuck", "process.stdout", map[string]any{"text": "x"})
	}
	if time.Since(started) > time.Second {
		t.Fatalf("output producers blocked for %v", time.Since(started))
	}
	if got := len(c.pluginEventQueue("stuck")); got > pluginEventQueueSize {
		t.Fatalf("queue length %d exceeds bound", got)
	}
}

func TestEventsWaitForLoadingRuntimeAndDropWhenAbsent(t *testing.T) {
	c := newHostTestController(t)
	ready := make(chan struct{})
	c.loading["p"] = ready
	result := make(chan *plugins.Runtime, 1)
	go func() { result <- c.awaitPluginRuntime("p") }()
	select {
	case <-result:
		t.Fatal("awaitPluginRuntime returned before loading finished")
	case <-time.After(100 * time.Millisecond):
	}
	c.runtimeMu.Lock()
	delete(c.loading, "p")
	c.runtimeMu.Unlock()
	close(ready)
	if got := <-result; got != nil {
		t.Fatalf("runtime = %v for a load that produced none", got)
	}
	if got := c.awaitPluginRuntime("unknown"); got != nil {
		t.Fatal("unknown plugin produced a runtime")
	}
}
