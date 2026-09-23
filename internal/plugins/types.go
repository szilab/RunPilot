package plugins

import (
	"fmt"
	"strings"
)

const ProtocolVersion = 1

type State string

const (
	StateStopped     State = "stopped"
	StateRunning     State = "running"
	StateUnavailable State = "unavailable"
	StateFailed      State = "failed"
)

type Manifest struct {
	APIVersion      string            `yaml:"apiVersion" json:"apiVersion"`
	ID              string            `yaml:"id" json:"id"`
	Name            string            `yaml:"name" json:"name"`
	Version         string            `yaml:"version" json:"version"`
	ProtocolVersion int               `yaml:"protocolVersion" json:"protocolVersion"`
	Description     string            `yaml:"description,omitempty" json:"description,omitempty"`
	DefaultEnabled  bool              `yaml:"defaultEnabled,omitempty" json:"defaultEnabled"`
	Capabilities    []string          `yaml:"capabilities" json:"capabilities"`
	Executables     map[string]string `yaml:"executables" json:"executables"`
	Args            []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env             map[string]string `yaml:"env,omitempty" json:"-"`
	// LegacyEnabled is accepted only to migrate the initial runtime manifests.
	// New manifests must use defaultEnabled; enabled is never persisted here.
	LegacyEnabled *bool `yaml:"enabled,omitempty" json:"-"`
}

func (m Manifest) Executable(goos, goarch string) (string, bool) {
	keys := []string{goos + "-" + goarch, goos, "default"}
	for _, key := range keys {
		if value := strings.TrimSpace(m.Executables[key]); value != "" {
			return value, true
		}
	}
	return "", false
}

func (m Manifest) Validate() error {
	if m.APIVersion != "runpilot/v1" {
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
	if m.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("plugin protocol version %d is not supported; RunPilot requires %d", m.ProtocolVersion, ProtocolVersion)
	}
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("at least one capability is required")
	}
	seen := map[string]struct{}{}
	for _, capability := range m.Capabilities {
		if !validID(capability) {
			return fmt.Errorf("invalid capability %q", capability)
		}
		if _, ok := seen[capability]; ok {
			return fmt.Errorf("duplicate capability %q", capability)
		}
		seen[capability] = struct{}{}
	}
	return nil
}

type Status struct {
	Manifest          Manifest `json:"manifest"`
	Enabled           bool     `json:"enabled"`
	PlatformSupported bool     `json:"platformSupported"`
	Healthy           bool     `json:"healthy"`
	State             State    `json:"state"`
	PID               int      `json:"pid,omitempty"`
	StartedAt         string   `json:"startedAt,omitempty"`
	Message           string   `json:"message,omitempty"`
}
