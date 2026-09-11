//go:build linux

package platform

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// KillProcessTree first asks the process group to terminate, then forces it.
// Managed commands are started in a distinct group by ConfigureCommand.
func KillProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminate process group %d: %w", pid, err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group %d: %w", pid, err)
	}
	return nil
}
