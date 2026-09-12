package backup

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/szilab/RunPilot/internal/model"
)

type robocopyProvider struct{}

func (robocopyProvider) Name() string               { return "robocopy" }
func (robocopyProvider) Capabilities() Capabilities { return Capabilities{DirectMirror: true} }
func (robocopyProvider) Validate(spec model.BackupSpec) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("robocopy backup engine is only available on Windows")
	}
	r := spec.Robocopy
	if r == nil {
		return fmt.Errorf("robocopy configuration is required")
	}
	if strings.TrimSpace(r.Source) == "" || strings.TrimSpace(r.Destination) == "" {
		return fmt.Errorf("robocopy source and destination are required")
	}
	if r.Mode != "" && r.Mode != model.BackupCopy && r.Mode != model.BackupMirror {
		return fmt.Errorf("unsupported robocopy mode %q", r.Mode)
	}
	if r.Retries < 0 || r.RetryWaitSeconds < 0 {
		return fmt.Errorf("robocopy retries and retry wait cannot be negative")
	}
	return nil
}
func (robocopyProvider) Plan(spec model.BackupSpec) (Plan, error) {
	r := spec.Robocopy
	args := []string{r.Source, r.Destination}
	switch r.Mode {
	case model.BackupMirror:
		args = append(args, "/MIR")
	case model.BackupCopy, "":
		args = append(args, "/E")
	default:
		return Plan{}, fmt.Errorf("unsupported backup mode %q", r.Mode)
	}
	retries := r.Retries
	wait := r.RetryWaitSeconds
	args = append(args,
		"/COPY:DAT",
		"/DCOPY:DAT",
		"/XJ",
		"/R:"+strconv.Itoa(retries),
		"/W:"+strconv.Itoa(wait),
		"/NP",
	)
	if len(r.ExcludeDirs) > 0 {
		args = append(args, "/XD")
		args = append(args, r.ExcludeDirs...)
	}
	if len(r.ExcludeFiles) > 0 {
		args = append(args, "/XF")
		args = append(args, r.ExcludeFiles...)
	}
	args = append(args, r.AdditionalArgs...)
	return Plan{Provider: "Robocopy", Steps: []Step{{Name: "backup", Command: model.CommandSpec{Path: "robocopy.exe", Args: args, Interpreter: "direct"}, Success: ExitCodeSuccess}}}, nil
}

func ExitCodeSuccess(code int) bool {
	// Robocopy uses bitmask-style exit codes. 0-7 are success or non-fatal differences;
	// 8+ means at least one copy failure.
	return code >= 0 && code < 8
}
