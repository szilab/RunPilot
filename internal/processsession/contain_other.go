//go:build !linux && !windows

package processsession

import (
	pty "github.com/aymanbagabas/go-pty"
	"os"
)

func contain(process *os.Process) (func() error, error) { return func() error { return nil }, nil }
func killTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}
func graceful(process *os.Process, _ pty.Pty) error {
	if process == nil {
		return nil
	}
	return process.Signal(os.Interrupt)
}
