//go:build linux

package platform

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

// ContainerEnv overrides container detection: "0"/"false" reports host
// metrics, "1"/"true" forces container metrics, and any other short name is
// used as the runtime label (for example "kubernetes").
const ContainerEnv = "RUNPILOT_CONTAINER"

const cgroupRoot = "/sys/fs/cgroup"

// userHZ is the kernel's fixed USER_HZ used by /proc/<pid>/stat on all
// mainstream Linux architectures.
const userHZ = 100

var (
	containerRuntimeOnce  sync.Once
	containerRuntimeValue string
	containerCPUSampler   cgroupCPUSampler
	runtimeLabelPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

func linuxContainerRuntime() string {
	containerRuntimeOnce.Do(func() {
		containerRuntimeValue = detectContainerRuntime(os.Getenv, func(path string) bool {
			_, err := os.Lstat(path)
			return err == nil
		})
	})
	return containerRuntimeValue
}

func detectContainerRuntime(getenv func(string) string, exists func(string) bool) string {
	marker := ""
	switch {
	case exists("/.dockerenv"):
		marker = "docker"
	case exists("/run/.containerenv"):
		marker = "podman"
	}
	switch value := strings.ToLower(strings.TrimSpace(getenv(ContainerEnv))); value {
	case "":
		return marker
	case "0", "false", "no":
		return ""
	case "1", "true", "yes":
		if marker != "" {
			return marker
		}
		return "container"
	default:
		if runtimeLabelPattern.MatchString(value) {
			return value
		}
		return "container"
	}
}

// cgroupStats are the calling container's own resource readings.
type cgroupStats struct {
	cpuUsageMicros uint64
	cpuUsageOK     bool
	cpuLimit       float64 // CPUs; 0 means no quota
	memoryUsage    uint64  // working set: usage minus reclaimable page cache
	memoryOK       bool
	memoryLimit    uint64 // bytes; 0 means no limit
}

func readCgroupStats(root string) cgroupStats {
	read := func(parts ...string) (string, bool) {
		data, err := os.ReadFile(filepath.Join(append([]string{root}, parts...)...))
		return strings.TrimSpace(string(data)), err == nil
	}
	var stats cgroupStats
	if _, v2 := read("cgroup.controllers"); v2 {
		if text, ok := read("cpu.stat"); ok {
			stats.cpuUsageMicros, stats.cpuUsageOK = statValue(text, "usage_usec")
		}
		if text, ok := read("cpu.max"); ok {
			stats.cpuLimit = parseCPUMax(text)
		}
		current, okCurrent := read("memory.current")
		if usage, err := strconv.ParseUint(current, 10, 64); okCurrent && err == nil {
			inactive := uint64(0)
			if text, ok := read("memory.stat"); ok {
				inactive, _ = statValue(text, "inactive_file")
			}
			stats.memoryUsage, stats.memoryOK = saturatingSub(usage, inactive), true
		}
		if text, ok := read("memory.max"); ok {
			stats.memoryLimit = parseMemoryLimit(text)
		}
		return stats
	}
	for _, dir := range []string{"cpuacct", "cpu,cpuacct"} {
		if text, ok := read(dir, "cpuacct.usage"); ok {
			if nanos, err := strconv.ParseUint(text, 10, 64); err == nil {
				stats.cpuUsageMicros, stats.cpuUsageOK = nanos/1000, true
				break
			}
		}
	}
	for _, dir := range []string{"cpu", "cpu,cpuacct"} {
		quota, okQuota := read(dir, "cpu.cfs_quota_us")
		period, okPeriod := read(dir, "cpu.cfs_period_us")
		if okQuota && okPeriod {
			stats.cpuLimit = parseCPUMax(quota + " " + period)
			break
		}
	}
	if current, ok := read("memory", "memory.usage_in_bytes"); ok {
		if usage, err := strconv.ParseUint(current, 10, 64); err == nil {
			inactive := uint64(0)
			if text, ok := read("memory", "memory.stat"); ok {
				inactive, _ = statValue(text, "total_inactive_file")
			}
			stats.memoryUsage, stats.memoryOK = saturatingSub(usage, inactive), true
		}
	}
	if text, ok := read("memory", "memory.limit_in_bytes"); ok {
		stats.memoryLimit = parseMemoryLimit(text)
	}
	return stats
}

// parseCPUMax reads cgroup v2 "quota period" (or v1 values joined the same
// way) and returns the CPU limit; "max" or a negative v1 quota means none.
func parseCPUMax(text string) float64 {
	fields := strings.Fields(text)
	if len(fields) != 2 || fields[0] == "max" {
		return 0
	}
	quota, errQuota := strconv.ParseFloat(fields[0], 64)
	period, errPeriod := strconv.ParseFloat(fields[1], 64)
	if errQuota != nil || errPeriod != nil || quota <= 0 || period <= 0 {
		return 0
	}
	return quota / period
}

// parseMemoryLimit treats "max" and the v1 page-rounded maximum as no limit.
func parseMemoryLimit(text string) uint64 {
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil || value >= 1<<62 {
		return 0
	}
	return value
}

func statValue(text, key string) (uint64, bool) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == key {
			value, err := strconv.ParseUint(fields[1], 10, 64)
			return value, err == nil
		}
	}
	return 0, false
}

