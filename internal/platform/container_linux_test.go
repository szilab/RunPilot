//go:build linux

package platform

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

// Docker bind-mounts /etc/hostname, /etc/hosts and /etc/resolv.conf from the
// host disk; those files must not appear as filesystems.
const containerMountinfo = `1290 1180 0:310 / / rw,relatime master:1 - overlay overlay rw,lowerdir=/l
1291 1290 0:313 / /proc rw - proc proc rw
1301 1290 259:3 /srv/runpilot /var/lib/runpilot rw,relatime - ext4 /dev/nvme0n1p3 rw
1302 1290 259:3 /var/lib/docker/containers/x/hostname /etc/hostname rw,relatime - ext4 /dev/nvme0n1p3 rw
1303 1290 259:3 /var/lib/docker/containers/x/hosts /etc/hosts rw,relatime - ext4 /dev/nvme0n1p3 rw
1304 1290 259:5 / /mnt/media\040disk rw,relatime - xfs /dev/sdb1 rw
1305 1290 0:26 / /run/docker.sock rw - tmpfs tmpfs rw`

func fakeStat(files ...string) func(string) (filesystemUsage, bool) {
	return func(mount string) (filesystemUsage, bool) {
		for _, file := range files {
			if file == mount {
				return filesystemUsage{directory: false, total: 100, free: 50}, true
			}
		}
		return filesystemUsage{directory: true, total: 1000, free: 400}, true
	}
}

