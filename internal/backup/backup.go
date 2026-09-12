// Package backup owns typed backup-tool integration. The runner only executes
// the returned plan; each provider owns its command and exit-code semantics.
package backup

import (
	"fmt"
	"strings"

	"github.com/szilab/RunPilot/internal/model"
)

type Capabilities struct {
	Retention    bool `json:"retention"`
	Verification bool `json:"verification"`
	Versioned    bool `json:"versioned"`
	DirectMirror bool `json:"directMirror"`
}
type Step struct {
	Name    string
	Command model.CommandSpec
	Success func(int) bool
}
type Plan struct {
	Provider string
	Steps    []Step
}
type Provider interface {
	Name() string
	Capabilities() Capabilities
	Validate(model.BackupSpec) error
	Plan(model.BackupSpec) (Plan, error)
}

func ProviderFor(engine string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "", "robocopy":
		return robocopyProvider{}, nil
	case "restic":
		return resticProvider{}, nil
	case "rdiff-backup", "rdiffbackup":
		return rdiffBackupProvider{}, nil
	default:
		return nil, fmt.Errorf("unsupported backup provider %q", engine)
	}
}
func canonical(spec model.BackupSpec) model.BackupSpec {
	if (spec.Engine == "" || strings.EqualFold(spec.Engine, "robocopy")) && spec.Robocopy == nil {
		spec.Robocopy = &model.RobocopyBackupSpec{Source: spec.Source, Destination: spec.Destination, Mode: spec.Mode, ExcludeDirs: spec.ExcludeDirs, ExcludeFiles: spec.ExcludeFiles, Retries: spec.Retries, RetryWaitSeconds: spec.RetryWaitSeconds, AdditionalArgs: spec.AdditionalArgs}
		if spec.Robocopy.Mode == "" {
			spec.Robocopy.Mode = model.BackupCopy
		}
		if spec.Robocopy.RetryWaitSeconds == 0 {
			spec.Robocopy.RetryWaitSeconds = 5
		}
	}
	// Older configurations used Source/Destination for every engine. Preserve
	// that established portable shorthand while normalizing it into each
	// provider's typed configuration.
	if strings.EqualFold(spec.Engine, "restic") && spec.Restic == nil {
		spec.Restic = &model.ResticBackupSpec{Repository: spec.Destination, Sources: []string{spec.Source}, Excludes: append(append([]string{}, spec.ExcludeDirs...), spec.ExcludeFiles...)}
	}
	if (strings.EqualFold(spec.Engine, "rdiff-backup") || strings.EqualFold(spec.Engine, "rdiffbackup")) && spec.RdiffBackup == nil {
		spec.RdiffBackup = &model.RdiffBackupSpec{Source: spec.Source, Destination: spec.Destination, Excludes: append(append([]string{}, spec.ExcludeDirs...), spec.ExcludeFiles...)}
	}
	return spec
}
func Validate(spec model.BackupSpec) error {
	spec = canonical(spec)
	p, err := ProviderFor(spec.Engine)
	if err != nil {
		return err
	}
	return p.Validate(spec)
}
func BuildPlan(spec model.BackupSpec) (Plan, error) {
	spec = canonical(spec)
	p, err := ProviderFor(spec.Engine)
	if err != nil {
		return Plan{}, err
	}
	if err = p.Validate(spec); err != nil {
		return Plan{}, err
	}
	return p.Plan(spec)
}
func Build(spec model.BackupSpec) (model.CommandSpec, error) {
	plan, err := BuildPlan(spec)
	if err != nil {
		return model.CommandSpec{}, err
	}
	if len(plan.Steps) != 1 {
		return model.CommandSpec{}, fmt.Errorf("backup provider %q requires multiple commands", plan.Provider)
	}
	return plan.Steps[0].Command, nil
}
func exitZero(code int) bool { return code == 0 }