func saturatingSub(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}

// cgroupCPUSampler converts cumulative cgroup CPU time into utilization of
// the CPU capacity available to the container.
type cgroupCPUSampler struct {
	mu        sync.Mutex
	lastUsage uint64
	lastAt    time.Time
	ok        bool
	averages  linuxPercentSampler
}

func (s *cgroupCPUSampler) sample(usageMicros uint64, capacity float64, now time.Time) (float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	percent := 0.0
	if s.ok && usageMicros >= s.lastUsage && now.After(s.lastAt) && capacity > 0 {
		elapsed := float64(now.Sub(s.lastAt).Microseconds())
		percent = math.Max(0, math.Min(100, float64(usageMicros-s.lastUsage)*100/(elapsed*capacity)))
	}
	s.lastUsage, s.lastAt, s.ok = usageMicros, now, true
	return s.averages.sampleValue(percent)
}

// containerCPUCapacity is the number of CPUs the container can use: its
// quota when one is set below the CPUs it may be scheduled on.
func containerCPUCapacity(limit float64, schedulable int) float64 {
	if limit > 0 && limit < float64(schedulable) {
		return limit
	}
	return float64(schedulable)
}

// processUptime derives how long PID 1 (the container's init) has run from
// /proc/1/stat start time and /proc/uptime.
func processUptime(stat string, systemUptime time.Duration) time.Duration {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0
	}
	fields := strings.Fields(stat[end+1:])
	// Fields after the command start at field 3 (state); starttime is field 22.
	if len(fields) < 20 {
		return 0
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0
	}
	started := time.Duration(ticks) * time.Second / userHZ
	if started > systemUptime {
		return 0
	}
	return systemUptime - started
}

func hostCPUCount() int {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "cpu") && len(line) > 3 && line[3] >= '0' && line[3] <= '9' {
			count++
		}
	}
	return count
}

// applyContainerStatus replaces host-wide CPU and memory readings with the
// container's own usage and limits. Host totals remain as context.
func applyContainerStatus(status *model.HostStatus, runtimeName string, stats cgroupStats, now time.Time) {
	schedulable := runtime.NumCPU()
	container := &model.ContainerStatus{Runtime: runtimeName, HostCPUCount: hostCPUCount(), HostMemoryBytes: status.MemoryTotalBytes}
	if stats.cpuLimit > 0 {
		container.CPULimit = math.Round(stats.cpuLimit*100) / 100
	}
	capacity := containerCPUCapacity(stats.cpuLimit, schedulable)
	status.CPUCount = int(math.Ceil(capacity))
	if stats.cpuUsageOK {
		status.CPUPercent, status.CPUAveragePercent = containerCPUSampler.sample(stats.cpuUsageMicros, capacity, now)
		status.CPUAvailable = true
	} else {
		status.CPUAvailable = false
	}
	if stats.memoryOK {
		total := status.MemoryTotalBytes
		if stats.memoryLimit > 0 && (total == 0 || stats.memoryLimit < total) {
			container.MemoryLimitBytes = stats.memoryLimit
			total = stats.memoryLimit
		}
		status.MemoryTotalBytes = total
		status.MemoryFreeBytes = saturatingSub(total, stats.memoryUsage)
	}
	// The load average is host-wide and not meaningful against a container's share.
	status.LoadAverage = nil
	if stat, err := os.ReadFile("/proc/1/stat"); err == nil {
		container.UptimeSeconds = int64(processUptime(string(stat), HostUptime()) / time.Second)
	}
	status.Container = container
}
