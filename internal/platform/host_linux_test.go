//go:build linux

package platform

import (
	"strings"
	"testing"
)

func TestLinuxCPUTimesAreAvailable(t *testing.T) {
	times, err := linuxCPUTimesNow()
	if err != nil {
		t.Fatal(err)
	}
	if times.total == 0 {
		t.Fatal("CPU total time is zero")
	}
}

func TestMaximumPercent(t *testing.T) {
	percent, ok := maximumPercent([]string{"12", "87%", "not-a-number"})
	if !ok || percent != 87 {
		t.Fatalf("percent = %v, available = %v", percent, ok)
	}
}

func TestLinuxDisksContainOnlyBlockDevices(t *testing.T) {
	disks, err := linuxDisks()
	if err != nil {
		t.Fatal(err)
	}
	for _, disk := range disks {
		if !strings.HasPrefix(disk.Device, "/dev/") {
			t.Fatalf("non-device filesystem returned: %#v", disk)
		}
		if disk.Path == "/dev/shm" {
			t.Fatalf("temporary filesystem returned: %#v", disk)
		}
	}
}
