package processmgr

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/history"
	"github.com/szilab/RunPilot/internal/launcher"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
)

type Manager struct {
	mu      sync.RWMutex
	defs    map[string]model.ProcessDefinition
	runtime map[string]*runtimeProcess
	history *history.Store
}

type runtimeProcess struct {
	mu           sync.Mutex
	state        string
	pid          int
	startedAt    *time.Time
	exitCode     *int
	restartCount int
	currentRunID string
	logPath      string
	desired      bool
	generation   uint64
}

func New(h *history.Store) *Manager {
	return &Manager{
		defs:    make(map[string]model.ProcessDefinition),
		runtime: make(map[string]*runtimeProcess),
		history: h,
	}
}

func (m *Manager) Reconcile(defs []model.ProcessDefinition) {
	next := make(map[string]model.ProcessDefinition, len(defs))
	for _, d := range defs {
		next[d.ID] = d
	}

	m.mu.Lock()
	var removed []string
	for id := range m.defs {
		if _, ok := next[id]; !ok {
			removed = append(removed, id)
		}
	}
	m.defs = next
	for id := range next {
		if _, ok := m.runtime[id]; !ok {
			m.runtime[id] = &runtimeProcess{state: "stopped"}
		}
	}
	m.mu.Unlock()

	for _, id := range removed {
		_ = m.Stop(id)
		m.mu.Lock()
		delete(m.runtime, id)
		m.mu.Unlock()
	}
}

