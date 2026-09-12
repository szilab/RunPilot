// Package software defines RunPilot's provider-neutral Software capability.
package software

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/szilab/RunPilot/internal/model"
)

type State string

const (
	StateInitializing State = "initializing"
	StateReady        State = "ready"
	StateUnavailable  State = "unavailable"
)

type Operation struct {
	Action  string `json:"action,omitempty"`
	State   string `json:"state,omitempty"`
	Message string `json:"message,omitempty"`
	Output  string `json:"output,omitempty"`
}

type Status struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	State         State     `json:"state"`
	Message       string    `json:"message,omitempty"`
	Root          string    `json:"root"`
	UsingDefault  bool      `json:"usingDefaultRoot"`
	Version       string    `json:"version,omitempty"`
	LastOperation Operation `json:"lastOperation,omitempty"`
}

type Package struct {
	Provider        string `json:"provider"`
	ID              string `json:"id"`
	Name            string `json:"name"`
	Version         string `json:"version,omitempty"`
	Available       string `json:"availableVersion,omitempty"`
	Bucket          string `json:"bucket,omitempty"`
	Description     string `json:"description,omitempty"`
	Homepage        string `json:"homepage,omitempty"`
	Installed       bool   `json:"installed"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Protected       bool   `json:"protected"`
}

type Bucket struct {
	Name      string `json:"name"`
	Source    string `json:"source,omitempty"`
	Protected bool   `json:"protected"`
}

// Provider is deliberately focused on the operations the Software UI needs.
// It does not expose tool-specific command execution.
type Provider interface {
	Status(context.Context) Status
	Installed(context.Context) ([]Package, error)
	Search(context.Context, string) ([]Package, error)
	Updates(context.Context) ([]Package, error)
	Install(context.Context, string) error
	Upgrade(context.Context, string) error
	UpgradeAll(context.Context) error
	Uninstall(context.Context, string) error
	Refresh(context.Context) error
}

// BucketManager is optional because not every Software provider will use
// Scoop-style buckets. It keeps this provider-specific capability out of the
// basic package-management contract.
type BucketManager interface {
	Buckets(context.Context) ([]Bucket, error)
	AddBucket(context.Context, string, string) error
	RemoveBucket(context.Context, string) error
}

func DefaultScoopRoot(dataDir string) string { return filepath.Join(dataDir, "software", "scoop") }

func ValidateDefinition(dataDir string, d model.SoftwareProviderDefinition) (model.SoftwareProviderDefinition, error) {
	if strings.TrimSpace(d.ID) == "" {
		return d, fmt.Errorf("software provider ID is required")
	}
	if strings.TrimSpace(d.Name) == "" {
		return d, fmt.Errorf("software provider name is required")
	}
	if d.Type != model.SoftwareProviderScoop || d.Scoop == nil {
		return d, fmt.Errorf("software provider type scoop with scoop settings is required")
	}
	root := strings.TrimSpace(d.Scoop.Root)
	if root == "" {
		return d, nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return d, fmt.Errorf("invalid Scoop root %q: %w", root, err)
	}
	if filepath.VolumeName(abs) == "" && filepath.IsAbs(abs) == false {
		return d, fmt.Errorf("Scoop root must be an absolute path")
	}
	if strings.ContainsAny(root, "\x00\r\n") {
		return d, fmt.Errorf("Scoop root contains invalid characters")
	}
	d.Scoop.Root = abs
	return d, nil
}

func EffectiveRoot(dataDir string, d model.SoftwareProviderDefinition) (string, bool, error) {
	d, err := ValidateDefinition(dataDir, d)
	if err != nil {
		return "", false, err
	}
	if d.Scoop.Root == "" {
		root, err := filepath.Abs(DefaultScoopRoot(dataDir))
		return root, true, err
	}
	return d.Scoop.Root, false, nil
}
