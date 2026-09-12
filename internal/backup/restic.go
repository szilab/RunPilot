package backup

import (
	"fmt"
	"github.com/szilab/RunPilot/internal/model"
	"runtime"
	"strconv"
	"strings"
)

type resticProvider struct{}

func (resticProvider) Name() string { return "restic" }
func (resticProvider) Capabilities() Capabilities {
	return Capabilities{Retention: true, Verification: true, Versioned: true}
}
func (resticProvider) Validate(spec model.BackupSpec) error {
	r := spec.Restic
	if r == nil {
		return fmt.Errorf("restic configuration is required")
	}
	if strings.TrimSpace(r.Repository) == "" {
		return fmt.Errorf("restic repository is required")
	}
	if len(nonEmpty(r.Sources)) == 0 {
		return fmt.Errorf("at least one restic source is required")
	}
	if r.Retention != nil {
		for _, n := range []int{r.Retention.KeepLast, r.Retention.KeepDaily, r.Retention.KeepWeekly, r.Retention.KeepMonthly, r.Retention.KeepYearly} {
			if n < 0 {
				return fmt.Errorf("restic retention values cannot be negative")
			}
		}
		if strings.TrimSpace(r.Retention.KeepWithin) == "" && r.Retention.KeepLast == 0 && r.Retention.KeepDaily == 0 && r.Retention.KeepWeekly == 0 && r.Retention.KeepMonthly == 0 && r.Retention.KeepYearly == 0 {
			return fmt.Errorf("restic retention needs at least one keep setting")
		}
	}
	return nil
}
func resticBase(r *model.ResticBackupSpec) ([]string, string) {
	exe := r.Executable
	if strings.TrimSpace(exe) == "" {
		exe = "restic"
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
	}
	args := []string{"--repo", r.Repository}
	if strings.TrimSpace(r.PasswordFile) != "" {
		args = append(args, "--password-file", r.PasswordFile)
	}
	return args, exe
}
func (resticProvider) Plan(spec model.BackupSpec) (Plan, error) {
	r := spec.Restic
	base, exe := resticBase(r)
	a := append([]string{}, base...)
	a = append(a, "backup")
	for _, x := range nonEmpty(r.Excludes) {
		a = append(a, "--exclude", x)
	}
	for _, tag := range nonEmpty(r.Tags) {
		a = append(a, "--tag", tag)
	}
	if r.UseVSS {
		a = append(a, "--use-fs-snapshot")
	}
	a = append(a, nonEmpty(r.Sources)...)
	steps := []Step{{Name: "backup", Command: model.CommandSpec{Path: exe, Args: a, Interpreter: "direct"}, Success: exitZero}}
	if r.Retention != nil {
		a = append([]string{}, base...)
		a = append(a, "forget")
		retentionArgs(&a, r.Retention)
		if r.Retention.Prune {
			a = append(a, "--prune")
		}
		steps = append(steps, Step{Name: "retention", Command: model.CommandSpec{Path: exe, Args: a, Interpreter: "direct"}, Success: exitZero})
	}
	if r.CheckAfterBackup {
		a = append(append([]string{}, base...), "check")
		steps = append(steps, Step{Name: "check", Command: model.CommandSpec{Path: exe, Args: a, Interpreter: "direct"}, Success: exitZero})
	}
	return Plan{Provider: "Restic", Steps: steps}, nil
}
func retentionArgs(a *[]string, r *model.ResticRetention) {
	for _, v := range []struct {
		f string
		n int
	}{{"--keep-last", r.KeepLast}, {"--keep-daily", r.KeepDaily}, {"--keep-weekly", r.KeepWeekly}, {"--keep-monthly", r.KeepMonthly}, {"--keep-yearly", r.KeepYearly}} {
		if v.n > 0 {
			*a = append(*a, v.f, strconv.Itoa(v.n))
		}
	}
	if strings.TrimSpace(r.KeepWithin) != "" {
		*a = append(*a, "--keep-within", r.KeepWithin)
	}
}
func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
