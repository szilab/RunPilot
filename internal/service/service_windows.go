//go:build windows

// Package service exposes native daemon control without leaking a particular
// operating system's service manager to the application layer.
package service

import (
	"github.com/szilab/RunPilot/internal/daemon"
	"github.com/szilab/RunPilot/internal/winservice"
)

func Install(dataDir string, options ...daemon.Options) error {
	return winservice.Install(dataDir, options...)
}
func Uninstall() error                                    { return winservice.Uninstall() }
func Start() error                                        { return winservice.Start() }
func Stop() error                                         { return winservice.Stop() }
func Run(dataDir string, options ...daemon.Options) error { return winservice.Run(dataDir, options...) }
