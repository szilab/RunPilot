package model

import "time"

type RestartMode string

const (
	RestartNever     RestartMode = "never"
	RestartOnFailure RestartMode = "on-failure"
	RestartAlways    RestartMode = "always"
)

type RestartPolicy struct {
	Mode                RestartMode `json:"mode" yaml:"mode"`
	InitialDelaySeconds int         `json:"initialDelaySeconds" yaml:"initialDelaySeconds"`
	MaxDelaySeconds     int         `json:"maxDelaySeconds" yaml:"maxDelaySeconds"`
	MaxRetries          int         `json:"maxRetries" yaml:"maxRetries"`
}

type CommandSpec struct {
	Path             string            `json:"path" yaml:"path"`
	Args             []string          `json:"args,omitempty" yaml:"args,omitempty"`
	WorkingDirectory string            `json:"workingDirectory,omitempty" yaml:"workingDirectory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty" yaml:"environment,omitempty"`
	Interpreter      string            `json:"interpreter,omitempty" yaml:"interpreter,omitempty"`
}

type ProcessDefinition struct {
	ID          string        `json:"id" yaml:"id"`
	Name        string        `json:"name" yaml:"name"`
	Description string        `json:"description,omitempty" yaml:"description,omitempty"`
	Autostart   bool          `json:"autostart" yaml:"autostart"`
	Command     CommandSpec   `json:"command" yaml:"command"`
	Restart     RestartPolicy `json:"restart" yaml:"restart"`
}

type ScheduleType string

const (
	ScheduleInterval ScheduleType = "interval"
	ScheduleDaily    ScheduleType = "daily"
	ScheduleCron     ScheduleType = "cron"
)

type ScheduleSpec struct {
	Type            ScheduleType `json:"type" yaml:"type"`
	IntervalSeconds int          `json:"intervalSeconds,omitempty" yaml:"intervalSeconds,omitempty"`
	TimeOfDay       string       `json:"timeOfDay,omitempty" yaml:"timeOfDay,omitempty"`
	Cron            string       `json:"cron,omitempty" yaml:"cron,omitempty"`
	TimeZone        string       `json:"timeZone,omitempty" yaml:"timeZone,omitempty"`
}

type JobType string

const (
	JobCommand JobType = "command"
	JobBackup  JobType = "backup"
)

type BackupMode string

const (
	BackupCopy   BackupMode = "copy"
	BackupMirror BackupMode = "mirror"
)

type BackupSpec struct {
	Engine           string     `json:"engine" yaml:"engine"`
	Source           string     `json:"source" yaml:"source"`
	Destination      string     `json:"destination" yaml:"destination"`
	Mode             BackupMode `json:"mode" yaml:"mode"`
	ExcludeDirs      []string   `json:"excludeDirs,omitempty" yaml:"excludeDirs,omitempty"`
	ExcludeFiles     []string   `json:"excludeFiles,omitempty" yaml:"excludeFiles,omitempty"`
	Retries          int        `json:"retries" yaml:"retries"`
	RetryWaitSeconds int        `json:"retryWaitSeconds" yaml:"retryWaitSeconds"`
	AdditionalArgs   []string   `json:"additionalArgs,omitempty" yaml:"additionalArgs,omitempty"`
}

