package backup

import (
	"fmt"
	"github.com/szilab/RunPilot/internal/model"
	"strings"
)

type rdiffBackupProvider struct{}

func (rdiffBackupProvider) Name() string { return "rdiff-backup" }
func (rdiffBackupProvider) Capabilities() Capabilities {
	return Capabilities{Retention: true, Verification: true, Versioned: true, DirectMirror: true}
}
func (rdiffBackupProvider) Validate(s model.BackupSpec) error {
	r := s.RdiffBackup
	if r == nil {
		return fmt.Errorf("rdiff-backup configuration is required")
	}
	if strings.TrimSpace(r.Source) == "" || strings.TrimSpace(r.Destination) == "" {
		return fmt.Errorf("rdiff-backup source and destination are required")
	}
	if r.Retention != nil && strings.TrimSpace(r.Retention.OlderThan) == "" {
		return fmt.Errorf("rdiff-backup retention value is required when retention is enabled")
	}
	return nil
}
func rdiffBase(r *model.RdiffBackupSpec) string {
	if strings.TrimSpace(r.Executable) != "" {
		return r.Executable
	}
	return "rdiff-backup.exe"
}
func (rdiffBackupProvider) Plan(s model.BackupSpec) (Plan, error) {
	r := s.RdiffBackup
	exe := rdiffBase(r)
	a := []string{}
	for _, x := range nonEmpty(r.Excludes) {
		a = append(a, "--exclude", x)
	}
	a = append(a, r.Source, r.Destination)
	steps := []Step{{Name: "backup", Command: model.CommandSpec{Path: exe, Args: a, Interpreter: "direct"}, Success: exitZero}}
	if r.Retention != nil {
		steps = append(steps, Step{Name: "retention", Command: model.CommandSpec{Path: exe, Args: []string{"remove", "increments", "--older-than", r.Retention.OlderThan, r.Destination}, Interpreter: "direct"}, Success: exitZero})
	}
	if r.VerifyAfterBackup {
		steps = append(steps, Step{Name: "verify", Command: model.CommandSpec{Path: exe, Args: []string{"verify", r.Destination}, Interpreter: "direct"}, Success: exitZero})
	}
	return Plan{Provider: "rdiff-backup", Steps: steps}, nil
}
