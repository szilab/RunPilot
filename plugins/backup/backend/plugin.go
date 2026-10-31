package main

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

const (
	stateVersion   = 1
	storageKey     = "jobs"
	callbackJob    = "job"
	callbackCheck  = "reconcile"
	reconcileID    = "reconcile"
	reconcileEvery = 30

	kindBackup  = "backup"
	kindRestore = "restore"
)

// callHost is replaced by tests; in the WASM module it is the ABI-v2 SDK.
var callHost = pluginapi.CallHost

type rpcError struct{ Code, Message string }

func (e *rpcError) Error() string { return e.Message }
func rpcFail(code, message string) *rpcError {
	return &rpcError{Code: code, Message: message}
}

func hostFailure(err error) *rpcError {
	if e, ok := err.(*rpcError); ok {
		return e
	}
	if capability, ok := pluginapi.AsCapabilityError(err); ok {
		return rpcFail(capability.Code, capability.Message)
	}
	return rpcFail("failed", err.Error())
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

func itoa(n int) string { return strconv.Itoa(n) }

func decodeBase64(value string) ([]byte, error) { return base64.StdEncoding.DecodeString(value) }

type stateFile struct {
	Version int   `json:"version"`
	Next    int   `json:"next"`
	Jobs    []Job `json:"jobs"`
}

// operation is one running backup, restore or maintenance run. Its steps
// run one after another and write into the same history execution.
type operation struct {
	runID, jobID, kind, startedAt string
	steps                         []step
	index                         int
	processID                     string
	env                           map[string]string
	timeout                       int
	stopping                      bool
}

// jobRuntime is in-memory only; it is rebuilt from history on start.
type jobRuntime struct {
	op          *operation
	lastRunID   string
	lastRunAt   string
	lastKind    string
	lastSuccess *bool
	lastMessage string
	err         string
}

type hostStatus struct {
	OS    string `json:"os"`
	Disks []struct {
		Path string `json:"path"`
	} `json:"disks"`
}

type plugin struct {
	jobs      []*Job
	next      int
	loadError string
	rt        map[string]*jobRuntime
	procs     map[string]*operation
	host      *hostStatus
	tools     map[string]tool
}

func newPlugin() *plugin {
	return &plugin{next: 1, rt: map[string]*jobRuntime{}, procs: map[string]*operation{}}
}

func (p *plugin) status() hostStatus {
	if p.host == nil {
		var status hostStatus
		if callHost("system.status", map[string]any{}, &status) != nil || status.OS == "" {
			return hostStatus{OS: "linux"}
		}
		p.host = &status
	}
	return *p.host
}

func (p *plugin) hostOS() string { return p.status().OS }

func (p *plugin) find(id string) *Job {
	for _, j := range p.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (p *plugin) runtimeOf(id string) *jobRuntime {
	r := p.rt[id]
	if r == nil {
		r = &jobRuntime{}
		p.rt[id] = r
	}
	return r
}

func (p *plugin) publish(event string, data any) {
	_ = callHost("events.publish", map[string]any{"event": event, "data": data}, nil)
}

// ---- persistence ---------------------------------------------------------

// load reads the stored jobs. Unreadable state is never overwritten: the
// plugin stays read-only until plugins/backup/data/storage.json is repaired.
func (p *plugin) load() {
	var raw json.RawMessage
	if err := callHost("storage.get", map[string]any{"key": storageKey}, &raw); err != nil {
		p.loadError = "could not read stored backup jobs: " + errText(err)
		return
	}
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var state stateFile
	if err := json.Unmarshal(raw, &state); err != nil {
		p.loadError = "stored backup jobs are unreadable: " + err.Error()
		return
	}
	if state.Version != stateVersion {
		p.loadError = "stored backup jobs have unsupported version " + itoa(state.Version)
		return
	}
	seen := map[string]bool{}
	next := state.Next
	for i := range state.Jobs {
		j := state.Jobs[i]
		if j.ID == "" || seen[j.ID] {
			p.loadError = "stored backup jobs contain a missing or duplicate ID"
			p.jobs, p.next = nil, 1
			return
		}
		seen[j.ID] = true
		if n, err := strconv.Atoi(strings.TrimPrefix(j.ID, "job-")); err == nil && n >= next {
			next = n + 1
		}
		normalizeJob(&j)
		p.jobs = append(p.jobs, &j)
	}
	if next < 1 {
		next = 1
	}
	p.next = next
}

func (p *plugin) save(jobs []*Job, next int) error {
	state := stateFile{Version: stateVersion, Next: next, Jobs: make([]Job, 0, len(jobs))}
	for _, j := range jobs {
		state.Jobs = append(state.Jobs, *j)
	}
	return callHost("storage.set", map[string]any{"key": storageKey, "value": state}, nil)
}

// ---- lifecycle -----------------------------------------------------------

func (p *plugin) init() {
	p.load()
	if p.loadError == "" {
		for _, j := range p.jobs {
			r := p.runtimeOf(j.ID)
			p.loadLastRun(j, r)
			if err := validateJob(j, p.hostOS()); err != nil {
				r.err = err.Error()
				continue
			}
			if err := p.applySchedule(j); err != nil {
				r.err = "schedule not registered: " + errText(err)
			}
		}
	}
	// Safety net for a lost process.exit callback; see reconcile.
	_ = p.register(reconcileID, callbackCheck, &Schedule{Type: "interval", IntervalSeconds: reconcileEvery})
}

func (p *plugin) loadLastRun(j *Job, r *jobRuntime) {
	var list struct {
		Executions []execution `json:"executions"`
	}
	if err := callHost("history.list", map[string]any{"subject": j.ID, "limit": 1}, &list); err != nil || len(list.Executions) == 0 {
		return
	}
	e := list.Executions[0]
	r.lastRunID, r.lastRunAt, r.lastKind, r.lastSuccess, r.lastMessage = e.ID, e.StartedAt, e.Kind, e.Success, e.Message
}

type execution struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	StartedAt string `json:"startedAt"`
	Success   *bool  `json:"success"`
	Message   string `json:"message"`
}

// shutdown stops running backups and records why; RunPilot would kill the
// processes anyway.
func (p *plugin) shutdown() {
	for id, op := range p.procs {
		_ = callHost("process.terminate", map[string]any{"id": id}, nil)
		failed := false
		p.finishHistory(op.runID, nil, &failed, "stopped: RunPilot is shutting down")
	}
	p.procs = map[string]*operation{}
}

// ---- schedules -----------------------------------------------------------

func (p *plugin) register(id, callback string, s *Schedule) error {
	return callHost("scheduler.register", map[string]any{"id": id, "callback": callback, "schedule": s}, nil)
}

func (p *plugin) unregister(id string) {
	_ = callHost("scheduler.remove", map[string]any{"id": id}, nil) // absent is fine.
}

// applySchedule makes the host timer match the job definition.
func (p *plugin) applySchedule(j *Job) error {
	p.unregister(j.ID)
	if j.Enabled && j.Schedule != nil {
		return p.register(j.ID, callbackJob, j.Schedule)
	}
	return nil
}

func (p *plugin) validateSchedule(j *Job) error {
	if j.Schedule == nil {
		return nil
	}
	return callHost("scheduler.validate", map[string]any{"schedule": j.Schedule}, nil)
}

func (p *plugin) nextRuns() map[string]string {
	var list struct {
		Schedules []struct {
			ID        string `json:"id"`
			NextRunAt string `json:"nextRunAt"`
		} `json:"schedules"`
	}
	out := map[string]string{}
	if callHost("scheduler.list", map[string]any{}, &list) == nil {
		for _, item := range list.Schedules {
			out[item.ID] = item.NextRunAt
		}
	}
	return out
}

// ---- operations ----------------------------------------------------------

func (p *plugin) appendHistory(runID, text string) {
	_ = callHost("history.append", map[string]any{"id": runID, "text": text}, nil)
}

func (p *plugin) finishHistory(runID string, code *int, success *bool, message string) {
	params := map[string]any{"id": runID, "message": message}
	if code != nil {
		params["exitCode"] = *code
	}
	if success != nil {
		params["success"] = *success
	}
	_ = callHost("history.finish", params, nil)
}

var kindLabels = map[string]string{kindBackup: "Backup", kindRestore: "Restore", "check": "Check repository", "unlock": "Remove stale locks", "init": "Initialize repository"}

// environment reads the job's environment file, if it has one.
func (p *plugin) environment(j *Job) (map[string]string, error) {
	if j.Restic == nil || j.Restic.EnvironmentFile == "" {
		return nil, nil
	}
	return p.readEnvironmentFile(j.Restic.EnvironmentFile)
}

// start records an execution and runs the first step. A job runs one
// operation at a time: a manual request is refused, a scheduled run is
// recorded as skipped.
func (p *plugin) start(j *Job, kind string, steps []step, manual bool) (string, error) {
	r := p.runtimeOf(j.ID)
	if r.op != nil {
		if manual {
			return "", rpcFail("already_running", "\""+j.Name+"\" is already running")
		}
		runID := p.begin(j, kind)
		if runID != "" {
			p.appendHistory(runID, "Skipped: "+strings.ToLower(kindLabels[r.op.kind])+" of this job was still running.\n")
			p.finishHistory(runID, nil, nil, "skipped: the previous run was still running")
			p.recordLast(r, runID, "", kind, nil, "skipped: the previous run was still running")
			p.publishStatus(j)
		}
		return "", nil
	}
	runID := p.begin(j, kind)
	if runID == "" {
		return "", rpcFail("failed", "could not record the run")
	}
	var started struct {
		StartedAt string `json:"startedAt"`
	}
	_ = callHost("history.get", map[string]any{"id": runID}, &started)
	op := &operation{runID: runID, jobID: j.ID, kind: kind, startedAt: started.StartedAt, steps: steps, timeout: j.TimeoutSeconds}
	p.appendHistory(runID, "=== "+kindLabels[kind]+": "+j.Name+" ("+j.Engine+") ===\n")
	env, err := p.environment(j)
	if err != nil {
		p.finish(j, r, op, nil, false, err.Error())
		return runID, rpcFail("invalid_argument", err.Error())
	}
	op.env = env
	r.op = op
	if err := p.runStep(op); err != nil {
		p.finish(j, r, op, nil, false, errText(err))
		return runID, hostFailure(err)
	}
	p.publishStatus(j)
	return runID, nil
}

func (p *plugin) begin(j *Job, kind string) string {
	var begin struct {
		ID string `json:"id"`
	}
	if callHost("history.begin", map[string]any{"kind": kind, "subject": j.ID, "label": j.Name}, &begin) != nil {
		return ""
	}
	return begin.ID
}

func (p *plugin) runStep(op *operation) error {
	s := op.steps[op.index]
	p.appendHistory(op.runID, "\n=== Step "+itoa(op.index+1)+"/"+itoa(len(op.steps))+": "+s.Name+" ("+s.Command+") ===\n")
	var started struct {
		ID string `json:"id"`
	}
	err := callHost("process.start", map[string]any{"command": s.Command, "args": s.Args, "environment": op.env, "interpreter": "direct", "timeoutSeconds": op.timeout, "historyId": op.runID}, &started)
	if err != nil {
		p.appendHistory(op.runID, "start error: "+errText(err)+"\n")
		return err
	}
	op.processID = started.ID
	p.procs[started.ID] = op
	return nil
}

type exitInfo struct {
	code       int
	terminated bool
	timedOut   bool
}

// processExited is idempotent: the exit event, a reconcile pass and
// shutdown may all see the same exit.
func (p *plugin) processExited(processID string, info exitInfo) {
	op := p.procs[processID]
	if op == nil {
		return
	}
	delete(p.procs, processID)
	op.processID = ""
	j, r := p.find(op.jobID), p.runtimeOf(op.jobID)
	s := op.steps[op.index]
	code := info.code
	var message string
	success := false
	switch {
	case info.timedOut:
		message = s.Name + " timed out after " + itoa(op.timeout) + " seconds"
	case info.terminated || op.stopping:
		message = "stopped"
	case !s.Success(code):
		message = s.Name + " failed with exit code " + itoa(code)
	default:
		success = true
	}
	if success && op.index+1 < len(op.steps) {
		op.index++
		if err := p.runStep(op); err == nil {
			if j != nil {
				p.publishStatus(j)
			}
			return
		} else {
			success, message = false, errText(err)
		}
	}
	p.finish(j, r, op, &code, success, message)
}

func (p *plugin) finish(j *Job, r *jobRuntime, op *operation, code *int, success bool, message string) {
	p.finishHistory(op.runID, code, &success, message)
	if r.op == op {
		r.op = nil
	}
	p.recordLast(r, op.runID, op.startedAt, op.kind, &success, message)
	name := ""
	if j != nil {
		name = j.Name
		p.publishStatus(j)
	}
	p.publish("backup.run.completed", map[string]any{"id": op.jobID, "name": name, "kind": op.kind, "runId": op.runID, "success": success, "message": message})
}

func (p *plugin) recordLast(r *jobRuntime, runID, startedAt, kind string, success *bool, message string) {
	if startedAt == "" {
		var e execution
		if callHost("history.get", map[string]any{"id": runID}, &e) == nil {
			startedAt = e.StartedAt
		}
	}
	r.lastRunID, r.lastRunAt, r.lastKind, r.lastSuccess, r.lastMessage = runID, startedAt, kind, success, message
}

func (p *plugin) stop(j *Job) error {
	r := p.runtimeOf(j.ID)
	if r.op == nil {
		return rpcFail("not_running", "\""+j.Name+"\" is not running")
	}
	r.op.stopping = true
	if r.op.processID == "" {
		return nil
	}
	return p.terminate(r.op.processID)
}

type processStatus struct {
	Running    bool `json:"running"`
	ExitCode   *int `json:"exitCode"`
	Terminated bool `json:"terminated"`
	TimedOut   bool `json:"timedOut"`
}

func (s processStatus) exit() exitInfo {
	code := -1
	if s.ExitCode != nil {
		code = *s.ExitCode
	}
	return exitInfo{code: code, terminated: s.Terminated, timedOut: s.TimedOut}
}

func (p *plugin) terminate(processID string) error {
	var status processStatus
	if err := callHost("process.terminate", map[string]any{"id": processID}, &status); err != nil {
		if isNotFound(err) {
			p.processExited(processID, exitInfo{code: -1, terminated: true})
			return nil
		}
		return err
	}
	if !status.Running {
		p.processExited(processID, status.exit())
	}
	return nil
}

// reconcile settles exits whose callback was lost or delayed.
func (p *plugin) reconcile() {
	ids := make([]string, 0, len(p.procs))
	for id := range p.procs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var status processStatus
		if err := callHost("process.status", map[string]any{"id": id}, &status); err != nil {
			if isNotFound(err) {
				p.processExited(id, exitInfo{code: -1})
			}
			continue
		}
		if !status.Running {
			p.processExited(id, status.exit())
		}
	}
}

