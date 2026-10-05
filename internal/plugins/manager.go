package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	active          map[string]*installedPlugin
	frozen          bool
	loaded          map[string]bool
}

func New(dataDir string, lookups ...enabledLookup) *Manager {
	var lookup enabledLookup
	if len(lookups) > 0 {
		lookup = lookups[0]
	}
	return &Manager{root: filepath.Join(dataDir, "plugins"), lookup: lookup, plugins: map[string]*installedPlugin{}, loaded: map[string]bool{}}
}

func (m *Manager) Root() string { return m.root }

func (m *Manager) Reload() []error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
		if strings.HasPrefix(entry.Name(), ".") {
			if !m.frozen && strings.HasPrefix(entry.Name(), ".removed-") {
				if err := os.RemoveAll(filepath.Join(m.root, entry.Name())); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		if !entry.IsDir() {
			continue
		}
		idRoot := filepath.Join(m.root, entry.Name())
		versions, readErr := os.ReadDir(filepath.Join(idRoot, "releases"))
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			errs = append(errs, readErr)
			continue
		}
		validVersions := versions[:0]
		for _, v := range versions {
			if v.IsDir() {
				if err := ValidateVersion(v.Name()); err == nil {
					validVersions = append(validVersions, v)
				} else {
					errs = append(errs, fmt.Errorf("%s: %w", entry.Name(), err))
				}
			}
		}
		versions = validVersions
		sort.Slice(versions, func(i, j int) bool { return versionBefore(versions[i].Name(), versions[j].Name()) })
		for i := len(versions) - 1; i >= 0; i-- {
			if !versions[i].IsDir() {
				continue
			}
			manifest, manifestErr := loadManifest(filepath.Join(idRoot, "releases", versions[i].Name(), "plugin.yaml"))
			if manifestErr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", entry.Name(), manifestErr))
				continue
			}
			if manifest.ID != entry.Name() || manifest.Version != versions[i].Name() {
				errs = append(errs, fmt.Errorf("package directory does not match manifest: %s/%s", entry.Name(), versions[i].Name()))
				continue
			}
			discovered[manifest.ID] = &installedPlugin{manifest: manifest, dir: filepath.Join(idRoot, "releases", versions[i].Name())}
			break
		}
	}
	for id, p := range discovered {
		if old := m.plugins[id]; old != nil && old.manifest.Version == p.manifest.Version {
			p.message = old.message
		}
	}
	m.plugins = discovered
	m.discoveryErrors = m.discoveryErrors[:0]
	for _, discoveryErr := range errs {
		m.discoveryErrors = append(m.discoveryErrors, discoveryErr.Error())
	}
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
		if !plugin.manifest.CompatibleHere() {
			state = StateIncompatible
		} else if enabled {
			state = StateEnabled
		}
		if plugin.message != nil {
			state = StateFailed
		}
		message := errorString(plugin.message)
		if state == StateIncompatible {
			message = errorString(plugin.manifest.CompatibilityError(runtime.GOOS))
		}
		var source *InstallSource
		if data, err := os.ReadFile(filepath.Join(plugin.dir, ".runpilot-source.json")); err == nil {
			var v InstallSource
			if json.Unmarshal(data, &v) == nil && v.ID == plugin.manifest.ID && v.Version == plugin.manifest.Version {
				source = &v
			}
		}
		if m.loaded[plugin.manifest.ID] && state == StateEnabled {
			state = StateLoaded
		}
		loadedVersion := ""
		if active := m.active[plugin.manifest.ID]; active != nil {
			loadedVersion = active.manifest.Version
		}
		out = append(out, Status{LoadedVersion: loadedVersion, Source: source, Loaded: m.loaded[plugin.manifest.ID], InstalledVersion: plugin.manifest.Version, Manifest: plugin.manifest, Enabled: enabled, State: state, Message: message, RestartRequired: m.restartRequired})
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

// PackageDir returns the immutable installed package directory. It is used by
// the generic runtime loader; callers must not derive paths from plugin IDs.
func (m *Manager) PackageDir(id string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	plugin := m.plugins[id]
	if plugin == nil {
		return "", false
	}
	return plugin.dir, true
}

func (m *Manager) AssetPath(id, relative string) (string, error) {
	m.mu.RLock()
	plugin := m.plugins[id]
	if m.frozen {
		plugin = m.active[id]
	}
	if plugin != nil {
		copy := *plugin
		plugin = &copy
	}
	m.mu.RUnlock()
	if plugin == nil {
		return "", fmt.Errorf("unknown plugin %q", id)
	}
	if !safePackagePath(relative) || relative == ".runpilot-source.json" {
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
	entries := m.plugins
	if m.frozen {
		entries = m.active
	}
	for id, plugin := range entries {
		if (!m.frozen && !m.enabled(plugin.manifest)) || !plugin.manifest.CompatibleHere() || plugin.manifest.Frontend == nil {
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

// FreezeActivation pins frontend assets and loaded state until process restart.
func (m *Manager) FreezeActivation() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = map[string]*installedPlugin{}
	for id, p := range m.plugins {
		if m.enabled(p.manifest) && p.manifest.CompatibleHere() && p.message == nil {
			copy := *p
			m.active[id] = &copy
			m.loaded[id] = true
		}
	}
	m.frozen = true
}
func (m *Manager) SetFailure(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.plugins[id]; p != nil {
		p.message = err
	}
}

// Uninstall removes the package from discovery. Active assets stay pinned in a
// private retired directory until the next startup; mutable data is untouched.
func (m *Manager) Uninstall(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validID(id) || m.plugins[id] == nil {
		return fmt.Errorf("unknown plugin %q", id)
	}
	retired, err := os.MkdirTemp(m.root, ".removed-")
	if err != nil {
		return err
	}
	target := filepath.Join(retired, id)
	if err := os.MkdirAll(target, 0o755); err != nil {
		_ = os.RemoveAll(retired)
		return err
	}
	if err := os.Rename(filepath.Join(m.root, id, "releases"), filepath.Join(target, "releases")); err != nil {
		_ = os.RemoveAll(retired)
		return err
	}
	if err := os.MkdirAll(filepath.Join(m.root, id, "releases"), 0o755); err != nil {
		return err
	}
	if p := m.active[id]; p != nil {
		p.dir = filepath.Join(target, "releases", p.manifest.Version)
	}
	delete(m.plugins, id)
	m.restartRequired = true
	return nil
}

func (m *Manager) RestartRequired() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.restartRequired
}
