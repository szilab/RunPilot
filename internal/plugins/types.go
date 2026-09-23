package plugins

import (
	"fmt"
	"strings"
)

const (
	PluginAPIVersion = "runpilot.plugin/v1"
	PluginABIVersion = 1
)

type State string

const (
	StateAvailable    State = "available"
	StateInstalled    State = "installed"
	StateEnabled      State = "enabled"
	StateLoaded       State = "loaded"
	StateIncompatible State = "incompatible"
	StateFailed       State = "failed"
)

type Capability struct {
	Type string `yaml:"type" json:"type"`
	ID   string `yaml:"id" json:"id"`
}

type BackendManifest struct {
	Module string `yaml:"module" json:"module"`
}
type FrontendManifest struct {
	Module     string `yaml:"module" json:"module"`
	Stylesheet string `yaml:"stylesheet,omitempty" json:"stylesheet,omitempty"`
}

type Manifest struct {
	APIVersion   string            `yaml:"apiVersion" json:"apiVersion"`
	ID           string            `yaml:"id" json:"id"`
	Name         string            `yaml:"name" json:"name"`
	Version      string            `yaml:"version" json:"version"`
	Description  string            `yaml:"description,omitempty" json:"description,omitempty"`
	Requires     Requires          `yaml:"requires" json:"requires"`
	Capabilities []Capability      `yaml:"capabilities" json:"capabilities"`
	Backend      *BackendManifest  `yaml:"backend,omitempty" json:"backend,omitempty"`
	Frontend     *FrontendManifest `yaml:"frontend,omitempty" json:"frontend,omitempty"`
	Permissions  []string          `yaml:"permissions,omitempty" json:"permissions,omitempty"`
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
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("at least one capability is required")
	}
	seen := map[string]struct{}{}
	for _, capability := range m.Capabilities {
		if !validID(capability.Type) || !validID(capability.ID) {
			return fmt.Errorf("invalid capability %q/%q", capability.Type, capability.ID)
		}
		key := capability.Type + ":" + capability.ID
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate capability %q", key)
		}
		seen[key] = struct{}{}
	}
	if m.Backend == nil && m.Frontend == nil {
		return fmt.Errorf("backend or frontend is required")
	}
	if m.Backend != nil && !safePackagePath(m.Backend.Module) {
		return fmt.Errorf("invalid backend module path %q", m.Backend.Module)
	}
	if m.Frontend != nil && (!safePackagePath(m.Frontend.Module) || (m.Frontend.Stylesheet != "" && !safePackagePath(m.Frontend.Stylesheet))) {
		return fmt.Errorf("invalid frontend asset path")
	}
	return nil
}

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
