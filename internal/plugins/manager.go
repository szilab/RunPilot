package plugins

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type instance struct {
	manifest Manifest
	dir      string

	mu        sync.Mutex
	cmd       *exec.Cmd
	done      chan error
	startedAt time.Time
	lastErr   error
}

type Manager struct {
	root string

	mu              sync.RWMutex
	plugins         map[string]*instance
	discoveryErrors []string
}

func New(dataDir string) *Manager {
	return &Manager{
		root:    filepath.Join(dataDir, "plugins"),
		plugins: map[string]*instance{},
	}
}

func (m *Manager) Root() string { return m.root }

func (m *Manager) Reload() []error {
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return []error{err}
	}

	entries, err := os.ReadDir(m.root)
	if err != nil {
		return []error{err}
	}

	discovered := map[string]*instance{}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(m.root, entry.Name())
		manifest, err := loadManifest(filepath.Join(dir, "plugin.yaml"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", entry.Name(), err))
			continue
		}
		if _, exists := discovered[manifest.ID]; exists {
			errs = append(errs, fmt.Errorf("%s: duplicate plugin id %q", entry.Name(), manifest.ID))
			continue
		}

		m.mu.RLock()
		old := m.plugins[manifest.ID]
		m.mu.RUnlock()
		if old != nil && old.isRunning() {
			old.mu.Lock()
			old.manifest = manifest
			old.dir = dir
			old.mu.Unlock()
			discovered[manifest.ID] = old
			continue
		}
		discovered[manifest.ID] = &instance{manifest: manifest, dir: dir}
	}

	m.mu.Lock()
	for id, current := range m.plugins {
		if current.isRunning() {
			if _, exists := discovered[id]; !exists {
				discovered[id] = current
			}
		}
	}
	m.plugins = discovered
	m.discoveryErrors = make([]string, 0, len(errs))
	for _, err := range errs {
		m.discoveryErrors = append(m.discoveryErrors, err.Error())
	}
	m.mu.Unlock()
	return errs
}

func (m *Manager) DiscoveryErrors() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.discoveryErrors...)
}

func (m *Manager) Statuses() []Status {
	m.mu.RLock()
	instances := make([]*instance, 0, len(m.plugins))
	for _, p := range m.plugins {
		instances = append(instances, p)
	}
	m.mu.RUnlock()

	out := make([]Status, 0, len(instances))
	for _, p := range instances {
		out = append(out, p.status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out
}

func (m *Manager) StartEnabled() []error {
	m.mu.RLock()
	ids := make([]string, 0, len(m.plugins))
	for id, p := range m.plugins {
		if p.manifest.Enabled {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	sort.Strings(ids)

	var errs []error
	for _, id := range ids {
		if err := m.Start(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func (m *Manager) Start(id string) error {
	p, err := m.plugin(id)
	if err != nil {
		return err
	}
	return p.start()
}

func (m *Manager) Stop(id string) error {
	p, err := m.plugin(id)
	if err != nil {
		return err
	}
	return p.stop()
}

func (m *Manager) Close() error {
	m.mu.RLock()
	ids := make([]string, 0, len(m.plugins))
	for id := range m.plugins {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	sort.Strings(ids)

	var errs []error
	for _, id := range ids {
		if err := m.Stop(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) plugin(id string) (*instance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p := m.plugins[id]
	if p == nil {
		return nil, fmt.Errorf("unknown plugin %q", id)
	}
	return p, nil
}

func (p *instance) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return nil
	}

	executable, ok := p.manifest.Executable(runtime.GOOS, runtime.GOARCH)
	if !ok {
		p.lastErr = fmt.Errorf("plugin is not available on %s/%s", runtime.GOOS, runtime.GOARCH)
		return p.lastErr
	}
	executable, err := resolveExecutable(p.dir, executable)
	if err != nil {
		p.lastErr = err
		return err
	}
	info, err := os.Stat(executable)
	if err != nil {
		p.lastErr = err
		return fmt.Errorf("plugin %q executable: %w", p.manifest.ID, err)
	}
	if !info.Mode().IsRegular() {
		p.lastErr = fmt.Errorf("plugin executable is not a regular file")
		return p.lastErr
	}

	logFile, err := os.OpenFile(filepath.Join(p.dir, "plugin.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		p.lastErr = err
		return err
	}

	cmd := exec.Command(executable, p.manifest.Args...)
	cmd.Dir = p.dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(),
		"RUNPILOT_PLUGIN=1",
		fmt.Sprintf("RUNPILOT_PLUGIN_PROTOCOL=%d", ProtocolVersion),
		"RUNPILOT_PLUGIN_ID="+p.manifest.ID,
	)
	for key, value := range p.manifest.Env {
		if validEnvKey(key) {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		p.lastErr = err
		return err
	}

	p.cmd = cmd
	p.done = make(chan error, 1)
	p.startedAt = time.Now().UTC()
	p.lastErr = nil
	go func(done chan error, cmd *exec.Cmd, logFile io.Closer) {
		err := cmd.Wait()
		_ = logFile.Close()
		done <- err
		close(done)
		p.mu.Lock()
		if p.cmd == cmd {
			p.cmd = nil
			p.lastErr = err
		}
		p.mu.Unlock()
	}(p.done, cmd, logFile)
	return nil
}

func (p *instance) stop() error {
	p.mu.Lock()
	cmd := p.cmd
	done := p.done
	if cmd == nil {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	if cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
	}
	select {
	case <-done:
		return nil
	case <-time.After(3 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return nil
	}
}

func (p *instance) isRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}

func (p *instance) status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()

	status := Status{Manifest: p.manifest, State: StateStopped}
	if p.cmd != nil && p.cmd.Process != nil {
		status.State = StateRunning
		status.PID = p.cmd.Process.Pid
		status.StartedAt = p.startedAt.Format(time.RFC3339)
		return status
	}
	if p.lastErr != nil {
		status.State = StateFailed
		status.Message = p.lastErr.Error()
	}
	return status
}

func loadManifest(path string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()

	var manifest Manifest
	decoder := yaml.NewDecoder(bufio.NewReader(f))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func resolveExecutable(dir, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", fmt.Errorf("plugin executable is required")
	}
	if filepath.IsAbs(configured) {
		return "", fmt.Errorf("plugin executable must be relative to the plugin directory")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.Abs(filepath.Join(root, filepath.Clean(configured)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin executable escapes plugin directory")
	}
	return resolved, nil
}
