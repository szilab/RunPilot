package jobs

import (
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/backup"
	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/history"
	"github.com/szilab/RunPilot/internal/launcher"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
)

type Runner struct {
	mu      sync.Mutex
	active  map[string]string
	status  map[string]model.JobStatus
	history *history.Store
}

func New(h *history.Store) *Runner {
	return &Runner{
		active:  make(map[string]string),
		status:  make(map[string]model.JobStatus),
		history: h,
	}
}

func (r *Runner) Run(def model.JobDefinition) (string, error) {
	model.NormalizeJob(&def)

	r.mu.Lock()
	if current, running := r.active[def.ID]; running && def.OverlapPolicy != "allow" {
		r.mu.Unlock()
		return "", fmt.Errorf("job %q is already running as %s", def.Name, current)
	}
	runID := config.NewID("run")
	r.active[def.ID] = runID
	st := r.status[def.ID]
	st.ID = def.ID
	st.Name = def.Name
	st.Running = true
	st.CurrentRunID = runID
	r.status[def.ID] = st
	r.mu.Unlock()

	go r.execute(def, runID)
	return runID, nil
}

func (r *Runner) execute(def model.JobDefinition, runID string) {
	started := time.Now()
	logPath := r.history.RunLogPath(runID)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		r.finish(def, runID, started, -1, false, logPath, err.Error())
		return
	}

	plan, err := planFor(def)
	if err != nil {
		_, _ = fmt.Fprintln(logFile, err)
		_ = logFile.Close()
		r.finish(def, runID, started, -1, false, logPath, err.Error())
		return
	}

	if def.Type == model.JobBackup {
		_, _ = fmt.Fprintf(logFile, "=== Backup: %s ===\n", plan.Provider)
	}
	deadline := time.Time{}
	if def.TimeoutSeconds > 0 {
		deadline = started.Add(time.Duration(def.TimeoutSeconds) * time.Second)
	}
	code, success, message := -1, true, ""
	for i, step := range plan.Steps {
		_, _ = fmt.Fprintf(logFile, "\n=== Step %d/%d: %s ===\n", i+1, len(plan.Steps), step.Name)
		code, success, message = runStep(logFile, step, deadline, def.TimeoutSeconds)
		if !success {
			break
		}
	}
	_ = logFile.Close()
	r.finish(def, runID, started, code, success, logPath, message)
}

func runStep(logFile *os.File, step backup.Step, deadline time.Time, timeoutSeconds int) (int, bool, string) {
	cmd, err := launcher.Build(step.Command)
	if err != nil {
		_, _ = fmt.Fprintln(logFile, err)
		return -1, false, err.Error()
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err = cmd.Start(); err != nil {
		_, _ = fmt.Fprintln(logFile, "start error:", err)
		return -1, false, err.Error()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	timedOut := false
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			timedOut = true
			_ = platform.KillProcessTree(cmd.Process.Pid)
			waitErr = <-done
		} else {
			timer := time.NewTimer(remaining)
			select {
			case waitErr = <-done:
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
				timedOut = true
				_ = platform.KillProcessTree(cmd.Process.Pid)
				waitErr = <-done
			}
		}
	} else {
		waitErr = <-done
	}
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	ok := waitErr == nil && step.Success(code)
	if timedOut {
		return code, false, fmt.Sprintf("timed out after %d seconds", timeoutSeconds)
	}
	if !ok && waitErr != nil {
		return code, false, waitErr.Error()
	}
	if !ok {
		return code, false, fmt.Sprintf("step %s failed with exit code %d", step.Name, code)
	}
	return code, true, ""
}

func planFor(def model.JobDefinition) (backup.Plan, error) {
	switch def.Type {
	case model.JobCommand:
		if def.Command == nil {
			return backup.Plan{}, fmt.Errorf("command job has no command")
		}
		return backup.Plan{Provider: "command", Steps: []backup.Step{{Name: "command", Command: *def.Command, Success: func(code int) bool { return code == 0 }}}}, nil
	case model.JobBackup:
		if def.Backup == nil {
			return backup.Plan{}, fmt.Errorf("backup job has no backup specification")
		}
		return backup.BuildPlan(*def.Backup)
	default:
		return backup.Plan{}, fmt.Errorf("unsupported job type %q", def.Type)
	}
}

func (r *Runner) finish(def model.JobDefinition, runID string, started time.Time, code int, success bool, logPath, message string) {
	finished := time.Now()
	rec := model.RunRecord{
		ID:         runID,
		Kind:       string(def.Type),
		TargetID:   def.ID,
		TargetName: def.Name,
		StartedAt:  started,
		FinishedAt: &finished,
		ExitCode:   &code,
		Success:    &success,
		LogPath:    logPath,
		Message:    message,
	}
	_ = r.history.Append(rec)

	r.mu.Lock()
	delete(r.active, def.ID)
	st := r.status[def.ID]
	st.ID = def.ID
	st.Name = def.Name
	st.Running = false
	st.CurrentRunID = ""
	st.LastRunAt = &started
	st.LastExitCode = &code
	st.LastSuccess = &success
	r.status[def.ID] = st
	r.mu.Unlock()
}

type View struct {
	Definition model.JobDefinition `json:"definition"`
	Status     model.JobStatus     `json:"status"`
}

func (r *Runner) Views(defs []model.JobDefinition) []View {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]View, 0, len(defs))
	for _, def := range defs {
		st := r.status[def.ID]
		st.ID = def.ID
		st.Name = def.Name
		out = append(out, View{Definition: def, Status: st})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Definition.Name < out[j].Definition.Name
	})
	return out
}
