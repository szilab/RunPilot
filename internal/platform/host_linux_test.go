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

func TestLinuxHostCPUDetailsAndGPUParser(t *testing.T) {
	status := HostStatus()
	if status.CPUCount < 1 || !status.CPUAvailable {
		t.Fatalf("CPU metadata unavailable: %+v", status)
	}
	if len(status.LoadAverage) != 3 {
		t.Fatalf("Linux load average = %v", status.LoadAverage)
	}
	gpu, ok := parseNvidiaGPU([]byte("\"NVIDIA, Model\", 18, 2048, 8192\nOther GPU, 42, 1024, 4096\n"))
	if !ok || gpu.Name != "Other GPU" || gpu.Percent != 42 || gpu.MemoryUsed != 1024*1024*1024 || gpu.MemoryTotal != 4096*1024*1024 {
		t.Fatalf("GPU data = %+v, available = %v", gpu, ok)
	}
	if _, ok := parseNvidiaGPU([]byte("broken, n/a, n/a, n/a\n")); ok {
		t.Fatal("invalid GPU telemetry reported as available")
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
