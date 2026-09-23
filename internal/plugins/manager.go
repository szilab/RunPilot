package plugins

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/pluginapi"
	"gopkg.in/yaml.v3"
)

const startupTimeout = 10 * time.Second
const maxPluginLog = 512 << 10

type instance struct {
	manifest  Manifest
	dir       string
	mu        sync.Mutex
	cmd       *exec.Cmd
	done      chan error
	startedAt time.Time
	lastErr   error
	address   string
	secret    string
	ready     bool
	stopping  bool
}

type enabledLookup func(string) (enabled bool, configured bool)

type Manager struct {
	root            string
	lookup          enabledLookup
	mu              sync.RWMutex
	plugins         map[string]*instance
	discoveryErrors []string
}

func New(dataDir string, lookups ...enabledLookup) *Manager {
	var lookup enabledLookup
	if len(lookups) != 0 {
		lookup = lookups[0]
	}
	return &Manager{root: filepath.Join(dataDir, "plugins"), lookup: lookup, plugins: map[string]*instance{}}
}
func (m *Manager) Root() string { return m.root }
func (m *Manager) enabled(manifest Manifest) bool {
	if m.lookup != nil {
		if enabled, ok := m.lookup(manifest.ID); ok {
			return enabled
		}
	}
	return manifest.DefaultEnabled
}

