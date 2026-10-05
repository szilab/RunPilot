//go:build !linux && !windows

package platform

import "time"

func HostUptime() time.Duration { return 0 }
