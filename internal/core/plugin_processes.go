package core

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/history"
	"github.com/szilab/RunPilot/internal/launcher"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/plugins"
)

const (
	maxPluginProcessTimeoutSeconds = 7 * 24 * 3600
	maxPluginProcessArgs           = 1024
	maxPluginProcessEnvironment    = 1024
	maxPluginProcessString         = 32 << 10
	// A child that leaves grandchildren holding its output pipe must not keep
	// the exit event from being delivered.
	pluginProcessWaitDelay     = 2 * time.Second
	pluginOutputNotifyInterval = 250 * time.Millisecond
)

// pluginProcessManager owns plugin commands. It keeps no history of its own:
// output that must be kept is captured into an owner-scoped history execution
// when the plugin supplies one.
type pluginProcessManager struct {
	mu       sync.RWMutex
	items    map[string]*pluginProcess
	captured map[string]bool // owner\x00execution currently receiving output
	emit     func(string, string, any)
	history  *history.Store
	publish  func(owner, event string, data any) // browser fanout, never the plugin
	wg       sync.WaitGroup
}
type pluginProcess struct {
	owner, id           string
	cmd                 *exec.Cmd
	started             time.Time
	mu                  sync.RWMutex
	running, terminated bool
	timedOut            bool
	exitCode            *int
}
type pluginProcessStart struct {
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	WorkingDirectory string            `json:"workingDirectory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	// Interpreter uses the launcher's CommandSpec semantics. Empty means
	// "direct", which is what plugins received before interpreters existed.
	Interpreter string `json:"interpreter,omitempty"`
	// TimeoutSeconds kills the process tree after this long; 0 means none.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// HistoryID captures combined stdout/stderr into that owner's unfinished
	// history execution instead of delivering process.stdout/stderr events.
	HistoryID string `json:"historyId,omitempty"`
}
type pluginProcessStatus struct {
	ID         string `json:"id"`
	PID        int    `json:"pid,omitempty"`
	Running    bool   `json:"running"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	Terminated bool   `json:"terminated"`
	TimedOut   bool   `json:"timedOut,omitempty"`
}

func newPluginProcessManager(emit func(string, string, any)) *pluginProcessManager {
	return &pluginProcessManager{items: map[string]*pluginProcess{}, captured: map[string]bool{}, emit: emit}
}

func invalidProcess(format string, args ...any) error {
	return &plugins.HostFailure{Code: "invalid_argument", Message: fmt.Sprintf(format, args...)}
}

func validatePluginProcessStart(input pluginProcessStart) error {
	if input.Command == "" {
		return invalidProcess("process command is required")
	}
	if len(input.Command) > maxPluginProcessString || len(input.WorkingDirectory) > maxPluginProcessString {
		return invalidProcess("process command or working directory is too long")
	}
	if len(input.Args) > maxPluginProcessArgs || len(input.Environment) > maxPluginProcessEnvironment {
		return invalidProcess("process has too many arguments or environment variables")
	}
	for _, arg := range input.Args {
		if len(arg) > maxPluginProcessString {
			return invalidProcess("process argument is too long")
		}
	}
	for name, value := range input.Environment {
		if len(name) > 1024 || len(value) > maxPluginProcessString {
			return invalidProcess("process environment variable is too long")
		}
	}
	if input.TimeoutSeconds < 0 || input.TimeoutSeconds > maxPluginProcessTimeoutSeconds {
		return invalidProcess("timeoutSeconds must be between 0 and %d", maxPluginProcessTimeoutSeconds)
	}
	if err := model.ValidateEnvironment(input.Environment); err != nil {
		return invalidProcess("%v", err)
	}
	return nil
}

