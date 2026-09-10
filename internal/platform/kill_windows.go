//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"strconv"
)

func KillProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	out, err := exec.Command("taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("taskkill pid %d: %w: %s", pid, err, string(out))
	}
	return nil
}
