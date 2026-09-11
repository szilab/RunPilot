//go:build !windows && !linux

package platform

import (
	"os"
	"runtime"

	"github.com/szilab/RunPilot/internal/model"
)

func HostStatus() model.HostStatus {
	hostname, _ := os.Hostname()
	return model.HostStatus{OS: runtime.GOOS, Hostname: hostname, Disks: []model.DiskStatus{}, Error: "detailed host metrics are unavailable on this platform"}
}
