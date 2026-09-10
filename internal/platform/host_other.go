//go:build !windows

package platform

import (
	"os"
	"runtime"

	"github.com/szilab/RunPilot/internal/model"
)

// HostStatus keeps non-Windows builds testable. Full host metrics are supplied
// by the Windows implementation used by the production executable.
func HostStatus() model.HostStatus {
	hostname, _ := os.Hostname()
	return model.HostStatus{
		OS:       runtime.GOOS,
		Hostname: hostname,
		Disks:    []model.DiskStatus{},
		Error:    "detailed host metrics are available on Windows only",
	}
}
