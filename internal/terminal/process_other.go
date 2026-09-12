//go:build !linux && !windows

package terminal

import "os"

func attachProcessTree(process *os.Process) (func() error, error) {
	return func() error {
		if process != nil {
			return process.Kill()
		}
		return nil
	}, nil
}
