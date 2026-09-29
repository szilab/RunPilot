package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

const (
	stateVersion    = 1
	storageKey      = "tasks"
	restartPrefix   = "restart:"
	reconcileID     = "reconcile"
	reconcileEvery  = 30
	callbackTask    = "task"
	callbackRestart = "restart"
	callbackCheck   = "reconcile"

	stateStopped    = "stopped"
	stateRunning    = "running"
	stateStopping   = "stopping"
	stateRestarting = "restarting"
)

// callHost is replaced by tests; in the WASM module it is the ABI-v2 SDK.
var callHost = pluginapi.CallHost

type stateFile struct {
	Version int    `json:"version"`
	Next    int    `json:"next"`
	Tasks   []Task `json:"tasks"`
}

type rpcError struct{ Code, Message string }

func (e *rpcError) Error() string { return e.Message }
func rpcFail(code, message string) *rpcError {
	return &rpcError{Code: code, Message: message}
}

// hostFailure converts a capability error to an RPC error, keeping its stable
// code and message.
func hostFailure(err error) *rpcError {
	if capability, ok := pluginapi.AsCapabilityError(err); ok {
		return rpcFail(capability.Code, capability.Message)
	}
	return rpcFail("host_failure", err.Error())
}

func errText(err error) string {
	if capability, ok := pluginapi.AsCapabilityError(err); ok {
		return capability.Message
	}
	return err.Error()
}

func isNotFound(err error) bool {
	capability, ok := pluginapi.AsCapabilityError(err)
	return ok && capability.Code == "not_found"
}

// taskRuntime is in-memory state only. Nothing here is persisted: host
// processes never outlive the plugin, so it is rebuilt from definitions.
type taskRuntime struct {
	state        string
	desired      bool
	pendingStart bool
	restartTimer bool
	restartCount int
	processID    string
	pid          int
	active       int
	currentRunID string
	startedAt    string
	lastRunID    string
	lastRunAt    string
	lastExit     *int
	lastSuccess  *bool
	message      string
	err          string
}

type procRef struct {
	taskID, runID, startedAt string
	timeoutSeconds           int
}

type plugin struct {
	tasks     []*Task
	next      int
	loadError string
	rt        map[string]*taskRuntime
	procs     map[string]*procRef
}

func newPlugin() *plugin {
	return &plugin{next: 1, rt: map[string]*taskRuntime{}, procs: map[string]*procRef{}}
}

