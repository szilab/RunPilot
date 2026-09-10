//go:build windows

package platform

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"
	"unsafe"

	"github.com/szilab/RunPilot/internal/model"
	"syscall"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGetDriveTypeW        = kernel32.NewProc("GetDriveTypeW")
	procGetDiskFreeSpaceExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
	procFindFirstVolumeW     = kernel32.NewProc("FindFirstVolumeW")
	procFindNextVolumeW      = kernel32.NewProc("FindNextVolumeW")
	procFindVolumeClose      = kernel32.NewProc("FindVolumeClose")
	procGetVolumePathNamesW  = kernel32.NewProc("GetVolumePathNamesForVolumeNameW")
	pdh                      = syscall.NewLazyDLL("pdh.dll")
	procPdhOpenQueryW        = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounter = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData  = pdh.NewProc("PdhCollectQueryData")
	procPdhGetCounterArrayW  = pdh.NewProc("PdhGetFormattedCounterArrayW")
	hostCPUSampler           cpuSampler
	hostGPUSampler           gpuSampler
)

const (
	driveFixed       = 3
	errorNoMoreFiles = 18
	pdhFmtDouble     = 0x00000200
	pdhMoreData      = 0x800007D2
	gpuCounterPath   = `\GPU Engine(*)\Utilization Percentage`
	cpuAverageWindow = time.Minute
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailablePhys        uint64
	TotalPageFile        uint64
	AvailablePageFile    uint64
	TotalVirtual         uint64
	AvailableVirtual     uint64
	AvailableExtendedVRM uint64
}

type fileTime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

type cpuTimes struct {
	idle   uint64
	kernel uint64
	user   uint64
}

type cpuSampler struct {
	mu      sync.Mutex
	last    cpuTimes
	ok      bool
	samples []percentSample
}

type percentSample struct {
	at      time.Time
	percent float64
}

type gpuSampler struct {
	mu      sync.Mutex
	query   uintptr
	counter uintptr
	ready   bool
	samples []percentSample
}

type pdhFmtCounterValue struct {
	Status uint32
	_      uint32
	Value  float64
}

type pdhFmtCounterValueItem struct {
	Name  *uint16
	Value pdhFmtCounterValue
}

func HostStatus() model.HostStatus {
	hostname, err := os.Hostname()
	status := model.HostStatus{OS: runtime.GOOS, Hostname: hostname, Disks: []model.DiskStatus{}}
	if err != nil {
		status.Error = fmt.Sprintf("read hostname: %v", err)
	}

	mem := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if ok, _, callErr := procGlobalMemoryStatusEx.Call(uintptr(unsafePointer(&mem))); ok == 0 {
		status.Error = appendStatusError(status.Error, fmt.Sprintf("read memory: %v", callErr))
	} else {
		status.MemoryTotalBytes = mem.TotalPhys
		status.MemoryFreeBytes = mem.AvailablePhys
	}

	if times, err := readCPUTimes(); err != nil {
		status.Error = appendStatusError(status.Error, err.Error())
	} else {
		status.CPUPercent, status.CPUAveragePercent = hostCPUSampler.sample(times)
	}

	if gpuPercent, gpuAverage, available := hostGPUSampler.percent(); available {
		status.GPUPercent, status.GPUAveragePercent = gpuPercent, gpuAverage
		status.GPUAvailable = true
	}

	disks, err := readDisks()
	if err != nil {
		status.Error = appendStatusError(status.Error, err.Error())
	} else {
		status.Disks = disks
	}
	return status
}

func readCPUTimes() (cpuTimes, error) {
	var idle, kernel, user fileTime
	if ok, _, err := procGetSystemTimes.Call(uintptr(unsafePointer(&idle)), uintptr(unsafePointer(&kernel)), uintptr(unsafePointer(&user))); ok == 0 {
		return cpuTimes{}, fmt.Errorf("read CPU: %w", err)
	}
	return cpuTimes{idle: fileTimeValue(idle), kernel: fileTimeValue(kernel), user: fileTimeValue(user)}, nil
}

func (s *cpuSampler) sample(current cpuTimes) (float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ok {
		s.last, s.ok = current, true
		return 0, 0
	}
	previous := s.last
	s.last = current
	total := (current.kernel - previous.kernel) + (current.user - previous.user)
	idle := current.idle - previous.idle
	if total == 0 || idle > total {
		return 0, averagePercentSamples(s.samples)
	}
	percent := float64(total-idle) * 100 / float64(total)
	now := time.Now()
	s.samples = append(s.samples, percentSample{at: now, percent: percent})
	s.samples = trimPercentSamples(s.samples, now)
	return percent, averagePercentSamples(s.samples)
}

func trimPercentSamples(samples []percentSample, now time.Time) []percentSample {
	cutoff := now.Add(-cpuAverageWindow)
	first := 0
	for first < len(samples) && samples[first].at.Before(cutoff) {
		first++
	}
	return samples[first:]
}

func averagePercentSamples(samples []percentSample) float64 {
	if len(samples) == 0 {
		return 0
	}
	total := 0.0
	for _, sample := range samples {
		total += sample.percent
	}
	return total / float64(len(samples))
}

