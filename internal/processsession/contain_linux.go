//go:build linux

package processsession

import (
	pty "github.com/aymanbagabas/go-pty"
	"os"
	"syscall"
)

func contain(process *os.Process) (func() error, error) {
	return func() error {
		if process == nil {
			return nil
		}
		if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}, nil
}
func killTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
func graceful(process *os.Process, _ pty.Pty) error {
	if process == nil {
		return nil
	}
	err := syscall.Kill(-process.Pid, syscall.SIGTERM)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