func (m *pluginProcessManager) start(owner string, input pluginProcessStart) (pluginProcessStatus, error) {
	if err := validatePluginProcessStart(input); err != nil {
		return pluginProcessStatus{}, err
	}
	interpreter := input.Interpreter
	if interpreter == "" {
		interpreter = "direct"
	}
	cmd, err := launcher.Build(model.CommandSpec{Path: input.Command, Args: input.Args, WorkingDirectory: input.WorkingDirectory, Environment: input.Environment, Interpreter: interpreter})
	if err != nil {
		return pluginProcessStatus{}, &plugins.HostFailure{Code: "invalid_argument", Message: err.Error()}
	}
	p := &pluginProcess{owner: owner, id: config.NewID("plugin-process"), cmd: cmd}
	var stdout, stderr io.ReadCloser
	var capture *history.ExecutionLog
	var notifier *outputNotifier
	captureKey := owner + "\x00" + input.HistoryID
	if input.HistoryID != "" {
		if m.history == nil {
			return pluginProcessStatus{}, &plugins.HostFailure{Code: "unavailable", Message: "execution history is unavailable"}
		}
		m.mu.Lock()
		busy := m.captured[captureKey]
		if !busy {
			m.captured[captureKey] = true
		}
		m.mu.Unlock()
		if busy {
			return pluginProcessStatus{}, invalidProcess("execution is already capturing a process")
		}
		notifier = newOutputNotifier(func(size int64) {
			if m.publish != nil {
				m.publish(owner, "history.output", map[string]any{"id": input.HistoryID, "size": size})
			}
		})
		capture, err = m.history.OpenExecutionLog(owner, input.HistoryID, notifier.notify)
		if err != nil {
			m.releaseCapture(captureKey)
			return pluginProcessStatus{}, &plugins.HostFailure{Code: "invalid_argument", Message: "historyId: " + err.Error()}
		}
		cmd.Stdout, cmd.Stderr = capture, capture
		cmd.WaitDelay = pluginProcessWaitDelay
	} else {
		if stdout, err = cmd.StdoutPipe(); err != nil {
			return pluginProcessStatus{}, err
		}
		if stderr, err = cmd.StderrPipe(); err != nil {
			return pluginProcessStatus{}, err
		}
	}
	if err := cmd.Start(); err != nil {
		if capture != nil {
			_ = capture.Close()
			notifier.stop()
			m.releaseCapture(captureKey)
		}
		return pluginProcessStatus{}, fmt.Errorf("start process: %w", err)
	}
	p.started, p.running = time.Now(), true
	pid := cmd.Process.Pid
	m.mu.Lock()
	m.items[p.id] = p
	m.mu.Unlock()
	var timeout *time.Timer
	if input.TimeoutSeconds > 0 {
		timeout = time.AfterFunc(time.Duration(input.TimeoutSeconds)*time.Second, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.running {
				p.timedOut = true
				_ = platform.KillProcessTree(pid)
			}
		})
	}
	var readers sync.WaitGroup
	if capture == nil {
		readers.Add(2)
		go m.copyOutput(p, "stdout", stdout, &readers)
		go m.copyOutput(p, "stderr", stderr, &readers)
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		err := cmd.Wait()
		if timeout != nil {
			timeout.Stop()
		}
		readers.Wait()
		if capture != nil {
			_ = capture.Close()
			notifier.stop()
			m.releaseCapture(captureKey)
			if errors.Is(err, exec.ErrWaitDelay) {
				err = nil // output pipe was merely held open by a grandchild.
			}
		}
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		p.mu.Lock()
		p.running = false
		p.exitCode = &code
		terminated, timedOut := p.terminated, p.timedOut
		p.mu.Unlock()
		event := map[string]any{"id": p.id, "exitCode": code, "success": err == nil && code == 0, "terminated": terminated, "timedOut": timedOut}
		if input.HistoryID != "" {
			event["historyId"] = input.HistoryID
		}
		m.emit(owner, "process.exit", event)
		time.AfterFunc(5*time.Minute, func() {
			m.mu.Lock()
			if current := m.items[p.id]; current == p {
				delete(m.items, p.id)
			}
			m.mu.Unlock()
		})
	}()
	return pluginProcessStatus{ID: p.id, PID: pid, Running: true}, nil
}

func (m *pluginProcessManager) releaseCapture(key string) {
	m.mu.Lock()
	delete(m.captured, key)
	m.mu.Unlock()
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

func (m *pluginProcessManager) lookup(owner, id string) (*pluginProcess, error) {
	m.mu.RLock()
	p := m.items[id]
	m.mu.RUnlock()
	if p == nil || p.owner != owner {
		return nil, &plugins.HostFailure{Code: "not_found", Message: "unknown process"}
	}
	return p, nil
}

func (m *pluginProcessManager) status(owner, id string) (pluginProcessStatus, error) {
	p, err := m.lookup(owner, id)
	if err != nil {
		return pluginProcessStatus{}, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	status := pluginProcessStatus{ID: id, Running: p.running, ExitCode: p.exitCode, Terminated: p.terminated, TimedOut: p.timedOut}
	if p.running {
		status.PID = p.cmd.Process.Pid
	}
	return status, nil
}

func (m *pluginProcessManager) terminate(owner, id string) (pluginProcessStatus, error) {
	p, err := m.lookup(owner, id)
	if err != nil {
		return pluginProcessStatus{}, err
	}
	p.mu.Lock()
	if !p.running {
		status := pluginProcessStatus{ID: id, Running: false, ExitCode: p.exitCode, Terminated: p.terminated, TimedOut: p.timedOut}
		p.mu.Unlock()
		return status, nil
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

// close terminates every plugin process and waits (bounded) for exit
// bookkeeping so captured logs are flushed before history closes.
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
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// outputNotifier coalesces output-growth notifications so a chatty process
// produces a bounded number of browser events.
type outputNotifier struct {
	mu      sync.Mutex
	publish func(int64)
	last    time.Time
	timer   *time.Timer
	size    int64
	pending bool
	stopped bool
}

func newOutputNotifier(publish func(int64)) *outputNotifier {
	return &outputNotifier{publish: publish}
}

func (n *outputNotifier) notify(size int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stopped {
		return
	}
	n.size = size
	wait := pluginOutputNotifyInterval - time.Since(n.last)
	if wait <= 0 {
		n.last, n.pending = time.Now(), false
		n.publish(size)
		return
	}
	n.pending = true
	if n.timer == nil {
		n.timer = time.AfterFunc(wait, n.flush)
	}
}

func (n *outputNotifier) flush() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.timer = nil
	if n.stopped || !n.pending {
		return
	}
	n.last, n.pending = time.Now(), false
	n.publish(n.size)
}

// stop delivers any coalesced update once, then silences the notifier.
func (n *outputNotifier) stop() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stopped {
		return
	}
	n.stopped = true
	if n.timer != nil {
		n.timer.Stop()
	}
	if n.pending {
		n.publish(n.size)
	}
}