// ---- views ---------------------------------------------------------------

type Status struct {
	State       string `json:"state"`
	Kind        string `json:"kind,omitempty"`
	RunID       string `json:"runId,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	Step        string `json:"step,omitempty"`
	StepIndex   int    `json:"stepIndex,omitempty"`
	Steps       int    `json:"steps,omitempty"`
	LastRunID   string `json:"lastRunId,omitempty"`
	LastRunAt   string `json:"lastRunAt,omitempty"`
	LastKind    string `json:"lastKind,omitempty"`
	LastSuccess *bool  `json:"lastSuccess,omitempty"`
	LastMessage string `json:"lastMessage,omitempty"`
	NextRunAt   string `json:"nextRunAt,omitempty"`
	Error       string `json:"error,omitempty"`
}

type View struct {
	Job    Job    `json:"job"`
	Status Status `json:"status"`
}

func (p *plugin) view(j *Job, next map[string]string) View {
	r := p.runtimeOf(j.ID)
	s := Status{LastRunID: r.lastRunID, LastRunAt: r.lastRunAt, LastKind: r.lastKind, LastSuccess: r.lastSuccess, LastMessage: r.lastMessage, Error: r.err}
	if j.Enabled && j.Schedule != nil {
		s.NextRunAt = next[j.ID]
	}
	switch {
	case r.op != nil:
		s.State, s.Kind, s.RunID, s.StartedAt = "running", r.op.kind, r.op.runID, r.op.startedAt
		if r.op.stopping {
			s.State = "stopping"
		}
		s.Step, s.StepIndex, s.Steps = r.op.steps[r.op.index].Name, r.op.index+1, len(r.op.steps)
	case r.lastSuccess != nil && !*r.lastSuccess:
		s.State = "failure"
	default:
		s.State = "idle"
	}
	return View{Job: cloneJob(*j), Status: s}
}

func (p *plugin) publishStatus(j *Job) {
	p.publish("backup.status", p.view(j, p.nextRuns()))
}

func (p *plugin) views() []View {
	next := p.nextRuns()
	views := make([]View, 0, len(p.jobs))
	for _, j := range p.jobs {
		views = append(views, p.view(j, next))
	}
	sort.Slice(views, func(i, k int) bool {
		a, b := strings.ToLower(views[i].Job.Name), strings.ToLower(views[k].Job.Name)
		if a != b {
			return a < b
		}
		return views[i].Job.ID < views[k].Job.ID
	})
	return views
}

// toolsFor detects the engines' default commands once; refresh checks
// again, for example after installing restic.
func (p *plugin) toolsFor(refresh bool) []tool {
	hostOS := p.hostOS()
	if p.tools == nil || refresh {
		p.tools = map[string]tool{}
		for _, engine := range engineNames(hostOS) {
			p.tools[engine] = detectTool(engine, executable("", engine, hostOS))
		}
	}
	out := []tool{}
	for _, engine := range engineNames(hostOS) {
		out = append(out, p.tools[engine])
	}
	return out
}
