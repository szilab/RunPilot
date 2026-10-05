//go:build linux

package platform

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

type linuxCPUTimes struct {
	idle  uint64
	total uint64
}

type linuxPercentSample struct {
	at      time.Time
	percent float64
}

type linuxPercentSampler struct {
	mu      sync.Mutex
	last    linuxCPUTimes
	ok      bool
	samples []linuxPercentSample
}

var (
	linuxCPUSampler    linuxPercentSampler
	linuxGPUSampler    linuxPercentSampler
	linuxCPUModelOnce  sync.Once
	linuxCPUModelValue string
)

func HostStatus() model.HostStatus {
	hostname, err := os.Hostname()
	status := model.HostStatus{OS: runtime.GOOS, Architecture: runtime.GOARCH, Hostname: hostname, CPUCount: runtime.NumCPU(), Disks: []model.DiskStatus{}}
	if err != nil {
		status.Error = fmt.Sprintf("read hostname: %v", err)
	}
	if total, free, err := linuxMemory(); err != nil {
		status.Error = appendStatusError(status.Error, err.Error())
	} else {
		status.MemoryTotalBytes, status.MemoryFreeBytes = total, free
	}
	if times, err := linuxCPUTimesNow(); err != nil {
		status.Error = appendStatusError(status.Error, err.Error())
	} else {
		status.CPUPercent, status.CPUAveragePercent = linuxCPUSampler.sample(times)
		status.CPUAvailable = true
	}
	status.CPUModel = linuxCPUModel()
	status.LoadAverage = linuxLoadAverage()
	if gpu, available := linuxGPUStatus(); available {
		status.GPUPercent, status.GPUAveragePercent = linuxGPUSampler.sampleValue(gpu.Percent)
		status.GPUAvailable = true
		status.GPUName, status.GPUMemoryUsed, status.GPUMemoryTotal = gpu.Name, gpu.MemoryUsed, gpu.MemoryTotal
	}
	if disks, err := linuxDisks(); err != nil {
		status.Error = appendStatusError(status.Error, err.Error())
	} else {
		status.Disks = disks
	}
	return status
}

func linuxCPUTimesNow() (linuxCPUTimes, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return linuxCPUTimes{}, fmt.Errorf("read CPU: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var values []uint64
		for _, field := range fields[1:] {
			v, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return linuxCPUTimes{}, fmt.Errorf("read CPU: %w", err)
			}
			values = append(values, v)
		}
		var total uint64
		for _, value := range values {
			total += value
		}
		idle := values[3]
		if len(values) > 4 {
			idle += values[4] // iowait is not CPU work.
		}
		return linuxCPUTimes{idle: idle, total: total}, nil
	}
	return linuxCPUTimes{}, fmt.Errorf("read CPU: aggregate CPU counters unavailable")
}

func (s *linuxPercentSampler) sample(current linuxCPUTimes) (float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ok {
		s.last, s.ok = current, true
		return 0, 0
	}
	previous := s.last
	s.last = current
	if current.total <= previous.total || current.idle < previous.idle {
		return 0, s.average(time.Now())
	}
	percent := float64((current.total-previous.total)-(current.idle-previous.idle)) * 100 / float64(current.total-previous.total)
	_, average := s.sampleValueLocked(percent)
	return percent, average
}

func (s *linuxPercentSampler) sampleValue(percent float64) (float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sampleValueLocked(percent)
}

func (s *linuxPercentSampler) sampleValueLocked(percent float64) (float64, float64) {
	now := time.Now()
	s.samples = append(s.samples, linuxPercentSample{at: now, percent: percent})
	return percent, s.average(now)
}

func (s *linuxPercentSampler) average(now time.Time) float64 {
	cutoff := now.Add(-time.Minute)
	first, total := 0, 0.0
	for first < len(s.samples) && s.samples[first].at.Before(cutoff) {
		first++
	}
	s.samples = s.samples[first:]
	for _, sample := range s.samples {
		total += sample.percent
	}
	if len(s.samples) == 0 {
		return 0
	}
	return total / float64(len(s.samples))
}

func linuxCPUModel() string {
	linuxCPUModelOnce.Do(func() {
		b, err := os.ReadFile("/proc/cpuinfo")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(b), "\n") {
			name, value, ok := strings.Cut(line, ":")
			if ok && (strings.TrimSpace(name) == "model name" || strings.TrimSpace(name) == "Hardware") {
				linuxCPUModelValue = strings.TrimSpace(value)
				return
			}
		}
	})
	return linuxCPUModelValue
}

func linuxLoadAverage() []float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return nil
	}
	load := make([]float64, 0, 3)
	for _, field := range fields[:3] {
		value, err := strconv.ParseFloat(field, 64)
		if err != nil || value < 0 {
			return nil
		}
		load = append(load, value)
	}
	return load
}

