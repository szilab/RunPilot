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
	Engine      string              `json:"engine" yaml:"engine"`
	Robocopy    *RobocopyBackupSpec `json:"robocopy,omitempty" yaml:"robocopy,omitempty"`
	Restic      *ResticBackupSpec   `json:"restic,omitempty" yaml:"restic,omitempty"`
	RdiffBackup *RdiffBackupSpec    `json:"rdiffBackup,omitempty" yaml:"rdiffBackup,omitempty"`

	// Legacy Robocopy fields are accepted on load and normalized into Robocopy.
	// They remain only to keep existing runpilot.yaml files working.
	Source           string     `json:"source,omitempty" yaml:"source,omitempty"`
	Destination      string     `json:"destination,omitempty" yaml:"destination,omitempty"`
	Mode             BackupMode `json:"mode,omitempty" yaml:"mode,omitempty"`
	ExcludeDirs      []string   `json:"excludeDirs,omitempty" yaml:"excludeDirs,omitempty"`
	ExcludeFiles     []string   `json:"excludeFiles,omitempty" yaml:"excludeFiles,omitempty"`
	Retries          int        `json:"retries,omitempty" yaml:"retries,omitempty"`
	RetryWaitSeconds int        `json:"retryWaitSeconds,omitempty" yaml:"retryWaitSeconds,omitempty"`
	AdditionalArgs   []string   `json:"additionalArgs,omitempty" yaml:"additionalArgs,omitempty"`
}

type RobocopyBackupSpec struct {
	Source           string     `json:"source" yaml:"source"`
	Destination      string     `json:"destination" yaml:"destination"`
	Mode             BackupMode `json:"mode" yaml:"mode"`
	ExcludeDirs      []string   `json:"excludeDirs,omitempty" yaml:"excludeDirs,omitempty"`
	ExcludeFiles     []string   `json:"excludeFiles,omitempty" yaml:"excludeFiles,omitempty"`
	Retries          int        `json:"retries" yaml:"retries"`
	RetryWaitSeconds int        `json:"retryWaitSeconds" yaml:"retryWaitSeconds"`
	AdditionalArgs   []string   `json:"additionalArgs,omitempty" yaml:"additionalArgs,omitempty"`
}

type ResticRetention struct {
	KeepLast    int    `json:"keepLast,omitempty" yaml:"keepLast,omitempty"`
	KeepDaily   int    `json:"keepDaily,omitempty" yaml:"keepDaily,omitempty"`
	KeepWeekly  int    `json:"keepWeekly,omitempty" yaml:"keepWeekly,omitempty"`
	KeepMonthly int    `json:"keepMonthly,omitempty" yaml:"keepMonthly,omitempty"`
	KeepYearly  int    `json:"keepYearly,omitempty" yaml:"keepYearly,omitempty"`
	KeepWithin  string `json:"keepWithin,omitempty" yaml:"keepWithin,omitempty"`
	Prune       bool   `json:"prune,omitempty" yaml:"prune,omitempty"`
}

type ResticBackupSpec struct {
	Executable       string           `json:"executable,omitempty" yaml:"executable,omitempty"`
	Repository       string           `json:"repository" yaml:"repository"`
	PasswordFile     string           `json:"passwordFile,omitempty" yaml:"passwordFile,omitempty"`
	Sources          []string         `json:"sources" yaml:"sources"`
	Excludes         []string         `json:"excludes,omitempty" yaml:"excludes,omitempty"`
	Tags             []string         `json:"tags,omitempty" yaml:"tags,omitempty"`
	UseVSS           bool             `json:"useVss,omitempty" yaml:"useVss,omitempty"`
	Retention        *ResticRetention `json:"retention,omitempty" yaml:"retention,omitempty"`
	CheckAfterBackup bool             `json:"checkAfterBackup,omitempty" yaml:"checkAfterBackup,omitempty"`
}

