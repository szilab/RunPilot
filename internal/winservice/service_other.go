//go:build !windows

package winservice

import (
	"fmt"

	"github.com/szilab/RunPilot/internal/daemon"
)

func unsupported() error {
	return fmt.Errorf("Windows service management is only available on Windows")
}
func Install(dataDir string, options ...daemon.Options) error { return unsupported() }
func Uninstall() error                                        { return unsupported() }
func Start() error                                            { return unsupported() }
func Stop() error                                             { return unsupported() }
func Run(dataDir string, options ...daemon.Options) error     { return unsupported() }
