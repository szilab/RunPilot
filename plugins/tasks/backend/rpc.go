package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// handle dispatches one browser RPC. Results are plain JSON values; failures
// are *rpcError and are written as {"error":{"code","message"}}.
func (pl *plugin) handle(operation string, request json.RawMessage) (any, *rpcError) {
	switch operation {
	case "tasks.list":
		pl.reconcile()
		result := map[string]any{"tasks": pl.views()}
		if pl.loadError != "" {
			result["loadError"] = pl.loadError
		}
		return result, nil
	case "tasks.create", "tasks.update":
		var args struct {
			Task Task `json:"task"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		if operation == "tasks.create" {
			return pl.create(args.Task)
		}
		return pl.update(args.Task)
	case "tasks.delete", "tasks.start", "tasks.stop", "tasks.restart", "tasks.run":
		var args struct {
			ID string `json:"id"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		return pl.control(operation, args.ID)
	case "tasks.history":
		var args struct {
			ID    string `json:"id"`
			Limit int    `json:"limit"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		var list struct {
			Executions []json.RawMessage `json:"executions"`
		}
		if err := callHost("history.list", map[string]any{"subject": args.ID, "limit": args.Limit}, &list); err != nil {
			return nil, hostFailure(err)
		}
		if list.Executions == nil {
			list.Executions = []json.RawMessage{}
		}
		return map[string]any{"runs": list.Executions}, nil
	case "tasks.output":
		var args struct {
			RunID    string `json:"runId"`
			MaxBytes int    `json:"maxBytes"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		var run json.RawMessage
		if err := callHost("history.get", map[string]any{"id": args.RunID}, &run); err != nil {
			return nil, hostFailure(err)
		}
		var output struct {
			Output    string `json:"output"`
			Size      int64  `json:"size"`
			Truncated bool   `json:"truncated"`
		}
		if err := callHost("history.output", map[string]any{"id": args.RunID, "maxBytes": args.MaxBytes}, &output); err != nil {
			return nil, hostFailure(err)
		}
		return map[string]any{"run": run, "output": output.Output, "size": output.Size, "truncated": output.Truncated}, nil
	default:
		return nil, rpcFail("unknown_method", "unknown tasks method")
	}
}

func decodeRequest(request json.RawMessage, destination any) *rpcError {
	if len(request) == 0 || string(request) == "null" {
		request = json.RawMessage("{}")
	}
	if err := json.Unmarshal(request, destination); err != nil {
		return rpcFail("invalid_argument", "invalid request: "+err.Error())
	}
	return nil
}

// writable guards every mutation: unreadable stored configuration is never
// overwritten, so operating on it is refused rather than guessed at.
func (pl *plugin) writable() *rpcError {
	if pl.loadError != "" {
		return rpcFail("state_unreadable", pl.loadError)
	}
	return nil
}

func (pl *plugin) index(id string) int {
	for i, t := range pl.tasks {
		if t.ID == id {
			return i
		}
	}
	return -1
}

func (pl *plugin) create(input Task) (any, *rpcError) {
	if err := pl.writable(); err != nil {
		return nil, err
	}
	if len(pl.tasks) >= maxTasks {
		return nil, rpcFail("limit_reached", "too many tasks")
	}
	t := cloneTask(input)
	t.ID = "task-" + strconv.Itoa(pl.next)
	normalizeTask(&t)
	if err := validateTask(&t); err != nil {
		return nil, rpcFail("invalid_argument", err.Error())
	}
	if err := pl.validateSchedule(&t); err != nil {
		return nil, hostFailure(err)
	}
	if err := pl.applySchedule(&t); err != nil {
		pl.unregister(t.ID)
		return nil, hostFailure(err)
	}
	tasks := append(append([]*Task(nil), pl.tasks...), &t)
	if err := pl.save(tasks, pl.next+1); err != nil {
		pl.unregister(t.ID)
		return nil, hostFailure(err)
	}
	pl.tasks, pl.next = tasks, pl.next+1
	pl.runtimeOf(t.ID)
	pl.publish("tasks.changed", map[string]any{"reason": "created", "id": t.ID})
	return pl.view(&t), nil
}

func (pl *plugin) update(input Task) (any, *rpcError) {
	if err := pl.writable(); err != nil {
		return nil, err
	}
	i := pl.index(input.ID)
	if i < 0 {
		return nil, rpcFail("not_found", "unknown task")
	}
	old := pl.tasks[i]
	if input.Type != old.Type {
		return nil, rpcFail("invalid_argument", "task type cannot be changed")
	}
	t := cloneTask(input)
	normalizeTask(&t)
	if err := validateTask(&t); err != nil {
		return nil, rpcFail("invalid_argument", err.Error())
	}
	if err := pl.validateSchedule(&t); err != nil {
		return nil, hostFailure(err)
	}
	if err := pl.applySchedule(&t); err != nil {
		_ = pl.applySchedule(old) // keep the previous registration.
		return nil, hostFailure(err)
	}
	tasks := append([]*Task(nil), pl.tasks...)
	tasks[i] = &t
	if err := pl.save(tasks, pl.next); err != nil {
		_ = pl.applySchedule(old)
		return nil, hostFailure(err)
	}
	pl.tasks = tasks
	r := pl.runtimeOf(t.ID)
	r.err = ""
	pl.publish("tasks.changed", map[string]any{"reason": "updated", "id": t.ID})
	return pl.view(&t), nil
}

func (pl *plugin) control(operation, id string) (any, *rpcError) {
	if err := pl.writable(); err != nil {
		return nil, err
	}
	i := pl.index(id)
	if i < 0 {
		return nil, rpcFail("not_found", "unknown task")
	}
	t := pl.tasks[i]
	r := pl.runtimeOf(t.ID)
	pl.reconcile() // settle an exit whose callback has not arrived yet.
	switch operation {
	case "tasks.delete":
		tasks := append(append([]*Task(nil), pl.tasks[:i]...), pl.tasks[i+1:]...)
		if err := pl.save(tasks, pl.next); err != nil {
			return nil, hostFailure(err)
		}
		pl.tasks = tasks
		_ = pl.stopTask(t, r, false)
		pl.unregister(t.ID)
		delete(pl.rt, t.ID)
		pl.publish("tasks.changed", map[string]any{"reason": "deleted", "id": t.ID})
		return map[string]any{"deleted": t.ID}, nil
	case "tasks.start":
		if t.Type != typeContinuous {
			return nil, rpcFail("invalid_argument", "only continuous tasks can be started; use run for scheduled tasks")
		}
		if err := pl.startTask(t, r); err != nil {
			return nil, asRPC(err)
		}
	case "tasks.stop":
		if err := pl.stopTask(t, r, false); err != nil {
			return nil, asRPC(err)
		}
	case "tasks.restart":
		if t.Type != typeContinuous {
			return nil, rpcFail("invalid_argument", "only continuous tasks can be restarted")
		}
		if r.processID == "" {
			if err := pl.startTask(t, r); err != nil {
				return nil, asRPC(err)
			}
		} else {
			r.pendingStart = true
			if err := pl.stopTask(t, r, true); err != nil {
				r.pendingStart = false
				return nil, asRPC(err)
			}
		}
	case "tasks.run":
		if t.Type != typeScheduled {
			return nil, rpcFail("invalid_argument", "only scheduled tasks can be run; use start for continuous tasks")
		}
		runID, err := pl.runNow(t, r, true)
		if err != nil {
			return nil, asRPC(err)
		}
		return map[string]any{"runId": runID, "task": pl.view(t)}, nil
	}
	return pl.view(t), nil
}

func asRPC(err error) *rpcError {
	if e, ok := err.(*rpcError); ok {
		return e
	}
	return hostFailure(err)
}

// event handles host callbacks. Unknown events are ignored so newer hosts can
// add events without breaking this plugin.
func (pl *plugin) event(operation string, payload json.RawMessage) {
	switch operation {
	case "process.exit":
		var exit struct {
			ID         string `json:"id"`
			ExitCode   int    `json:"exitCode"`
			Success    bool   `json:"success"`
			Terminated bool   `json:"terminated"`
			TimedOut   bool   `json:"timedOut"`
		}
		if json.Unmarshal(payload, &exit) == nil {
			pl.processExited(exit.ID, exitInfo{code: exit.ExitCode, success: exit.Success, terminated: exit.Terminated, timedOut: exit.TimedOut})
		}
	case "scheduler.fired":
		var fired struct {
			ID       string `json:"id"`
			Callback string `json:"callback"`
		}
		if json.Unmarshal(payload, &fired) != nil {
			return
		}
		switch fired.Callback {
		case callbackCheck:
			pl.reconcile()
		case callbackRestart:
			pl.restartFired(strings.TrimPrefix(fired.ID, restartPrefix))
		case callbackTask:
			t := pl.find(fired.ID)
			if t == nil || pl.loadError != "" || t.Type != typeScheduled || !t.Enabled {
				pl.unregister(fired.ID)
				return
			}
			_, _ = pl.runNow(t, pl.runtimeOf(t.ID), false)
		}
	}
}
