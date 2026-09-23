package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type enabledLookup func(string) (enabled bool, configured bool)

type installedPlugin struct {
	manifest Manifest
	dir      string
	message  error
}

type FrontendExtension struct {
	ID         string `json:"id"`
	Module     string `json:"module"`
	Stylesheet string `json:"stylesheet,omitempty"`
}

// Manager discovers immutable installed packages. It deliberately has no
// process, socket, token, or executable lifecycle.
type Manager struct {
	root            string
	lookup          enabledLookup
	mu              sync.RWMutex
	plugins         map[string]*installedPlugin
	discoveryErrors []string
	restartRequired bool
}

func New(dataDir string, lookups ...enabledLookup) *Manager {
	var lookup enabledLookup
	if len(lookups) > 0 {
		lookup = lookups[0]
	}
	return &Manager{root: filepath.Join(dataDir, "plugins"), lookup: lookup, plugins: map[string]*installedPlugin{}}
}

func (m *Manager) Root() string { return m.root }

func (m *Manager) Reload() []error {
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return []error{err}
	}
	discovered := map[string]*installedPlugin{}
	var errs []error
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return []error{err}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		idRoot := filepath.Join(m.root, entry.Name())
		versions, readErr := os.ReadDir(idRoot)
		if readErr != nil {
			errs = append(errs, readErr)
			continue
		}
		for i := len(versions) - 1; i >= 0; i-- {
			if !versions[i].IsDir() {
				continue
			}
			manifest, manifestErr := loadManifest(filepath.Join(idRoot, versions[i].Name(), "plugin.yaml"))
			if manifestErr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", entry.Name(), manifestErr))
				continue
			}
			discovered[manifest.ID] = &installedPlugin{manifest: manifest, dir: filepath.Join(idRoot, versions[i].Name())}
			break
		}
	}
	m.mu.Lock()
	m.plugins = discovered
	m.discoveryErrors = m.discoveryErrors[:0]
	for _, discoveryErr := range errs {
		m.discoveryErrors = append(m.discoveryErrors, discoveryErr.Error())
	}
	m.mu.Unlock()
	return errs
}

func (m *Manager) DiscoveryErrors() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.discoveryErrors...)
}

func (m *Manager) enabled(manifest Manifest) bool {
	if m.lookup != nil {
		if enabled, configured := m.lookup(manifest.ID); configured {
			return enabled
		}
	}
	return false
}

func (m *Manager) Statuses() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Status, 0, len(m.plugins))
	for _, plugin := range m.plugins {
		enabled := m.enabled(plugin.manifest)
		state := StateInstalled
		if enabled {
			state = StateEnabled
		}
		if plugin.message != nil {
			state = StateFailed
		}
		out = append(out, Status{Manifest: plugin.manifest, Enabled: enabled, State: state, Message: errorString(plugin.message), RestartRequired: m.restartRequired})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out
}

// Start and Stop retain only the manager's narrow lifecycle API while
// activation remains restart-only; neither starts a process.
func (m *Manager) StartEnabled() []error { return nil }
func (m *Manager) Start(id string) error {
	if _, ok := m.Manifest(id); !ok {
		return fmt.Errorf("unknown plugin %q", id)
	}
	return nil
}
func (m *Manager) Stop(id string) error {
	if _, ok := m.Manifest(id); !ok {
		return fmt.Errorf("unknown plugin %q", id)
	}
	return nil
}
func (m *Manager) Close() error { return nil }

func (m *Manager) Manifest(id string) (Manifest, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	plugin := m.plugins[id]
	if plugin == nil {
		return Manifest{}, false
	}
	return plugin.manifest, true
}

func (m *Manager) AssetPath(id, relative string) (string, error) {
	m.mu.RLock()
	plugin := m.plugins[id]
	m.mu.RUnlock()
	if plugin == nil {
		return "", fmt.Errorf("unknown plugin %q", id)
	}
	if !safePackagePath(relative) {
		return "", fmt.Errorf("invalid plugin asset path")
	}
	root, err := filepath.Abs(plugin.dir)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin asset path escapes installation")
	}
	return target, nil
}

func (m *Manager) FrontendExtensions() []FrontendExtension {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]FrontendExtension, 0, len(m.plugins))
	for id, plugin := range m.plugins {
		if !m.enabled(plugin.manifest) || plugin.manifest.Frontend == nil {
			continue
		}
		result = append(result, FrontendExtension{ID: id, Module: "/plugins/" + id + "/" + plugin.manifest.Frontend.Module, Stylesheet: stylesheetURL(id, plugin.manifest.Frontend.Stylesheet)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func stylesheetURL(id, stylesheet string) string {
	if stylesheet == "" {
		return ""
	}
	return "/plugins/" + id + "/" + stylesheet
}

func (m *Manager) SetRestartRequired(value bool) {
	m.mu.Lock()
	m.restartRequired = value
	m.mu.Unlock()
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func loadManifest(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	var manifest Manifest
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
