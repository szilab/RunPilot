//go:build linux

package terminal

import (
	"os"
	"syscall"
)

func attachProcessTree(process *os.Process) (func() error, error) {
	return func() error {
		if process == nil {
			return nil
		}
		// go-pty starts Unix commands in a new session, whose process group is
		// rooted at the shell PID. Killing the group also reaps interactive children.
		if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}, nil
}