type RdiffBackupRetention struct {
	OlderThan string `json:"olderThan" yaml:"olderThan"`
}
type RdiffBackupSpec struct {
	Executable        string                `json:"executable,omitempty" yaml:"executable,omitempty"`
	Source            string                `json:"source" yaml:"source"`
	Destination       string                `json:"destination" yaml:"destination"`
	Excludes          []string              `json:"excludes,omitempty" yaml:"excludes,omitempty"`
	Retention         *RdiffBackupRetention `json:"retention,omitempty" yaml:"retention,omitempty"`
	VerifyAfterBackup bool                  `json:"verifyAfterBackup,omitempty" yaml:"verifyAfterBackup,omitempty"`
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
	Version       int                 `json:"version" yaml:"version"`
	Server        ServerConfig        `json:"server" yaml:"server"`
	Processes     []ProcessDefinition `json:"processes" yaml:"processes"`
	Jobs          []JobDefinition     `json:"jobs" yaml:"jobs"`
	Software      SoftwareConfig      `json:"software" yaml:"software"`
	RemoteTargets []RemoteTarget      `json:"remoteTargets" yaml:"remoteTargets"`
	Remote        RemoteConfig        `json:"remote,omitempty" yaml:"remote,omitempty"`
}

// RemoteConfig contains provider-wide settings. Destinations remain provider
// configuration and are deliberately not accepted from browser clients.
type RemoteConfig struct {
	Guacd GuacdConfig `json:"guacd,omitempty" yaml:"guacd,omitempty"`
}

type GuacdConfig struct {
	Host                  string `json:"host,omitempty" yaml:"host,omitempty"`
	Port                  int    `json:"port,omitempty" yaml:"port,omitempty"`
	TLS                   bool   `json:"tls,omitempty" yaml:"tls,omitempty"`
	ConnectTimeoutSeconds int    `json:"connectTimeoutSeconds,omitempty" yaml:"connectTimeoutSeconds,omitempty"`
}

// RemoteTarget is a reusable, administrator-configured graphical workload.
// It intentionally uses CommandSpec so command validation and environment
// semantics stay identical to the rest of RunPilot.
type RemoteTargetType string

const (
	RemoteTargetApplication RemoteTargetType = "application"
	RemoteTargetDesktop     RemoteTargetType = "desktop"
)

type RemoteDBusMode string

const (
	// RemoteDBusIsolated creates a private session bus for the Xpra session when
	// dbus-launch is available. It is the safe default for remote applications.
	RemoteDBusIsolated RemoteDBusMode = "isolated"
	// RemoteDBusHost intentionally passes the RunPilot process's host session
	// bus to the target for compatibility with desktop-session applications.
	RemoteDBusHost RemoteDBusMode = "host-session"
)

type RemoteTarget struct {
	ID       string           `json:"id" yaml:"id"`
	Name     string           `json:"name" yaml:"name"`
	Provider string           `json:"provider" yaml:"provider"`
	Type     RemoteTargetType `json:"type" yaml:"type"`
	Command  CommandSpec      `json:"command" yaml:"command"`
	DBusMode RemoteDBusMode   `json:"dbusMode,omitempty" yaml:"dbusMode,omitempty"`
	// ForwardDBus is accepted only when loading legacy configurations. Normalize
	// converts true to DBusMode host-session and never writes it back.
	ForwardDBus bool `json:"forwardDbus,omitempty" yaml:"forwardDbus,omitempty"`
	// Xpra contains typed provider-specific settings. It is nil in legacy
	// targets; NormalizeXpraRemoteOptions supplies RunPilot defaults at runtime.
	Xpra *XpraRemoteOptions `json:"xpra,omitempty" yaml:"xpra,omitempty"`
	// RDP contains the configured, non-secret RDP endpoint settings. Passwords
	// are intentionally never part of a RemoteTarget.
	RDP *RDPRemoteOptions `json:"rdp,omitempty" yaml:"rdp,omitempty"`
}

type RDPSecurityMode string

const (
	RDPSecurityAutomatic RDPSecurityMode = "automatic"
	RDPSecurityNLA       RDPSecurityMode = "nla"
	RDPSecurityNLAExt    RDPSecurityMode = "nla-ext"
	RDPSecurityTLS       RDPSecurityMode = "tls"
	RDPSecurityRDP       RDPSecurityMode = "rdp"
)

type RDPCertificatePolicy string

const (
	RDPCertificateValidate    RDPCertificatePolicy = "validate"
	RDPCertificateTOFU        RDPCertificatePolicy = "tofu"
	RDPCertificateIgnore      RDPCertificatePolicy = "ignore"
	RDPCertificateFingerprint RDPCertificatePolicy = "fingerprint"
)

