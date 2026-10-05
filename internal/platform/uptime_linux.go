//go:build linux

package platform

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// HostUptime returns zero when the kernel uptime cannot be read.
func HostUptime() time.Duration {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}
