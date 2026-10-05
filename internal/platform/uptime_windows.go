//go:build windows

package platform

import "time"

func HostUptime() time.Duration {
	ticks, _, _ := kernel32.NewProc("GetTickCount64").Call()
	return time.Duration(ticks) * time.Millisecond
}