func (m *Manager) Reload() []error {
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return []error{err}
	}
	discovered := map[string]*instance{}
	var errs []error
	// User/developer plugins take precedence over immutable bundled files. The
	// source tree is a test-only convenience and must not pollute manager unit
	// tests that intentionally supply their own plugin directory.
	roots := []string{m.root}
	if !hasPluginDirectory(m.root) {
		roots = append(roots, sourceRoot())
	}
	roots = append(roots, bundledRoot())
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			manifest, err := loadManifest(filepath.Join(dir, "plugin.yaml"))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", entry.Name(), err))
				continue
			}
			if _, exists := discovered[manifest.ID]; exists {
				continue
			}
			m.mu.RLock()
			old := m.plugins[manifest.ID]
			m.mu.RUnlock()
			if old != nil && old.isRunning() {
				old.mu.Lock()
				old.manifest, old.dir = manifest, dir
				old.mu.Unlock()
				discovered[manifest.ID] = old
			} else {
				discovered[manifest.ID] = &instance{manifest: manifest, dir: dir}
			}
		}
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
func hasPluginDirectory(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(root, entry.Name(), "plugin.yaml")); err == nil {
				return true
			}
		}
	}
	return false
}
func bundledRoot() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(executable), "plugins")
}
func sourceRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return filepath.Join(wd, "plugins")
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return ""
		}
		wd = parent
	}
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
		out = append(out, p.status(m.enabled(p.manifest)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out
}
func (m *Manager) StartEnabled() []error {
	m.mu.RLock()
	var ids []string
	for id, p := range m.plugins {
		if m.enabled(p.manifest) {
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
func (m *Manager) Manifest(id string) (Manifest, bool) {
	p, err := m.plugin(id)
	if err != nil {
		return Manifest{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.manifest, true
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
	if p.cmd != nil {
		p.mu.Unlock()
		return nil
	}
	executable, ok := p.manifest.Executable(runtime.GOOS, runtime.GOARCH)
	if !ok {
		p.lastErr = fmt.Errorf("plugin is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
		p.mu.Unlock()
		return p.lastErr
	}
	executable, err := resolveExecutable(p.dir, executable)
	developmentArgs, development := p.developmentCommand(executable)
	if err == nil {
		info, statErr := os.Stat(executable)
		err = statErr
		if err == nil && !info.Mode().IsRegular() {
			err = fmt.Errorf("plugin executable is not a regular file")
		}
	}
	if development {
		err = nil
	}
	if development {
		// go run leaves a compiler wrapper between us and the plugin process,
		// which makes reliable shutdown impossible. Build a short-lived test
		// binary instead so the manager remains the direct parent.
		temporary, makeErr := os.MkdirTemp("", "runpilot-plugin-test-")
		if makeErr != nil {
			p.lastErr = makeErr
			p.mu.Unlock()
			return makeErr
		}
		built := filepath.Join(temporary, "plugin")
		if runtime.GOOS == "windows" {
			built += ".exe"
		}
		build := exec.Command("go", "build", "-o", built, developmentArgs[1])
		build.Dir = filepath.Join(p.dir, "..", "..")
		if output, buildErr := build.CombinedOutput(); buildErr != nil {
			_ = os.RemoveAll(temporary)
			p.lastErr = fmt.Errorf("build development plugin: %w: %s", buildErr, strings.TrimSpace(string(output)))
			p.mu.Unlock()
			return p.lastErr
		}
		executable = built
		development = false
	}
	if err != nil {
		p.lastErr = err
		p.mu.Unlock()
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		p.lastErr = err
		p.mu.Unlock()
		return err
	}
	address := listener.Addr().String()
	_ = listener.Close()
	secret, err := newSecret()
	if err != nil {
		p.lastErr = err
		p.mu.Unlock()
		return err
	}
	file, err := os.OpenFile(filepath.Join(p.dir, "plugin.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		p.lastErr = err
		p.mu.Unlock()
		return err
	}
	cmd := exec.Command(executable, p.manifest.Args...)
	cmd.Dir = p.dir
	writer := &limitedWriter{w: file, n: maxPluginLog}
	cmd.Stdout = writer
	cmd.Stderr = writer
	cmd.Env = append(os.Environ(), "RUNPILOT_PLUGIN=1", fmt.Sprintf("RUNPILOT_PLUGIN_PROTOCOL=%d", ProtocolVersion), "RUNPILOT_PLUGIN_ID="+p.manifest.ID, "RUNPILOT_PLUGIN_LISTEN="+address, "RUNPILOT_PLUGIN_SECRET="+secret)
	for key, value := range p.manifest.Env {
		if validEnvKey(key) {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if err := cmd.Start(); err != nil {
		_ = file.Close()
		p.lastErr = err
		p.mu.Unlock()
		return err
	}
	p.cmd, p.done, p.startedAt, p.lastErr, p.address, p.secret, p.ready, p.stopping = cmd, make(chan error, 1), time.Now().UTC(), nil, address, secret, false, false
	done := p.done
	p.mu.Unlock()
	go func() {
		err := cmd.Wait()
		_ = file.Close()
		done <- err
		close(done)
		p.mu.Lock()
		if p.cmd == cmd {
			p.cmd = nil
			p.ready = false
			if !p.stopping {
				if err == nil {
					p.lastErr = errors.New("plugin exited unexpectedly")
				} else {
					p.lastErr = err
				}
			}
		}
		p.mu.Unlock()
	}()
	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var info pluginapi.Info
		err := control(address, secret, ctx, "GET", "/v1/info", nil, &info)
		if err == nil && info.ID == p.manifest.ID && info.ProtocolVersion == ProtocolVersion {
			var health pluginapi.Health
			err = control(address, secret, ctx, "GET", "/v1/health", nil, &health)
			if err == nil && health.Healthy {
				p.mu.Lock()
				if p.cmd == cmd {
					p.ready = true
				}
				p.mu.Unlock()
				cancel()
				return nil
			}
		}
		cancel()
		time.Sleep(75 * time.Millisecond)
	}
	p.mu.Lock()
	p.lastErr = fmt.Errorf("plugin did not become ready within %s", startupTimeout)
	p.mu.Unlock()
	_ = p.stop()
	return p.lastErr
}

// Source-tree execution exists only for Go test binaries. Production builds
// always resolve a packaged executable adjacent to its immutable manifest.
func (p *instance) developmentCommand(executable string) ([]string, bool) {
	if !strings.HasSuffix(os.Args[0], ".test") {
		return nil, false
	}
	if _, err := os.Stat(executable); err == nil {
		return nil, false
	}
	command := map[string]string{"remote.xpra": "./cmd/runpilot-plugin-remote-xpra", "remote.rdp": "./cmd/runpilot-plugin-remote-rdp", "remote.vnc": "./cmd/runpilot-plugin-remote-vnc"}[p.manifest.ID]
	if command == "" {
		return nil, false
	}
	return []string{"run", command}, true
}
func (p *instance) stop() error {
	p.mu.Lock()
	cmd, done := p.cmd, p.done
	if cmd == nil {
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
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
func (p *instance) isRunning() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.cmd != nil }
func (p *instance) status(enabled bool) Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, supported := p.manifest.Executable(runtime.GOOS, runtime.GOARCH)
	status := Status{Manifest: p.manifest, Enabled: enabled, PlatformSupported: supported}
	if !supported {
		status.State = StateUnavailable
		status.Message = "unsupported on this platform"
		return status
	}
	if !enabled && p.cmd == nil {
		status.State = StateStopped
		status.Message = "disabled"
		return status
	}
	if p.cmd != nil && p.cmd.Process != nil {
		status.State = StateRunning
		status.Healthy = p.ready
		status.PID = p.cmd.Process.Pid
		status.StartedAt = p.startedAt.Format(time.RFC3339)
		if !p.ready {
			status.Message = "starting"
		}
		return status
	}
	status.State = StateStopped
	if p.lastErr != nil {
		status.State = StateFailed
		status.Message = p.lastErr.Error()
	}
	return status
}

type limitedWriter struct {
	w  io.Writer
	n  int
	mu sync.Mutex
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	original := len(b)
	if l.n <= 0 {
		return original, nil
	}
	if len(b) > l.n {
		b = b[:l.n]
	}
	n, err := l.w.Write(b)
	l.n -= n
	if err != nil {
		return n, err
	}
	return original, nil
}
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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
	if manifest.LegacyEnabled != nil {
		manifest.DefaultEnabled = *manifest.LegacyEnabled
		manifest.LegacyEnabled = nil
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