type JobDefinition struct {
	ID             string       `json:"id" yaml:"id"`
	Name           string       `json:"name" yaml:"name"`
	Description    string       `json:"description,omitempty" yaml:"description,omitempty"`
	Enabled        bool         `json:"enabled" yaml:"enabled"`
	Type           JobType      `json:"type" yaml:"type"`
	Schedule       ScheduleSpec `json:"schedule" yaml:"schedule"`
	OverlapPolicy  string       `json:"overlapPolicy" yaml:"overlapPolicy"`
	TimeoutSeconds int          `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
	Command        *CommandSpec `json:"command,omitempty" yaml:"command,omitempty"`
	Backup         *BackupSpec  `json:"backup,omitempty" yaml:"backup,omitempty"`
}

type ServerConfig struct {
	Bind     string `json:"bind" yaml:"bind"`
	Port     int    `json:"port,omitempty" yaml:"port,omitempty"`
	BasePath string `json:"basePath,omitempty" yaml:"basePath,omitempty"`
	Token    string `json:"token" yaml:"token"`
}

type Config struct {
	Version   int                 `json:"version" yaml:"version"`
	Server    ServerConfig        `json:"server" yaml:"server"`
	Processes []ProcessDefinition `json:"processes" yaml:"processes"`
	Jobs      []JobDefinition     `json:"jobs" yaml:"jobs"`
	Storage   []StorageDefinition `json:"storage" yaml:"storage"`
	Software  SoftwareConfig      `json:"software" yaml:"software"`
}

// SoftwareConfig contains the external providers RunPilot manages for its
// Software capability. Provider-specific settings stay typed below.
type SoftwareConfig struct {
	Providers []SoftwareProviderDefinition `json:"providers" yaml:"providers"`
}

type SoftwareProviderType string

const SoftwareProviderScoop SoftwareProviderType = "scoop"

type ScoopProviderSpec struct {
	// Root is the RunPilot-owned Scoop root. An empty value selects the root
	// derived from the active RunPilot data directory.
	Root string `json:"root,omitempty" yaml:"root,omitempty"`
}

type SoftwareProviderDefinition struct {
	ID    string               `json:"id" yaml:"id"`
	Name  string               `json:"name" yaml:"name"`
	Type  SoftwareProviderType `json:"type" yaml:"type"`
	Scoop *ScoopProviderSpec   `json:"scoop,omitempty" yaml:"scoop,omitempty"`
}

// Storage definitions describe locations RunPilot can manage; deleting one
// never alters the data at that location.
type StorageType string

const StorageLocal StorageType = "local"

type LocalStorageScope string

const (
	LocalStorageScopeRoot LocalStorageScope = "root"
	LocalStorageScopeHost LocalStorageScope = "host"
)

type LocalStorageSpec struct {
	Scope LocalStorageScope `json:"scope" yaml:"scope"`
	Root  string            `json:"root,omitempty" yaml:"root,omitempty"`
}

type StorageDefinition struct {
	ID    string            `json:"id" yaml:"id"`
	Name  string            `json:"name" yaml:"name"`
	Type  StorageType       `json:"type" yaml:"type"`
	Local *LocalStorageSpec `json:"local,omitempty" yaml:"local,omitempty"`
}

type DiskStatus struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"totalBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
}

type HostStatus struct {
	OS                string       `json:"os"`
	Hostname          string       `json:"hostname"`
	CPUPercent        float64      `json:"cpuPercent"`
	CPUAveragePercent float64      `json:"cpuAveragePercent"`
	GPUPercent        float64      `json:"gpuPercent"`
	GPUAveragePercent float64      `json:"gpuAveragePercent"`
	GPUAvailable      bool         `json:"gpuAvailable"`
	MemoryTotalBytes  uint64       `json:"memoryTotalBytes"`
	MemoryFreeBytes   uint64       `json:"memoryFreeBytes"`
	Disks             []DiskStatus `json:"disks"`
	Error             string       `json:"error,omitempty"`
}

type HealthIssue struct {
	Source  string `json:"source"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

type Overview struct {
	Host             HostStatus    `json:"host"`
	ProcessCount     int           `json:"processCount"`
	RunningProcesses int           `json:"runningProcesses"`
	JobCount         int           `json:"jobCount"`
	RunningJobs      int           `json:"runningJobs"`
	Issues           []HealthIssue `json:"issues"`
}

type ProcessStatus struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	State        string     `json:"state"`
	PID          int        `json:"pid,omitempty"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	ExitCode     *int       `json:"exitCode,omitempty"`
	RestartCount int        `json:"restartCount"`
	CurrentRunID string     `json:"currentRunId,omitempty"`
}

type JobStatus struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Running      bool       `json:"running"`
	CurrentRunID string     `json:"currentRunId,omitempty"`
	LastRunAt    *time.Time `json:"lastRunAt,omitempty"`
	LastExitCode *int       `json:"lastExitCode,omitempty"`
	LastSuccess  *bool      `json:"lastSuccess,omitempty"`
}

type RunRecord struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	TargetID   string     `json:"targetId"`
	TargetName string     `json:"targetName"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	Success    *bool      `json:"success,omitempty"`
	LogPath    string     `json:"logPath"`
	Message    string     `json:"message,omitempty"`
}
