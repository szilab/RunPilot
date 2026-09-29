package plugins

import (
	"fmt"
	"runtime"
	"strings"
)

const (
	PluginAPIVersion = "runpilot.plugin/v1"
	PluginABIVersion = 1
)

type State string

const (
	StateInstalled    State = "installed"
	StateEnabled      State = "enabled"
	StateLoaded       State = "loaded"
	StateIncompatible State = "incompatible"
	StateFailed       State = "failed"
)

type BackendManifest struct {
	Module string `yaml:"module" json:"module"`
}
type FrontendManifest struct {
	Module     string `yaml:"module" json:"module"`
	Stylesheet string `yaml:"stylesheet,omitempty" json:"stylesheet,omitempty"`
}

type Manifest struct {
	APIVersion  string            `yaml:"apiVersion" json:"apiVersion"`
	ID          string            `yaml:"id" json:"id"`
	Name        string            `yaml:"name" json:"name"`
	Version     string            `yaml:"version" json:"version"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Requires    Requires          `yaml:"requires" json:"requires"`
	Platforms   []string          `yaml:"platforms,omitempty" json:"platforms,omitempty"`
	Backend     *BackendManifest  `yaml:"backend,omitempty" json:"backend,omitempty"`
	Frontend    *FrontendManifest `yaml:"frontend,omitempty" json:"frontend,omitempty"`
}

type Requires struct {
	RunPilotAPI int `yaml:"runpilotApi" json:"runpilotApi"`
}

func (m Manifest) Validate() error {
	if m.APIVersion != PluginAPIVersion {
		return fmt.Errorf("unsupported apiVersion %q", m.APIVersion)
	}
	if !validID(m.ID) {
		return fmt.Errorf("invalid plugin id %q", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("plugin name is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("plugin version is required")
	}
	if m.Requires.RunPilotAPI != PluginABIVersion {
		return fmt.Errorf("plugin API version %d is not supported; RunPilot requires %d", m.Requires.RunPilotAPI, PluginABIVersion)
	}
	seen := map[string]struct{}{}
	for _, platform := range m.Platforms {
		platform = strings.ToLower(strings.TrimSpace(platform))
		if platform != "windows" && platform != "linux" {
			return fmt.Errorf("unsupported platform %q (supported: windows, linux)", platform)
		}
		key := platform
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate platform %q", key)
		}
		seen[key] = struct{}{}
	}
	if m.Backend != nil && !safePackagePath(m.Backend.Module) {
		return fmt.Errorf("invalid backend module path %q", m.Backend.Module)
	}
	if m.Frontend != nil && (!safePackagePath(m.Frontend.Module) || (m.Frontend.Stylesheet != "" && !safePackagePath(m.Frontend.Stylesheet))) {
		return fmt.Errorf("invalid frontend asset path")
	}
	return nil
}

// Compatible reports whether this package can be activated on the current OS.
// An empty platforms list means platform-independent.
func (m Manifest) Compatible(goos string) bool {
	if len(m.Platforms) == 0 {
		return true
	}
	for _, platform := range m.Platforms {
		if strings.EqualFold(platform, goos) {
			return true
		}
	}
	return false
}

func (m Manifest) CompatibleHere() bool { return m.Compatible(runtime.GOOS) }

type Status struct {
	Manifest         Manifest `json:"manifest"`
	Enabled          bool     `json:"enabled"`
	State            State    `json:"state"`
	Message          string   `json:"message,omitempty"`
	InstalledVersion string   `json:"installedVersion,omitempty"`
	UpdateAvailable  bool     `json:"updateAvailable,omitempty"`
	RestartRequired  bool     `json:"restartRequired,omitempty"`
}

func safePackagePath(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(value, "/") && !strings.Contains(value, "\\") && value != "." && !strings.HasPrefix(value, "../") && !strings.Contains(value, "/../")
}
