//go:build !windows

package software

import (
	"context"
	"fmt"
)

type systemRunner struct{}

func (systemRunner) Run(context.Context, Command) (CommandResult, error) {
	return CommandResult{}, fmt.Errorf("Scoop is only supported on Windows")
}

type httpDownloader struct{}

func (httpDownloader) Download(context.Context, string, string) error {
	return fmt.Errorf("Scoop is only supported on Windows")
}
func powerShellPath() string { return "powershell.exe" }