func TestLinuxDisksSkipFileBindMounts(t *testing.T) {
	stat := fakeStat("/etc/hostname", "/etc/hosts")
	paths := func(disks []model.DiskStatus) []string {
		out := []string{}
		for _, disk := range disks {
			out = append(out, disk.Path)
		}
		return out
	}
	container := paths(linuxDisksFrom(containerMountinfo, true, stat))
	if want := []string{"/", "/mnt/media disk", "/var/lib/runpilot"}; !equalStrings(container, want) {
		t.Fatalf("container filesystems = %q, want %q", container, want)
	}
	host := paths(linuxDisksFrom(containerMountinfo, false, stat))
	if want := []string{"/mnt/media disk", "/var/lib/runpilot"}; !equalStrings(host, want) {
		t.Fatalf("host filesystems = %q, want %q", host, want)
	}
	// A file mount listed first must not hide the directory on the same device.
	reordered := "1302 1290 259:3 /h /etc/hostname rw - ext4 /dev/nvme0n1p3 rw\n1301 1290 259:3 /srv /var/lib/runpilot rw - ext4 /dev/nvme0n1p3 rw"
	if got := paths(linuxDisksFrom(reordered, true, stat)); !equalStrings(got, []string{"/var/lib/runpilot"}) {
		t.Fatalf("file mount hid the data filesystem: %q", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDetectContainerRuntime(t *testing.T) {
	exists := func(paths ...string) func(string) bool {
		return func(path string) bool {
			for _, p := range paths {
				if p == path {
					return true
				}
			}
			return false
		}
	}
	env := func(value string) func(string) string {
		return func(name string) string {
			if name == ContainerEnv {
				return value
			}
			return ""
		}
	}
	cases := []struct {
		name, env, want string
		markers         []string
	}{
		{"host", "", "", nil},
		{"docker marker", "", "docker", []string{"/.dockerenv"}},
		{"podman marker", "", "podman", []string{"/run/.containerenv"}},
		{"forced off", "false", "", []string{"/.dockerenv"}},
		{"forced generic", "1", "container", nil},
		{"forced keeps marker", "true", "docker", []string{"/.dockerenv"}},
		{"named runtime", "kubernetes", "kubernetes", nil},
		{"invalid name", "Bad Name!", "container", nil},
	}
	for _, tc := range cases {
		if got := detectContainerRuntime(env(tc.env), exists(tc.markers...)); got != tc.want {
			t.Errorf("%s: runtime = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadCgroupStatsV2(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"cgroup.controllers": "cpu memory",
		"cpu.stat":           "usage_usec 5000000\nuser_usec 4000000\n",
		"cpu.max":            "150000 100000\n",
		"memory.current":     "524288000\n",
		"memory.stat":        "anon 300000000\ninactive_file 104857600\n",
		"memory.max":         "1073741824\n",
	})
	stats := readCgroupStats(root)
	if !stats.cpuUsageOK || stats.cpuUsageMicros != 5000000 || stats.cpuLimit != 1.5 {
		t.Fatalf("cpu stats = %+v", stats)
	}
	if !stats.memoryOK || stats.memoryUsage != 524288000-104857600 || stats.memoryLimit != 1<<30 {
		t.Fatalf("memory stats = %+v", stats)
	}
	writeFiles(t, root, map[string]string{"cpu.max": "max 100000\n", "memory.max": "max\n"})
	if stats := readCgroupStats(root); stats.cpuLimit != 0 || stats.memoryLimit != 0 {
		t.Fatalf("unlimited cgroup reported limits: %+v", stats)
	}
}

func TestReadCgroupStatsV1(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"cpuacct/cpuacct.usage":        "3000000000\n",
		"cpu/cpu.cfs_quota_us":         "-1\n",
		"cpu/cpu.cfs_period_us":        "100000\n",
		"memory/memory.usage_in_bytes": "200000000\n",
		"memory/memory.stat":           "cache 1\ntotal_inactive_file 50000000\n",
		"memory/memory.limit_in_bytes": "9223372036854771712\n",
	})
	stats := readCgroupStats(root)
	if !stats.cpuUsageOK || stats.cpuUsageMicros != 3000000 || stats.cpuLimit != 0 {
		t.Fatalf("cpu stats = %+v", stats)
	}
	if !stats.memoryOK || stats.memoryUsage != 150000000 || stats.memoryLimit != 0 {
		t.Fatalf("memory stats = %+v", stats)
	}
}

func TestCgroupCPUSamplerUsesContainerCapacity(t *testing.T) {
	var sampler cgroupCPUSampler
	start := time.Unix(1000, 0)
	if current, _ := sampler.sample(1_000_000, 2, start); current != 0 {
		t.Fatalf("first sample = %v", current)
	}
	// One CPU-second over one wall-clock second on a 2-CPU limit is 50%.
	if current, _ := sampler.sample(2_000_000, 2, start.Add(time.Second)); current != 50 {
		t.Fatalf("utilization = %v, want 50", current)
	}
	if current, _ := sampler.sample(9_000_000, 2, start.Add(2*time.Second)); current != 100 {
		t.Fatalf("utilization above capacity = %v, want clamp to 100", current)
	}
	if got := containerCPUCapacity(1.5, 16); got != 1.5 {
		t.Fatalf("capacity = %v", got)
	}
	if got := containerCPUCapacity(32, 16); got != 16 {
		t.Fatalf("quota above schedulable CPUs = %v", got)
	}
}

func TestApplyContainerStatusUsesLimits(t *testing.T) {
	status := model.HostStatus{CPUCount: 16, LoadAverage: []float64{1, 2, 3}, MemoryTotalBytes: 32 << 30, MemoryFreeBytes: 20 << 30, CPUAvailable: true}
	applyContainerStatus(&status, "docker", cgroupStats{cpuUsageOK: true, cpuUsageMicros: 1, cpuLimit: 1.5, memoryOK: true, memoryUsage: 256 << 20, memoryLimit: 1 << 30}, time.Unix(5000, 0))
	if status.Container == nil || status.Container.Runtime != "docker" || status.Container.CPULimit != 1.5 || status.Container.MemoryLimitBytes != 1<<30 || status.Container.HostMemoryBytes != 32<<30 {
		t.Fatalf("container = %+v", status.Container)
	}
	if status.MemoryTotalBytes != 1<<30 || status.MemoryFreeBytes != 768<<20 {
		t.Fatalf("memory = %d/%d", status.MemoryFreeBytes, status.MemoryTotalBytes)
	}
	if status.LoadAverage != nil || status.CPUCount != 2 || !status.CPUAvailable {
		t.Fatalf("cpu fields = %+v", status)
	}
	unlimited := model.HostStatus{MemoryTotalBytes: 8 << 30}
	applyContainerStatus(&unlimited, "docker", cgroupStats{memoryOK: true, memoryUsage: 1 << 30}, time.Unix(6000, 0))
	if unlimited.Container.MemoryLimitBytes != 0 || unlimited.MemoryTotalBytes != 8<<30 || unlimited.MemoryFreeBytes != 7<<30 || unlimited.CPUAvailable {
		t.Fatalf("unlimited container = %+v %+v", unlimited, unlimited.Container)
	}
}

func TestProcessUptime(t *testing.T) {
	stat := "1 (tini (init)) S 0 1 1 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 50000 1000 100"
	if got := processUptime(stat, 1000*time.Second); got != 500*time.Second {
		t.Fatalf("uptime = %v, want 500s", got)
	}
	if got := processUptime("garbage", time.Hour); got != 0 {
		t.Fatalf("invalid stat uptime = %v", got)
	}
}