func (m *Manager) StartAutostart() {
	m.mu.RLock()
	var ids []string
	for id, d := range m.defs {
		if d.Autostart {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	for _, id := range ids {
		_ = m.Start(id)
	}
}

func (m *Manager) Start(id string) error {
	return m.start(id, true)
}

func (m *Manager) start(id string, resetRetries bool) error {
	m.mu.RLock()
	def, ok := m.defs[id]
	rt := m.runtime[id]
	m.mu.RUnlock()
	if !ok || rt == nil {
		return fmt.Errorf("unknown process %q", id)
	}

	rt.mu.Lock()
	if rt.state == "running" || rt.state == "starting" || rt.state == "stopping" {
		rt.mu.Unlock()
		return fmt.Errorf("process %q is already %s", def.Name, rt.state)
	}
	if resetRetries {
		rt.restartCount = 0
	}
	rt.desired = true
	rt.state = "starting"
	rt.generation++
	gen := rt.generation
	runID := config.NewID("run")
	rt.currentRunID = runID
	rt.exitCode = nil
	rt.mu.Unlock()

	cmd, err := launcher.Build(def.Command)
	if err != nil {
		m.startFailed(def, rt, runID, err)
		return err
	}
	logPath := m.history.RunLogPath(runID)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		m.startFailed(def, rt, runID, err)
		return err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	started := time.Now()
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		m.startFailed(def, rt, runID, err)
		return fmt.Errorf("start %s: %w", def.Name, err)
	}

	rt.mu.Lock()
	if rt.generation != gen {
		rt.mu.Unlock()
		_ = platform.KillProcessTree(cmd.Process.Pid)
		_ = logFile.Close()
		return fmt.Errorf("process %q start superseded", def.Name)
	}
	rt.state = "running"
	rt.pid = cmd.Process.Pid
	rt.startedAt = &started
	rt.logPath = logPath
	rt.mu.Unlock()

	go m.wait(def.ID, def.Name, rt, gen, cmd, logFile, started, runID)
	return nil
}

func (m *Manager) startFailed(def model.ProcessDefinition, rt *runtimeProcess, runID string, cause error) {
	now := time.Now()
	code := -1
	ok := false
	rt.mu.Lock()
	rt.state = "stopped"
	rt.pid = 0
	rt.startedAt = nil
	rt.exitCode = &code
	rt.desired = false
	rt.mu.Unlock()
	_ = m.history.Append(model.RunRecord{
		ID:         runID,
		Kind:       "process",
		TargetID:   def.ID,
		TargetName: def.Name,
		StartedAt:  now,
		FinishedAt: &now,
		ExitCode:   &code,
		Success:    &ok,
		LogPath:    m.history.RunLogPath(runID),
		Message:    cause.Error(),
	})
}

func (m *Manager) wait(
	id, name string,
	rt *runtimeProcess,
	gen uint64,
	cmd *exec.Cmd,
	logFile io.Closer,
	started time.Time,
	runID string,
) {
	err := cmd.Wait()
	_ = logFile.Close()

	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	m.mu.RLock()
	def := m.defs[id]
	m.mu.RUnlock()

	success := err == nil && code == 0
	finished := time.Now()

	rt.mu.Lock()
	if rt.generation != gen {
		rt.mu.Unlock()
		return
	}
	rt.state = "stopped"
	rt.pid = 0
	rt.startedAt = nil
	rt.exitCode = &code
	desired := rt.desired
	restarts := rt.restartCount
	rt.mu.Unlock()

	_ = m.history.Append(model.RunRecord{
		ID:         runID,
		Kind:       "process",
		TargetID:   id,
		TargetName: name,
		StartedAt:  started,
		FinishedAt: &finished,
		ExitCode:   &code,
		Success:    &success,
		LogPath:    m.history.RunLogPath(runID),
	})

	if !desired {
		return
	}
	restart := def.Restart.Mode == model.RestartAlways ||
		(def.Restart.Mode == model.RestartOnFailure && !success)
	if !restart {
		return
	}
	if def.Restart.MaxRetries > 0 && restarts >= def.Restart.MaxRetries {
		rt.mu.Lock()
		rt.desired = false
		rt.mu.Unlock()
		return
	}

	delay := restartDelay(def.Restart, restarts)
	rt.mu.Lock()
	rt.restartCount++
	currentGen := rt.generation
	rt.mu.Unlock()

	time.Sleep(delay)

	rt.mu.Lock()
	stillDesired := rt.desired && rt.generation == currentGen && rt.state == "stopped"
	rt.mu.Unlock()
	if stillDesired {
		_ = m.start(id, false)
	}
}

func restartDelay(p model.RestartPolicy, retry int) time.Duration {
	base := p.InitialDelaySeconds
	if base <= 0 {
		base = 2
	}
	max := p.MaxDelaySeconds
	if max <= 0 {
		max = 60
	}
	seconds := base
	for i := 0; i < retry; i++ {
		seconds *= 2
		if seconds >= max {
			seconds = max
			break
		}
	}
	return time.Duration(seconds) * time.Second
}

func (m *Manager) Stop(id string) error {
	m.mu.RLock()
	rt := m.runtime[id]
	_, knownDef := m.defs[id]
	m.mu.RUnlock()
	if rt == nil {
		if knownDef {
			return nil
		}
		return fmt.Errorf("unknown process %q", id)
	}

	rt.mu.Lock()
	rt.desired = false
	if rt.state == "stopped" {
		rt.mu.Unlock()
		return nil
	}
	pid := rt.pid
	if pid <= 0 {
		rt.state = "stopped"
		rt.mu.Unlock()
		return nil
	}
	rt.state = "stopping"
	rt.mu.Unlock()

	if err := platform.KillProcessTree(pid); err != nil {
		// The process may have exited between the state read and taskkill.
		rt.mu.Lock()
		alreadyStopped := rt.pid == 0 || rt.state == "stopped"
		rt.mu.Unlock()
		if !alreadyStopped {
			return err
		}
	}
	return nil
}

func (m *Manager) Restart(id string) error {
	if err := m.Stop(id); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.RLock()
		rt := m.runtime[id]
		m.mu.RUnlock()
		if rt == nil {
			return fmt.Errorf("unknown process %q", id)
		}
		rt.mu.Lock()
		stopped := rt.state == "stopped"
		rt.mu.Unlock()
		if stopped {
			return m.Start(id)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout stopping process %q", id)
}

type View struct {
	Definition model.ProcessDefinition `json:"definition"`
	Status     model.ProcessStatus     `json:"status"`
}

func (m *Manager) Views() []View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]View, 0, len(m.defs))
	for id, def := range m.defs {
		rt := m.runtime[id]
		status := model.ProcessStatus{ID: id, Name: def.Name, State: "stopped"}
		if rt != nil {
			rt.mu.Lock()
			status.State = rt.state
			status.PID = rt.pid
			status.StartedAt = rt.startedAt
			status.ExitCode = rt.exitCode
			status.RestartCount = rt.restartCount
			status.CurrentRunID = rt.currentRunID
			rt.mu.Unlock()
		}
		out = append(out, View{Definition: def, Status: status})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Definition.Name < out[j].Definition.Name
	})
	return out
}

func (m *Manager) LogPath(id string) (string, error) {
	m.mu.RLock()
	rt := m.runtime[id]
	m.mu.RUnlock()
	if rt == nil {
		return "", fmt.Errorf("unknown process %q", id)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.logPath == "" {
		return "", fmt.Errorf("process has no log yet")
	}
	return rt.logPath, nil
}
