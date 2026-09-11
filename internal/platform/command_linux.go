//go:build linux

package platform

import (
	"os/exec"
	"syscall"
)

// ConfigureCommand gives every managed Linux workload its own process group.
// KillProcessTree can then stop descendants as one managed unit.
func ConfigureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
