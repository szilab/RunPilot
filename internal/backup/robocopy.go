package backup

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/szilab/RunPilot/internal/model"
)

func Build(spec model.BackupSpec) (model.CommandSpec, error) {
	if !strings.EqualFold(spec.Engine, "robocopy") && spec.Engine != "" {
		return model.CommandSpec{}, fmt.Errorf("unsupported backup engine %q", spec.Engine)
	}
	if strings.TrimSpace(spec.Source) == "" || strings.TrimSpace(spec.Destination) == "" {
		return model.CommandSpec{}, fmt.Errorf("backup source and destination are required")
	}
	args := []string{spec.Source, spec.Destination}
	switch spec.Mode {
	case model.BackupMirror:
		args = append(args, "/MIR")
	case model.BackupCopy, "":
		args = append(args, "/E")
	default:
		return model.CommandSpec{}, fmt.Errorf("unsupported backup mode %q", spec.Mode)
	}
	retries := spec.Retries
	if retries < 0 {
		retries = 0
	}
	wait := spec.RetryWaitSeconds
	if wait <= 0 {
		wait = 5
	}
	args = append(args,
		"/COPY:DAT",
		"/DCOPY:DAT",
		"/XJ",
		"/R:"+strconv.Itoa(retries),
		"/W:"+strconv.Itoa(wait),
		"/NP",
	)
	if len(spec.ExcludeDirs) > 0 {
		args = append(args, "/XD")
		args = append(args, spec.ExcludeDirs...)
	}
	if len(spec.ExcludeFiles) > 0 {
		args = append(args, "/XF")
		args = append(args, spec.ExcludeFiles...)
	}
	args = append(args, spec.AdditionalArgs...)
	return model.CommandSpec{
		Path:        "robocopy.exe",
		Args:        args,
		Interpreter: "direct",
	}, nil
}

func ExitCodeSuccess(code int) bool {
	// Robocopy uses bitmask-style exit codes. 0-7 are success or non-fatal differences;
	// 8+ means at least one copy failure.
	return code >= 0 && code < 8
}
