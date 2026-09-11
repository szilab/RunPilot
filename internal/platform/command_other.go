//go:build !linux

package platform

import "os/exec"

func ConfigureCommand(cmd *exec.Cmd) {}
