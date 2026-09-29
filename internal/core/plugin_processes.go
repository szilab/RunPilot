package core

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/launcher"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
)

// pluginProcessManager owns short-lived plugin commands. It deliberately has
// no history: plugins persist any history they need through plugin storage.
type pluginProcessManager struct {
	mu    sync.RWMutex
	items map[string]*pluginProcess
	emit  func(string, string, any)
}
type pluginProcess struct {
	owner, id           string
	cmd                 *exec.Cmd
	started             time.Time
	mu                  sync.RWMutex
	running, terminated bool
	exitCode            *int
}
type pluginProcessStart struct {
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	WorkingDirectory string            `json:"workingDirectory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
}
type pluginProcessStatus struct {
	ID         string `json:"id"`
	Running    bool   `json:"running"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	Terminated bool   `json:"terminated"`
}

func newPluginProcessManager(emit func(string, string, any)) *pluginProcessManager {
	return &pluginProcessManager{items: map[string]*pluginProcess{}, emit: emit}
}
func (m *pluginProcessManager) start(owner string, input pluginProcessStart) (pluginProcessStatus, error) {
	cmd, err := launcher.Build(model.CommandSpec{Path: input.Command, Args: input.Args, WorkingDirectory: input.WorkingDirectory, Environment: input.Environment, Interpreter: "direct"})
	if err != nil {
		return pluginProcessStatus{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return pluginProcessStatus{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return pluginProcessStatus{}, err
	}
	if err := cmd.Start(); err != nil {
		return pluginProcessStatus{}, fmt.Errorf("start process: %w", err)
	}
	p := &pluginProcess{owner: owner, id: config.NewID("plugin-process"), cmd: cmd, started: time.Now(), running: true}
	m.mu.Lock()
	m.items[p.id] = p
	m.mu.Unlock()
	var readers sync.WaitGroup
	readers.Add(2)
	go m.copyOutput(p, "stdout", stdout, &readers)
	go m.copyOutput(p, "stderr", stderr, &readers)
	go func() {
		err := cmd.Wait()
		readers.Wait()
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		p.mu.Lock()
		p.running = false
		p.exitCode = &code
		terminated := p.terminated
		p.mu.Unlock()
		m.emit(owner, "process.exit", map[string]any{"id": p.id, "exitCode": code, "success": err == nil && code == 0, "terminated": terminated})
		time.AfterFunc(5*time.Minute, func() {
			m.mu.Lock()
			if current := m.items[p.id]; current == p {
				delete(m.items, p.id)
			}
			m.mu.Unlock()
		})
	}()
	return pluginProcessStatus{ID: p.id, Running: true}, nil
}
func (m *pluginProcessManager) copyOutput(p *pluginProcess, kind string, reader io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()
	queue := make(chan string, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for text := range queue {
			m.emit(p.owner, "process."+kind, map[string]any{"id": p.id, "text": text})
		}
	}()
	buf := make([]byte, 8192)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			text := string(append([]byte(nil), buf[:n]...))
			select {
			case queue <- text:
			default: /* bounded output: discard chunks while plugin is slow */
			}
		}
		if err != nil {
			break
		}
	}
	close(queue)
	<-done
}
func (m *pluginProcessManager) status(owner, id string) (pluginProcessStatus, error) {
	m.mu.RLock()
	p := m.items[id]
	m.mu.RUnlock()
	if p == nil || p.owner != owner {
		return pluginProcessStatus{}, fmt.Errorf("unknown process")
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return pluginProcessStatus{ID: id, Running: p.running, ExitCode: p.exitCode, Terminated: p.terminated}, nil
}
func (m *pluginProcessManager) terminate(owner, id string) (pluginProcessStatus, error) {
	m.mu.RLock()
	p := m.items[id]
	m.mu.RUnlock()
	if p == nil || p.owner != owner {
		return pluginProcessStatus{}, fmt.Errorf("unknown process")
	}
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return pluginProcessStatus{ID: id, Running: false, ExitCode: p.exitCode, Terminated: p.terminated}, nil
	}
	p.terminated = true
	pid := p.cmd.Process.Pid
	p.mu.Unlock()
	if err := platform.KillProcessTree(pid); err != nil {
		return pluginProcessStatus{}, err
	}
	return m.status(owner, id)
}
func (m *pluginProcessManager) stopOwner(owner string) {
	m.mu.RLock()
	ids := []string{}
	for id, p := range m.items {
		if p.owner == owner {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	for _, id := range ids {
		_, _ = m.terminate(owner, id)
	}
}
func (m *pluginProcessManager) close() {
	m.mu.RLock()
	owners := map[string]bool{}
	for _, p := range m.items {
		owners[p.owner] = true
	}
	m.mu.RUnlock()
	for owner := range owners {
		m.stopOwner(owner)
	}
}

var _ = context.Background