func (pl *plugin) find(id string) *Task {
	for _, t := range pl.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (pl *plugin) runtimeOf(id string) *taskRuntime {
	r := pl.rt[id]
	if r == nil {
		r = &taskRuntime{state: stateStopped}
		pl.rt[id] = r
	}
	return r
}

func (pl *plugin) publish(event string, data any) {
	_ = callHost("events.publish", map[string]any{"event": event, "data": data}, nil)
}

// ---- persistence ---------------------------------------------------------

// load reads persisted definitions. Unreadable state is never overwritten:
// the plugin stays read-only and reports loadError until an administrator
// repairs or removes plugin-data/tasks/storage.json.
func (pl *plugin) load() {
	var raw json.RawMessage
	if err := callHost("storage.get", map[string]any{"key": storageKey}, &raw); err != nil {
		pl.loadError = "could not read stored tasks: " + errText(err)
		return
	}
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var state stateFile
	if err := json.Unmarshal(raw, &state); err != nil {
		pl.loadError = "stored task configuration is unreadable: " + err.Error()
		return
	}
	if state.Version != stateVersion {
		pl.loadError = "stored task configuration has unsupported version " + strconv.Itoa(state.Version)
		return
	}
	seen := map[string]bool{}
	next := state.Next
	for i := range state.Tasks {
		t := state.Tasks[i]
		if t.ID == "" || seen[t.ID] {
			pl.loadError = "stored task configuration contains a missing or duplicate task ID"
			pl.tasks, pl.next = nil, 1
			return
		}
		seen[t.ID] = true
		if n, err := strconv.Atoi(strings.TrimPrefix(t.ID, "task-")); err == nil && n >= next {
			next = n + 1
		}
		normalizeTask(&t)
		pl.tasks = append(pl.tasks, &t)
	}
	if next < 1 {
		next = 1
	}
	pl.next = next
}

func (pl *plugin) save(tasks []*Task, next int) error {
	state := stateFile{Version: stateVersion, Next: next, Tasks: make([]Task, 0, len(tasks))}
	for _, t := range tasks {
		state.Tasks = append(state.Tasks, *t)
	}
	return callHost("storage.set", map[string]any{"key": storageKey, "value": state}, nil)
}

// ---- lifecycle -----------------------------------------------------------

func (pl *plugin) init() {
	pl.load()
	if pl.loadError == "" {
		for _, t := range pl.tasks {
			pl.restore(t)
		}
	}
	// Safety net for a lost process.exit callback; see reconcile.
	_ = pl.register(reconcileID, callbackCheck, &Schedule{Type: "interval", IntervalSeconds: reconcileEvery})
}

// restore registers schedules and autostarts continuous tasks. A task that
// fails to restore is marked with an error and left untouched on disk.
func (pl *plugin) restore(t *Task) {
	r := pl.runtimeOf(t.ID)
	pl.loadLastRun(t, r)
	if err := validateTask(t); err != nil {
		r.err = err.Error()
		return
	}
	switch t.Type {
	case typeScheduled:
		if t.Enabled {
			if err := pl.register(t.ID, callbackTask, t.Schedule); err != nil {
				r.err = "schedule not registered: " + errText(err)
			}
		}
	case typeContinuous:
		if t.Autostart {
			r.desired = true
			if err := pl.spawn(t, r); err != nil {
				r.err = "autostart failed: " + errText(err)
			}
		}
	}
}

func (pl *plugin) loadLastRun(t *Task, r *taskRuntime) {
	var list struct {
		Executions []struct {
			ID         string `json:"id"`
			StartedAt  string `json:"startedAt"`
			FinishedAt string `json:"finishedAt"`
			ExitCode   *int   `json:"exitCode"`
			Success    *bool  `json:"success"`
			Message    string `json:"message"`
		} `json:"executions"`
	}
	if err := callHost("history.list", map[string]any{"subject": t.ID, "limit": 1}, &list); err != nil || len(list.Executions) == 0 {
		return
	}
	e := list.Executions[0]
	r.lastRunID, r.lastRunAt, r.lastExit, r.lastSuccess = e.ID, e.StartedAt, e.ExitCode, e.Success
	if e.Success != nil && !*e.Success {
		r.message = e.Message
	}
}

// shutdown stops every process this plugin started and finalizes their
// executions; the host would kill them anyway, but this records why.
func (pl *plugin) shutdown() {
	for id, ref := range pl.procs {
		_ = callHost("process.terminate", map[string]any{"id": id}, nil)
		pl.finishExecution(ref.runID, nil, false, "stopped: RunPilot is shutting down")
	}
	pl.procs = map[string]*procRef{}
	for id, r := range pl.rt {
		if r.restartTimer {
			_ = callHost("scheduler.remove", map[string]any{"id": restartPrefix + id}, nil)
			r.restartTimer = false
		}
		r.desired = false
	}
}

// ---- schedules -----------------------------------------------------------

func (pl *plugin) register(id, callback string, s *Schedule) error {
	return callHost("scheduler.register", map[string]any{"id": id, "callback": callback, "schedule": s}, nil)
}

func (pl *plugin) unregister(id string) {
	_ = callHost("scheduler.remove", map[string]any{"id": id}, nil) // absent is fine.
}

// applySchedule makes the host registration match the task definition.
func (pl *plugin) applySchedule(t *Task) error {
	pl.unregister(t.ID)
	if t.Type == typeScheduled && t.Enabled {
		return pl.register(t.ID, callbackTask, t.Schedule)
	}
	return nil
}

func (pl *plugin) validateSchedule(t *Task) error {
	if t.Type != typeScheduled {
		return nil
	}
	return callHost("scheduler.validate", map[string]any{"schedule": t.Schedule}, nil)
}

func (pl *plugin) cancelRestartTimer(id string, r *taskRuntime) {
	if r.restartTimer {
		pl.unregister(restartPrefix + id)
		r.restartTimer = false
	}
}

// ---- executions ----------------------------------------------------------

type processStart struct {
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	WorkingDirectory string            `json:"workingDirectory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Interpreter      string            `json:"interpreter"`
	TimeoutSeconds   int               `json:"timeoutSeconds,omitempty"`
	HistoryID        string            `json:"historyId"`
}

func (pl *plugin) finishExecution(runID string, code *int, success bool, message string) {
	_ = callHost("history.finish", map[string]any{"id": runID, "exitCode": code, "success": success, "message": message}, nil)
}

// launch records an execution and starts its process. On failure the
// execution is finished with the error so history shows why nothing ran.
func (pl *plugin) launch(t *Task, r *taskRuntime) (*procRef, error) {
	kind := "process"
	if t.Type == typeScheduled {
		kind = "command"
	}
	var begin struct {
		ID        string `json:"id"`
		StartedAt string `json:"startedAt"`
	}
	if err := callHost("history.begin", map[string]any{"kind": kind, "subject": t.ID, "label": t.Name}, &begin); err != nil {
		return nil, err
	}
	interpreter := t.Command.Interpreter
	if interpreter == "" {
		interpreter = "auto"
	}
	var started struct {
		ID  string `json:"id"`
		PID int    `json:"pid"`
	}
	err := callHost("process.start", processStart{Command: t.Command.Path, Args: t.Command.Args, WorkingDirectory: t.Command.WorkingDirectory, Environment: t.Command.Environment, Interpreter: interpreter, TimeoutSeconds: t.TimeoutSeconds, HistoryID: begin.ID}, &started)
	if err != nil {
		message := errText(err)
		_ = callHost("history.append", map[string]any{"id": begin.ID, "text": "start error: " + message + "\n"}, nil)
		code := -1
		pl.finishExecution(begin.ID, &code, false, message)
		ok := false
		r.lastRunID, r.lastRunAt, r.lastExit, r.lastSuccess, r.message = begin.ID, begin.StartedAt, &code, &ok, message
		pl.publish("tasks.run.completed", map[string]any{"id": t.ID, "name": t.Name, "runId": begin.ID, "exitCode": code, "success": false, "message": message})
		return nil, err
	}
	ref := &procRef{taskID: t.ID, runID: begin.ID, startedAt: begin.StartedAt, timeoutSeconds: t.TimeoutSeconds}
	pl.procs[started.ID] = ref
	r.processID, r.pid, r.currentRunID, r.startedAt = started.ID, started.PID, begin.ID, begin.StartedAt
	r.message = ""
	return ref, nil
}

// spawn starts a continuous task's process.
func (pl *plugin) spawn(t *Task, r *taskRuntime) error {
	if _, err := pl.launch(t, r); err != nil {
		r.state, r.desired = stateStopped, false
		r.processID, r.pid, r.currentRunID, r.startedAt = "", 0, "", ""
		pl.publishStatus(t)
		return err
	}
	r.state = stateRunning
	pl.publishStatus(t)
	return nil
}

type exitInfo struct {
	code       int
	success    bool
	terminated bool
	timedOut   bool
}

func exitMessage(ref *procRef, info exitInfo) string {
	switch {
	case info.timedOut:
		return "timed out after " + strconv.Itoa(ref.timeoutSeconds) + " seconds"
	case info.terminated:
		return "stopped"
	case !info.success:
		return "exit code " + strconv.Itoa(info.code)
	}
	return ""
}

// processExited is idempotent: the host event, a reconcile pass and shutdown
// may all observe the same exit, and only the first one has an effect.
func (pl *plugin) processExited(processID string, info exitInfo) {
	ref := pl.procs[processID]
	if ref == nil {
		return
	}
	delete(pl.procs, processID)
	message := exitMessage(ref, info)
	success := info.success && !info.timedOut
	code := info.code
	pl.finishExecution(ref.runID, &code, success, message)
	t, r := pl.find(ref.taskID), pl.rt[ref.taskID]
	name := ""
	if t != nil {
		name = t.Name
	}
	pl.publish("tasks.run.completed", map[string]any{"id": ref.taskID, "name": name, "runId": ref.runID, "exitCode": code, "success": success, "message": message})
	if t == nil || r == nil {
		return
	}
	r.lastRunID, r.lastRunAt, r.lastExit, r.lastSuccess = ref.runID, ref.startedAt, &code, &success
	if success {
		r.message = ""
	} else {
		r.message = message
	}
	if t.Type == typeScheduled {
		if r.active > 0 {
			r.active--
		}
		if r.active == 0 {
			r.currentRunID, r.startedAt, r.processID, r.pid = "", "", "", 0
		}
		pl.publishStatus(t)
		return
	}
	if r.processID != processID {
		return
	}
	pl.continuousExited(t, r, info, success)
}

func (pl *plugin) continuousExited(t *Task, r *taskRuntime, info exitInfo, success bool) {
	r.processID, r.pid, r.currentRunID, r.startedAt = "", 0, "", ""
	r.state = stateStopped
	if r.pendingStart {
		r.pendingStart, r.restartCount, r.desired = false, 0, true
		_ = pl.spawn(t, r)
		return
	}
	policy := t.Restart
	restart := r.desired && policy != nil && (policy.Mode == restartAlways || (policy.Mode == restartOnFailure && !success))
	if !restart {
		pl.publishStatus(t)
		return
	}
	if policy.MaxRetries > 0 && r.restartCount >= policy.MaxRetries {
		r.desired = false
		r.message = "restart limit reached (" + strconv.Itoa(policy.MaxRetries) + ")"
		pl.publishStatus(t)
		return
	}
	delay := restartDelay(policy, r.restartCount)
	r.restartCount++
	// The host scheduler is the only timer a WASM backend has; an interval
	// registration removed on its first fire is a one-shot delay.
	if err := pl.register(restartPrefix+t.ID, callbackRestart, &Schedule{Type: "interval", IntervalSeconds: delay}); err != nil {
		r.desired = false
		r.message = "restart not scheduled: " + errText(err)
		pl.publishStatus(t)
		return
	}
	r.restartTimer = true
	r.state = stateRestarting
	pl.publishStatus(t)
}

func (pl *plugin) restartFired(taskID string) {
	r := pl.rt[taskID]
	pl.unregister(restartPrefix + taskID)
	t := pl.find(taskID)
	if r == nil || t == nil {
		return
	}
	r.restartTimer = false
	if r.state != stateRestarting || !r.desired {
		return
	}
	_ = pl.spawn(t, r)
}

// reconcile detects exits whose callback was lost or delayed (queue
// overflow, a callback timeout). It is cheap when nothing is running.
func (pl *plugin) reconcile() {
	ids := make([]string, 0, len(pl.procs))
	for id := range pl.procs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var status struct {
			Running    bool `json:"running"`
			ExitCode   *int `json:"exitCode"`
			Terminated bool `json:"terminated"`
			TimedOut   bool `json:"timedOut"`
		}
		if err := callHost("process.status", map[string]any{"id": id}, &status); err != nil {
			if isNotFound(err) {
				pl.processExited(id, exitInfo{code: -1})
			}
			continue
		}
		if status.Running {
			continue
		}
		code := -1
		if status.ExitCode != nil {
			code = *status.ExitCode
		}
		pl.processExited(id, exitInfo{code: code, success: code == 0 && !status.TimedOut, terminated: status.Terminated, timedOut: status.TimedOut})
	}
}

// ---- control -------------------------------------------------------------

// terminate asks the host to stop a process and settles immediately when it
// had already exited.
func (pl *plugin) terminate(processID string) error {
	var status struct {
		Running    bool `json:"running"`
		ExitCode   *int `json:"exitCode"`
		Terminated bool `json:"terminated"`
		TimedOut   bool `json:"timedOut"`
	}
	if err := callHost("process.terminate", map[string]any{"id": processID}, &status); err != nil {
		if isNotFound(err) {
			pl.processExited(processID, exitInfo{code: -1})
			return nil
		}
		return err
	}
	if !status.Running {
		code := -1
		if status.ExitCode != nil {
			code = *status.ExitCode
		}
		pl.processExited(processID, exitInfo{code: code, success: code == 0 && !status.TimedOut, terminated: status.Terminated, timedOut: status.TimedOut})
	}
	return nil
}

func (pl *plugin) stopTask(t *Task, r *taskRuntime, keepPending bool) error {
	r.desired = false
	if !keepPending {
		r.pendingStart = false
	}
	pl.cancelRestartTimer(t.ID, r)
	if t.Type == typeScheduled {
		for id, ref := range pl.procs {
			if ref.taskID == t.ID {
				if err := pl.terminate(id); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if r.processID == "" {
		r.state = stateStopped
		pl.publishStatus(t)
		return nil
	}
	previous := r.state
	r.state = stateStopping
	pl.publishStatus(t)
	if err := pl.terminate(r.processID); err != nil {
		r.state = previous
		pl.publishStatus(t)
		return err
	}
	return nil
}

func (pl *plugin) startTask(t *Task, r *taskRuntime) error {
	if r.state == stateRunning || r.state == stateStopping {
		return rpcFail("already_running", "task \""+t.Name+"\" is already "+r.state)
	}
	pl.cancelRestartTimer(t.ID, r)
	r.desired, r.restartCount, r.pendingStart = true, 0, false
	r.err = ""
	return pl.spawn(t, r)
}

func (pl *plugin) runNow(t *Task, r *taskRuntime, manual bool) (string, error) {
	if r.active > 0 && t.OverlapPolicy != overlapAllow {
		if manual {
			return "", rpcFail("already_running", "task \""+t.Name+"\" is already running")
		}
		return "", nil
	}
	ref, err := pl.launch(t, r)
	if err != nil {
		pl.publishStatus(t)
		return "", err
	}
	r.active++
	r.state = stateRunning
	pl.publishStatus(t)
	return ref.runID, nil
}

// ---- views ---------------------------------------------------------------

type Status struct {
	State        string `json:"state"`
	PID          int    `json:"pid,omitempty"`
	StartedAt    string `json:"startedAt,omitempty"`
	RestartCount int    `json:"restartCount,omitempty"`
	CurrentRunID string `json:"currentRunId,omitempty"`
	RunningCount int    `json:"runningCount,omitempty"`
	LastRunID    string `json:"lastRunId,omitempty"`
	LastRunAt    string `json:"lastRunAt,omitempty"`
	LastExitCode *int   `json:"lastExitCode,omitempty"`
	LastSuccess  *bool  `json:"lastSuccess,omitempty"`
	Message      string `json:"message,omitempty"`
	Error        string `json:"error,omitempty"`
}

type View struct {
	Task   Task   `json:"task"`
	Status Status `json:"status"`
}

func (pl *plugin) view(t *Task) View {
	r := pl.runtimeOf(t.ID)
	s := Status{StartedAt: r.startedAt, RestartCount: r.restartCount, CurrentRunID: r.currentRunID, LastRunID: r.lastRunID, LastRunAt: r.lastRunAt, LastExitCode: r.lastExit, LastSuccess: r.lastSuccess, Message: r.message, Error: r.err}
	if t.Type == typeScheduled {
		s.RunningCount = r.active
		switch {
		case r.active > 0:
			s.State = stateRunning
		case r.lastSuccess != nil && !*r.lastSuccess:
			s.State = "failure"
		default:
			s.State = "idle"
		}
		s.PID = r.pid
		if r.active == 0 {
			s.StartedAt = ""
		}
	} else {
		s.State, s.PID = r.state, r.pid
	}
	return View{Task: cloneTask(*t), Status: s}
}

func (pl *plugin) publishStatus(t *Task) {
	pl.publish("tasks.status", pl.view(t))
}

func (pl *plugin) views() []View {
	views := make([]View, 0, len(pl.tasks))
	for _, t := range pl.tasks {
		views = append(views, pl.view(t))
	}
	sort.Slice(views, func(i, j int) bool {
		a, b := strings.ToLower(views[i].Task.Name), strings.ToLower(views[j].Task.Name)
		if a != b {
			return a < b
		}
		return views[i].Task.ID < views[j].Task.ID
	})
	return views
}