// percent reports the busiest GPU engine reported by Windows. GPU engines can
// run concurrently, so summing their values can exceed 100% and is misleading.
func (s *gpuSampler) percent() (float64, float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready && !s.initialize() {
		return 0, 0, false
	}
	if status, _, _ := procPdhCollectQueryData.Call(s.query); uint32(status) != 0 {
		return 0, 0, false
	}
	var bufferSize, itemCount uint32
	status, _, _ := procPdhGetCounterArrayW.Call(s.counter, pdhFmtDouble, uintptr(unsafe.Pointer(&bufferSize)), uintptr(unsafe.Pointer(&itemCount)), 0)
	if uint32(status) != pdhMoreData || bufferSize == 0 || itemCount == 0 {
		return 0, 0, false
	}
	buffer := make([]byte, bufferSize)
	status, _, _ = procPdhGetCounterArrayW.Call(s.counter, pdhFmtDouble, uintptr(unsafe.Pointer(&bufferSize)), uintptr(unsafe.Pointer(&itemCount)), uintptr(unsafe.Pointer(&buffer[0])))
	if uint32(status) != 0 {
		return 0, 0, false
	}
	itemSize := unsafe.Sizeof(pdhFmtCounterValueItem{})
	max := 0.0
	for i := uint32(0); i < itemCount; i++ {
		item := (*pdhFmtCounterValueItem)(unsafe.Pointer(uintptr(unsafe.Pointer(&buffer[0])) + uintptr(i)*itemSize))
		if item.Value.Status == 0 && item.Value.Value > max {
			max = item.Value.Value
		}
	}
	if max > 100 {
		max = 100
	}
	now := time.Now()
	s.samples = append(s.samples, percentSample{at: now, percent: max})
	s.samples = trimPercentSamples(s.samples, now)
	return max, averagePercentSamples(s.samples), true
}

func (s *gpuSampler) initialize() bool {
	status, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&s.query)))
	if uint32(status) != 0 {
		return false
	}
	path, err := syscall.UTF16PtrFromString(gpuCounterPath)
	if err != nil {
		return false
	}
	status, _, _ = procPdhAddEnglishCounter.Call(s.query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&s.counter)))
	if uint32(status) != 0 {
		return false
	}
	s.ready = true
	return true
}

func readDisks() ([]model.DiskStatus, error) {
	disks := make([]model.DiskStatus, 0)
	seen := make(map[string]bool)
	volumeName := make([]uint16, 1024)
	handle, _, err := procFindFirstVolumeW.Call(uintptr(unsafe.Pointer(&volumeName[0])), uintptr(len(volumeName)))
	if handle == ^uintptr(0) {
		return nil, fmt.Errorf("find volumes: %w", err)
	}
	defer procFindVolumeClose.Call(handle)
	for {
		for _, path := range volumePaths(volumeName) {
			if !seen[path] && readFixedDisk(path, &disks) {
				seen[path] = true
			}
		}
		ok, _, callErr := procFindNextVolumeW.Call(handle, uintptr(unsafe.Pointer(&volumeName[0])), uintptr(len(volumeName)))
		if ok != 0 {
			continue
		}
		if callErr == syscall.Errno(errorNoMoreFiles) {
			break
		}
		return nil, fmt.Errorf("find next volume: %w", callErr)
	}
	sort.Slice(disks, func(i, j int) bool {
		left, right := isDriveLetterPath(disks[i].Path), isDriveLetterPath(disks[j].Path)
		if left != right {
			return left
		}
		return disks[i].Path < disks[j].Path
	})
	return disks, nil
}

func isDriveLetterPath(path string) bool {
	return len(path) == 3 && path[1] == ':' && path[2] == '\\' && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z'))
}

func volumePaths(volumeName []uint16) []string {
	var needed uint32
	buffer := make([]uint16, 1024)
	for {
		ok, _, callErr := procGetVolumePathNamesW.Call(uintptr(unsafe.Pointer(&volumeName[0])), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(&needed)))
		if ok != 0 {
			return splitUTF16MultiString(buffer)
		}
		if callErr != syscall.ERROR_INSUFFICIENT_BUFFER || needed <= uint32(len(buffer)) {
			return nil
		}
		buffer = make([]uint16, needed)
	}
}

func splitUTF16MultiString(values []uint16) []string {
	paths := make([]string, 0, 1)
	start := 0
	for i, value := range values {
		if value != 0 {
			continue
		}
		if i == start {
			break
		}
		paths = append(paths, syscall.UTF16ToString(values[start:i]))
		start = i + 1
	}
	return paths
}

func readFixedDisk(path string, disks *[]model.DiskStatus) bool {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	driveType, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(pathPtr)))
	if driveType != driveFixed {
		return false
	}
	var free, total uint64
	if ok, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(pathPtr)), uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), 0); ok == 0 {
		return false
	}
	*disks = append(*disks, model.DiskStatus{Path: path, TotalBytes: total, FreeBytes: free})
	return true
}

func fileTimeValue(t fileTime) uint64 { return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime) }

func appendStatusError(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "; " + next
}

// unsafePointer is kept here so host metrics stay isolated from the rest of
// the platform package's Windows API details.
func unsafePointer[T any](value *T) unsafe.Pointer { return unsafe.Pointer(value) }
