package core

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/plugins"
	"github.com/szilab/RunPilot/internal/processsession"
)

const (
	maxPluginSessionsPerOwner = 8
	maxPluginSessionsGlobal   = 32
	maxPluginSessionLifetime  = 12 * time.Hour
	maxPluginSessionInput     = 16 << 10
	maxPluginSessionSpec      = 1 << 20
	maxPluginSessionOutput    = 8 << 10
	maxPluginSessionRows      = 300
	maxPluginSessionColumns   = 500
	pluginSessionGrace        = 5 * time.Second
	pluginSessionRetention    = 5 * time.Minute
)

type pluginSessionStart struct {
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	WorkingDirectory string            `json:"workingDirectory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Size             struct {
		Rows    uint16 `json:"rows"`
		Columns uint16 `json:"columns"`
	} `json:"size"`
}
type pluginSessionStatus struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Reason   string `json:"reason,omitempty"`
}
type pluginSessionEventEmitter func(owner, event string, data any) bool

type pluginSessionManager struct {
	mu     sync.RWMutex
	items  map[string]*pluginSession
	emit   pluginSessionEventEmitter
	closed bool
	wg     sync.WaitGroup
}
type pluginSession struct {
	owner, id       string
	manager         *pluginSessionManager
	process         *processsession.Session
	started         time.Time
	mu              sync.RWMutex
	state, reason   string
	requestedReason string
	exitCode        *int
	sequence        uint64
	finishOnce      sync.Once
	lifetime        *time.Timer
	finished        time.Time
	emit            pluginSessionEventEmitter
}

func newPluginSessionManager(emit pluginSessionEventEmitter) *pluginSessionManager {
	return &pluginSessionManager{items: map[string]*pluginSession{}, emit: emit}
}
func sessionInvalid(message string) error {
	return &plugins.HostFailure{Code: "invalid_argument", Message: message}
}
func sessionNotFound() error {
	return &plugins.HostFailure{Code: "not_found", Message: "unknown process session"}
}

func validatePluginSessionStart(in pluginSessionStart) error {
	if strings.TrimSpace(in.Command) == "" {
		return sessionInvalid("command is required")
	}
	if len(in.Command) > maxPluginProcessString || len(in.WorkingDirectory) > maxPluginProcessString {
		return sessionInvalid("command or working directory is too long")
	}
	if len(in.Args) > maxPluginProcessArgs || len(in.Environment) > maxPluginProcessEnvironment {
		return sessionInvalid("too many arguments or environment variables")
	}
	total := len(in.Command) + len(in.WorkingDirectory)
	for _, arg := range in.Args {
		if len(arg) > maxPluginProcessString {
			return sessionInvalid("argument is too long")
		}
		total += len(arg)
	}
	for key, value := range in.Environment {
		if len(key) > 1024 || len(value) > maxPluginProcessString {
			return sessionInvalid("environment variable is too long")
		}
		total += len(key) + len(value)
	}
	if total > maxPluginSessionSpec {
		return sessionInvalid("process session specification exceeds 1 MiB")
	}
	if err := model.ValidateEnvironment(in.Environment); err != nil {
		return sessionInvalid(err.Error())
	}
	if in.Size.Rows == 0 || in.Size.Columns == 0 || in.Size.Rows > maxPluginSessionRows || in.Size.Columns > maxPluginSessionColumns {
		return sessionInvalid("terminal size must be between 1x1 and 300x500")
	}
	return nil
}
func (m *pluginSessionManager) create(owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in pluginSessionStart
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, sessionInvalid("invalid create request")
	}
	if err := validatePluginSessionStart(in); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, &plugins.HostFailure{Code: "unavailable", Message: "process session manager is shutting down"}
	}
	owned, global := 0, 0
	for _, item := range m.items {
		if item.isRunning() {
			global++
			if item.owner == owner {
				owned++
			}
		}
	}
	if owned >= maxPluginSessionsPerOwner || global >= maxPluginSessionsGlobal {
		m.mu.Unlock()
		return nil, &plugins.HostFailure{Code: "resource_limit", Message: "process session limit reached"}
	}
	id := config.NewID("session")
	proc, err := processsession.Start(in.Command, in.Args, in.WorkingDirectory, processSessionEnvironment(os.Environ(), in.Environment), in.Size.Rows, in.Size.Columns)
	if err != nil {
		m.mu.Unlock()
		return nil, &plugins.HostFailure{Code: "failed", Message: "could not start interactive process"}
	}
	item := &pluginSession{owner: owner, id: id, manager: m, process: proc, started: time.Now(), state: "running", emit: m.emit}
	m.items[id] = item
	item.lifetime = time.AfterFunc(maxPluginSessionLifetime, func() { item.requestStop("timed_out", true) })
	m.wg.Add(2)
	m.mu.Unlock()
	outputDone := make(chan struct{})
	go m.readOutput(item, outputDone)
	go func() {
		defer m.wg.Done()
		err := proc.Wait()
		// Let the reader drain bytes already buffered by the PTY. Closing the
		// pseudoconsole then releases readers on platforms that signal EOF only
		// when the host endpoint closes.
		select {
		case <-outputDone:
		case <-time.After(100 * time.Millisecond):
		}
		_ = proc.Close()
		<-outputDone
		item.mu.RLock()
		requested := item.requestedReason
		item.mu.RUnlock()
		if err != nil {
			var exitErr interface{ ExitCode() int }
			if errors.As(err, &exitErr) {
				code := exitErr.ExitCode()
				var exitCode *int
				if code >= 0 {
					exitCode = &code
				}
				if requested == "" {
					requested = "exited"
				}
				item.finish(requested, exitCode, false)
				return
			}
		}
		code, ok := proc.ExitCode()
		var exitCode *int
		if ok {
			exitCode = &code
		}
		if requested == "" {
			requested = "exited"
		}
		item.finish(requested, exitCode, false)
	}()
	return json.Marshal(pluginSessionStatus{ID: id, State: "running"})
}
func mergeSessionEnvironment(inherited []string, values map[string]string) []string {
	result := append([]string(nil), inherited...)
	for name, value := range values {
		replaced := false
		for i, item := range result {
			key, _, _ := strings.Cut(item, "=")
			if (runtime.GOOS == "windows" && strings.EqualFold(key, name)) || key == name {
				result[i] = name + "=" + value
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, name+"="+value)
		}
	}
	return result
}
func processSessionEnvironment(inherited []string, values map[string]string) []string {
	result := mergeSessionEnvironment(inherited, values)
	if runtime.GOOS == "windows" {
		return result
	}
	has := func(name string) bool {
		for _, entry := range result {
			key, _, _ := strings.Cut(entry, "=")
			if key == name {
				return true
			}
		}
		return false
	}
	if !has("TERM") {
		result = append(result, "TERM=xterm-256color")
	}
	if !has("COLORTERM") {
		result = append(result, "COLORTERM=truecolor")
	}
	return result
}
func (m *pluginSessionManager) readOutput(item *pluginSession, done chan<- struct{}) {
	defer m.wg.Done()
	defer close(done)
	buf := make([]byte, maxPluginSessionOutput)
	for {
		n, err := item.process.Read(buf)
		if n > 0 {
			item.mu.Lock()
			item.sequence++
			seq := item.sequence
			item.mu.Unlock()
			data := map[string]any{"id": item.id, "sequence": seq, "data": base64.StdEncoding.EncodeToString(append([]byte(nil), buf[:n]...))}
			if !m.emit(item.owner, "process.session.output", data) {
				item.finish("io_error", nil, true)
				return
			}
		}
		if err != nil {
			select {
			case <-item.process.Done():
			case <-time.After(100 * time.Millisecond):
				if item.isRunning() {
					item.finish("io_error", nil, true)
				}
			}
			return
		}
	}
}
func (s *pluginSession) isRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state == "running"
}
func (s *pluginSession) requestStop(reason string, force bool) {
	s.mu.Lock()
	if s.state != "running" {
		s.mu.Unlock()
		return
	}
	if s.requestedReason == "" {
		s.requestedReason = reason
	}
	s.mu.Unlock()
	var err error
	if force {
		err = s.process.Force()
	} else {
		err = s.process.Graceful()
	}
	if err != nil {
		s.finish(reason, nil, true)
	}
}
func (s *pluginSession) finish(reason string, code *int, force bool) {
	s.finishOnce.Do(func() {
		if force {
			_ = s.process.Force()
		}
		s.mu.Lock()
		s.state = "exited"
		s.reason = reason
		s.exitCode = code
		s.finished = time.Now()
		if s.lifetime != nil {
			s.lifetime.Stop()
		}
		s.mu.Unlock()
		if reason == "io_error" {
			_ = s.process.Close()
			_ = s.emit(s.owner, "process.session.error", map[string]any{"id": s.id, "code": "io_error", "message": "interactive process session I/O failed"})
		}
		_ = s.emit(s.owner, "process.session.exit", pluginSessionStatus{ID: s.id, State: "exited", ExitCode: code, Reason: reason})
		time.AfterFunc(pluginSessionRetention, func() {
			s.manager.mu.Lock()
			if s.manager.items[s.id] == s {
				delete(s.manager.items, s.id)
			}
			s.manager.mu.Unlock()
		})
	})
}
func (m *pluginSessionManager) lookup(owner, id string) (*pluginSession, error) {
	m.mu.RLock()
	item := m.items[id]
	m.mu.RUnlock()
	if item == nil || item.owner != owner {
		return nil, sessionNotFound()
	}
	return item, nil
}
func decodeSessionID(raw json.RawMessage) (string, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.ID) == "" {
		return "", sessionInvalid("id is required")
	}
	return in.ID, nil
}
func (m *pluginSessionManager) write(owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		ID   string `json:"id"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ID == "" {
		return nil, sessionInvalid("id and base64 data are required")
	}
	if len(in.Data) == 0 || len(in.Data) > base64.StdEncoding.EncodedLen(maxPluginSessionInput) {
		return nil, sessionInvalid("data must contain 1 to 16384 decoded bytes")
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil || len(data) == 0 || len(data) > maxPluginSessionInput {
		return nil, sessionInvalid("data must contain 1 to 16384 decoded bytes")
	}
	item, err := m.lookup(owner, in.ID)
	if err != nil {
		return nil, err
	}
	if !item.isRunning() {
		return nil, &plugins.HostFailure{Code: "failed_precondition", Message: "process session is not running"}
	}
	if _, err = item.process.Write(data); err != nil {
		item.finish("io_error", nil, true)
		return nil, &plugins.HostFailure{Code: "failed", Message: "could not write process session input"}
	}
	return json.RawMessage(`{}`), nil
}
func (m *pluginSessionManager) resize(owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		ID      string `json:"id"`
		Rows    uint16 `json:"rows"`
		Columns uint16 `json:"columns"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ID == "" {
		return nil, sessionInvalid("id, rows and columns are required")
	}
	if in.Rows == 0 || in.Columns == 0 || in.Rows > maxPluginSessionRows || in.Columns > maxPluginSessionColumns {
		return nil, sessionInvalid("terminal size must be between 1x1 and 300x500")
	}
	item, err := m.lookup(owner, in.ID)
	if err != nil {
		return nil, err
	}
	if !item.isRunning() {
		return nil, &plugins.HostFailure{Code: "failed_precondition", Message: "process session is not running"}
	}
	if err = item.process.Resize(in.Rows, in.Columns); err != nil {
		item.finish("io_error", nil, true)
		return nil, &plugins.HostFailure{Code: "failed", Message: "could not resize process session"}
	}
	return json.RawMessage(`{}`), nil
}
func (m *pluginSessionManager) status(owner string, raw json.RawMessage) (json.RawMessage, error) {
	id, err := decodeSessionID(raw)
	if err != nil {
		return nil, err
	}
	item, err := m.lookup(owner, id)
	if err != nil {
		return nil, err
	}
	item.mu.RLock()
	status := pluginSessionStatus{ID: item.id, State: item.state, ExitCode: item.exitCode, Reason: item.reason}
	item.mu.RUnlock()
	return json.Marshal(status)
}
func (m *pluginSessionManager) terminate(owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		ID    string `json:"id"`
		Force bool   `json:"force"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.ID == "" {
		return nil, sessionInvalid("id is required")
	}
	item, err := m.lookup(owner, in.ID)
	if err != nil {
		return nil, err
	}
	if !item.isRunning() {
		return m.status(owner, raw)
	}
	if in.Force {
		item.requestStop("terminated", true)
	} else {
		item.requestStop("terminated", false)
		time.AfterFunc(pluginSessionGrace, func() { item.requestStop("terminated", true) })
	}
	return m.status(owner, raw)
}
func (m *pluginSessionManager) deliveryFailed(owner, id string) {
	item, err := m.lookup(owner, id)
	if err == nil {
		item.finish("io_error", nil, true)
	}
}
func (m *pluginSessionManager) stopOwner(owner string) {
	m.mu.RLock()
	var items []*pluginSession
	for _, item := range m.items {
		if item.owner == owner {
			items = append(items, item)
		}
	}
	m.mu.RUnlock()
	for _, item := range items {
		item.requestStop("terminated", true)
	}
}
func (m *pluginSessionManager) close() {
	m.mu.Lock()
	m.closed = true
	items := make([]*pluginSession, 0, len(m.items))
	for _, item := range m.items {
		items = append(items, item)
	}
	m.mu.Unlock()
	for _, item := range items {
		item.requestStop("terminated", true)
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}
