//go:build !windows && !linux

package service

import (
	"fmt"
	"github.com/szilab/RunPilot/internal/daemon"
)

func unsupported() error {
	return fmt.Errorf("native service management is supported on Windows and Linux only")
}
func Install(dataDir string, options ...daemon.Options) error { return unsupported() }
func Uninstall() error                                        { return unsupported() }
func Start() error                                            { return unsupported() }
func Stop() error                                             { return unsupported() }
func Run(dataDir string, options ...daemon.Options) error     { return unsupported() }