type gpuReading struct {
	Percent     float64
	Name        string
	MemoryUsed  uint64
	MemoryTotal uint64
}

// linuxGPUStatus uses standard vendor interfaces when present. Absence of a
// compatible driver simply leaves GPU metrics unavailable instead of treating
// it as a host error.
func linuxGPUStatus() (gpuReading, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,utilization.gpu,memory.used,memory.total", "--format=csv,noheader,nounits").Output(); err == nil {
		if gpu, ok := parseNvidiaGPU(output); ok {
			return gpu, true
		}
	}
	paths, _ := filepath.Glob("/sys/class/drm/card*/device/gpu_busy_percent")
	intelPaths, _ := filepath.Glob("/sys/class/drm/card*/device/gt_busy_percent")
	paths = append(paths, intelPaths...)
	for _, path := range paths {
		if value, err := os.ReadFile(path); err == nil {
			if percent, ok := maximumPercent(strings.Fields(string(value))); ok {
				return gpuReading{Percent: percent}, true
			}
		}
	}
	return gpuReading{}, false
}

func parseNvidiaGPU(output []byte) (gpuReading, bool) {
	records, err := csv.NewReader(strings.NewReader(string(output))).ReadAll()
	if err != nil {
		return gpuReading{}, false
	}
	var best gpuReading
	found := false
	for _, record := range records {
		if len(record) != 4 {
			continue
		}
		percent, err := strconv.ParseFloat(strings.TrimSpace(record[1]), 64)
		if err != nil || percent < 0 {
			continue
		}
		if !found || percent > best.Percent {
			best = gpuReading{Percent: percent, Name: strings.TrimSpace(record[0])}
			if used, err := strconv.ParseUint(strings.TrimSpace(record[2]), 10, 64); err == nil {
				best.MemoryUsed = used * 1024 * 1024
			}
			if total, err := strconv.ParseUint(strings.TrimSpace(record[3]), 10, 64); err == nil {
				best.MemoryTotal = total * 1024 * 1024
			}
			found = true
		}
	}
	if best.Percent > 100 {
		best.Percent = 100
	}
	return best, found
}

func maximumPercent(fields []string) (float64, bool) {
	max, found := 0.0, false
	for _, field := range fields {
		value := strings.TrimSuffix(strings.TrimSpace(field), "%")
		percent, err := strconv.ParseFloat(value, 64)
		if err == nil && percent >= 0 {
			if percent > max {
				max = percent
			}
			found = true
		}
	}
	if max > 100 {
		max = 100
	}
	return max, found
}

func linuxMemory() (uint64, uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, fmt.Errorf("read memory: %w", err)
	}
	defer f.Close()
	values := map[string]uint64{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		if v, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
			values[strings.TrimSuffix(fields[0], ":")] = v * 1024
		}
	}
	if err := s.Err(); err != nil {
		return 0, 0, fmt.Errorf("read memory: %w", err)
	}
	total := values["MemTotal"]
	if total == 0 {
		return 0, 0, fmt.Errorf("read memory: MemTotal unavailable")
	}
	free := values["MemAvailable"]
	if free == 0 {
		free = values["MemFree"] + values["Buffers"] + values["Cached"]
	}
	return total, free, nil
}

func linuxDisks() ([]model.DiskStatus, error) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read filesystems: %w", err)
	}
	seen := map[string]bool{}
	disks := []model.DiskStatus{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if len(fields) < 5 || separator < 0 || separator+2 >= len(fields) {
			continue
		}
		mount := strings.ReplaceAll(strings.ReplaceAll(fields[4], `\040`, " "), `\011`, "\t")
		device := fields[separator+2]
		if seen[device] || !filepath.IsAbs(mount) || !strings.HasPrefix(device, "/dev/") {
			continue
		}
		seen[device] = true
		var fs syscall.Statfs_t
		if err := syscall.Statfs(mount, &fs); err != nil || fs.Blocks == 0 {
			continue
		}
		unit := uint64(fs.Bsize)
		disks = append(disks, model.DiskStatus{Device: device, Label: linuxDiskLabel(device), Path: mount, TotalBytes: fs.Blocks * unit, FreeBytes: fs.Bavail * unit})
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i].Path < disks[j].Path })
	return disks, nil
}

// linuxDiskLabel reads udev's stable by-label links instead of invoking an
// external command. A filesystem may legitimately have no label.
func linuxDiskLabel(device string) string {
	canonical, err := filepath.EvalSymlinks(device)
	if err != nil {
		canonical = device
	}
	entries, err := os.ReadDir("/dev/disk/by-label")
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		candidate, err := filepath.EvalSymlinks(filepath.Join("/dev/disk/by-label", entry.Name()))
		if err == nil && candidate == canonical {
			return entry.Name()
		}
	}
	return ""
}

func appendStatusError(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "; " + next
}