// RDPRemoteOptions is deliberately limited to desktop connection settings.
// Credentials are supplied in-memory by the browser for each connection.
type RDPRemoteOptions struct {
	Host                   string               `json:"host" yaml:"host"`
	Port                   int                  `json:"port,omitempty" yaml:"port,omitempty"`
	Username               string               `json:"username,omitempty" yaml:"username,omitempty"`
	Domain                 string               `json:"domain,omitempty" yaml:"domain,omitempty"`
	SecurityMode           RDPSecurityMode      `json:"securityMode,omitempty" yaml:"securityMode,omitempty"`
	Clipboard              *bool                `json:"clipboard,omitempty" yaml:"clipboard,omitempty"`
	DynamicResize          *bool                `json:"dynamicResize,omitempty" yaml:"dynamicResize,omitempty"`
	ServerLayout           string               `json:"serverLayout,omitempty" yaml:"serverLayout,omitempty"`
	ResizeMethod           string               `json:"resizeMethod,omitempty" yaml:"resizeMethod,omitempty"`
	DPIMode                string               `json:"dpiMode,omitempty" yaml:"dpiMode,omitempty"`
	DPI                    int                  `json:"dpi,omitempty" yaml:"dpi,omitempty"`
	ColorDepth             int                  `json:"colorDepth,omitempty" yaml:"colorDepth,omitempty"`
	CertificatePolicy      RDPCertificatePolicy `json:"certificatePolicy,omitempty" yaml:"certificatePolicy,omitempty"`
	CertificateFingerprint string               `json:"certificateFingerprint,omitempty" yaml:"certificateFingerprint,omitempty"`
	Copy                   *bool                `json:"copy,omitempty" yaml:"copy,omitempty"`
	Paste                  *bool                `json:"paste,omitempty" yaml:"paste,omitempty"`
	ClipboardNormalization string               `json:"clipboardNormalization,omitempty" yaml:"clipboardNormalization,omitempty"`
	PerformanceProfile     string               `json:"performanceProfile,omitempty" yaml:"performanceProfile,omitempty"`
	TimeoutSeconds         int                  `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
	TimeZone               string               `json:"timeZone,omitempty" yaml:"timeZone,omitempty"`
}

type XpraProfile string

const (
	XpraProfileRecommended   XpraProfile = "recommended"
	XpraProfileAutomatic     XpraProfile = "automatic"
	XpraProfileCompatibility XpraProfile = "compatibility"
	XpraProfileCustom        XpraProfile = "custom"
)

type XpraDPIMode string

const (
	XpraDPIAuto   XpraDPIMode = "auto"
	XpraDPI96     XpraDPIMode = "96"
	XpraDPICustom XpraDPIMode = "custom"
)

type XpraMenuMode string

const (
	XpraMenuAutohide XpraMenuMode = "autohide"
	XpraMenuVisible  XpraMenuMode = "visible"
	XpraMenuHidden   XpraMenuMode = "hidden"
)

// XpraRemoteOptions is deliberately small and typed. Pointer booleans let a
// partial/legacy YAML target inherit defaults while still allowing false to be
// an explicit user choice.
type XpraRemoteOptions struct {
	Profile            XpraProfile  `json:"profile,omitempty" yaml:"profile,omitempty"`
	Encoding           string       `json:"encoding,omitempty" yaml:"encoding,omitempty"`
	Video              *bool        `json:"video,omitempty" yaml:"video,omitempty"`
	DPIMode            XpraDPIMode  `json:"dpiMode,omitempty" yaml:"dpiMode,omitempty"`
	DPI                int          `json:"dpi,omitempty" yaml:"dpi,omitempty"`
	LaunchAfterConnect *bool        `json:"launchAfterConnect,omitempty" yaml:"launchAfterConnect,omitempty"`
	Clipboard          *bool        `json:"clipboard,omitempty" yaml:"clipboard,omitempty"`
	DynamicResize      *bool        `json:"dynamicResize,omitempty" yaml:"dynamicResize,omitempty"`
	Menu               XpraMenuMode `json:"menu,omitempty" yaml:"menu,omitempty"`
	ToolbarPosition    string       `json:"toolbarPosition,omitempty" yaml:"toolbarPosition,omitempty"`
	Sound              *bool        `json:"sound,omitempty" yaml:"sound,omitempty"`
	Printing           *bool        `json:"printing,omitempty" yaml:"printing,omitempty"`
	FileTransfer       *bool        `json:"fileTransfer,omitempty" yaml:"fileTransfer,omitempty"`
}

type RemoteProviderCapabilities struct {
	ApplicationSessions bool `json:"applicationSessions"`
	DesktopSessions     bool `json:"desktopSessions"`
	Clipboard           bool `json:"clipboard"`
	DynamicResize       bool `json:"dynamicResize"`
	Fullscreen          bool `json:"fullscreen"`
}

type RemoteProviderStatus struct {
	ID                  string                     `json:"id"`
	Name                string                     `json:"name"`
	State               string                     `json:"state"`
	Message             string                     `json:"message,omitempty"`
	InstallHint         string                     `json:"installHint,omitempty"`
	Version             string                     `json:"version,omitempty"`
	Platform            string                     `json:"platform"`
	Capabilities        RemoteProviderCapabilities `json:"capabilities"`
	HTML5Available      bool                       `json:"html5Available"`
	DBusLaunchAvailable bool                       `json:"dbusLaunchAvailable"`
	Warnings            []string                   `json:"warnings,omitempty"`
	XpraDefaults        *XpraRemoteOptions         `json:"xpraDefaults,omitempty"`
}

type RemoteSessionState string

const (
	RemoteSessionStarting RemoteSessionState = "starting"
	RemoteSessionRunning  RemoteSessionState = "running"
	RemoteSessionStopping RemoteSessionState = "stopping"
	RemoteSessionStopped  RemoteSessionState = "stopped"
	RemoteSessionFailed   RemoteSessionState = "failed"
)

// RemoteSession is deliberately runtime-only. Internal endpoints, display
// identifiers, and provider process details never leave the remote package.
type RemoteSession struct {
	ID          string             `json:"id"`
	Provider    string             `json:"provider"`
	TargetID    string             `json:"targetId"`
	TargetName  string             `json:"targetName"`
	Type        RemoteTargetType   `json:"type"`
	State       RemoteSessionState `json:"state"`
	CreatedAt   time.Time          `json:"createdAt"`
	StartedAt   *time.Time         `json:"startedAt,omitempty"`
	StoppedAt   *time.Time         `json:"stoppedAt,omitempty"`
	Failure     string             `json:"failure,omitempty"`
	Message     string             `json:"message,omitempty"`
	WindowCount *int               `json:"windowCount,omitempty"`
	// Xpra is an effective, non-sensitive display configuration snapshot. It
	// remains fixed for the lifetime of this running session.
	Xpra *XpraRemoteOptions `json:"xpra,omitempty"`
	// RDP is an effective, non-secret endpoint snapshot. It remains fixed for
	// the session lifetime; in particular, it contains no password.
	RDP *RDPRemoteOptions `json:"rdp,omitempty"`
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

const (
	StorageLocal        StorageType = "local"
	StorageDockerVolume StorageType = "docker-volume"
)

type LocalStorageScope string

const (
	LocalStorageScopeRoot LocalStorageScope = "root"
	LocalStorageScopeHost LocalStorageScope = "host"
)

type LocalStorageSpec struct {
	Scope LocalStorageScope `json:"scope" yaml:"scope"`
	Root  string            `json:"root,omitempty" yaml:"root,omitempty"`
}

// DockerVolumeStorageSpec identifies a Docker volume without exposing its
// daemon-managed host mountpoint in configuration or API responses.
type DockerVolumeStorageSpec struct {
	Volume string `json:"volume" yaml:"volume"`
}

type StorageDefinition struct {
	ID           string                   `json:"id" yaml:"id"`
	Name         string                   `json:"name" yaml:"name"`
	Type         StorageType              `json:"type" yaml:"type"`
	Local        *LocalStorageSpec        `json:"local,omitempty" yaml:"local,omitempty"`
	DockerVolume *DockerVolumeStorageSpec `json:"dockerVolume,omitempty" yaml:"dockerVolume,omitempty"`
}

type DiskStatus struct {
	Device     string `json:"device,omitempty"`
	Label      string `json:"label,omitempty"`
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
	TaskCount        int           `json:"taskCount"`
	RunningTasks     int           `json:"runningTasks"`
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
