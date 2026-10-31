package model

type CommandSpec struct {
	Path             string            `json:"path" yaml:"path"`
	Args             []string          `json:"args,omitempty" yaml:"args,omitempty"`
	WorkingDirectory string            `json:"workingDirectory,omitempty" yaml:"workingDirectory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty" yaml:"environment,omitempty"`
	Interpreter      string            `json:"interpreter,omitempty" yaml:"interpreter,omitempty"`
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

type ServerConfig struct {
	Bind                 string `json:"bind" yaml:"bind"`
	Port                 int    `json:"port,omitempty" yaml:"port,omitempty"`
	BasePath             string `json:"basePath,omitempty" yaml:"basePath,omitempty"`
	Token                string `json:"token" yaml:"token"`
	SetupComplete        bool   `json:"setupComplete" yaml:"setupComplete"`
	AdminUsername        string `json:"-" yaml:"adminUsername,omitempty"`
	AdminPasswordHash    string `json:"-" yaml:"adminPasswordHash,omitempty"`
	BearerAuthEnabled    bool   `json:"bearerAuthEnabled" yaml:"bearerAuthEnabled"`
	WebSocketPayloadMode string `json:"websocketPayloadMode,omitempty" yaml:"websocketPayloadMode,omitempty"`
	// Reverse-proxy single sign-on. RUNPILOT_TRUSTED_PROXIES and
	// RUNPILOT_PROXY_AUTH_HEADER replace both values when either is set.
	ProxyAuthTrustedProxies string `json:"-" yaml:"proxyAuthTrustedProxies,omitempty"`
	ProxyAuthHeader         string `json:"-" yaml:"proxyAuthHeader,omitempty"`
}

type Config struct {
	Version        int                  `json:"version" yaml:"version"`
	Server         ServerConfig         `json:"server" yaml:"server"`
	PluginRegistry PluginRegistryConfig `json:"pluginRegistry,omitempty" yaml:"pluginRegistry,omitempty"`
	// Plugins contains mutable administrator choices. Plugin manifests are
	// installation metadata and must never be rewritten when a user toggles a
	// plugin.
	Plugins  map[string]PluginSettings `json:"plugins,omitempty" yaml:"plugins,omitempty"`
	Launcher []LauncherEntry           `json:"launcher,omitempty" yaml:"launcher,omitempty"`
}

// LauncherEntry is framework configuration for an Overview shortcut.
type LauncherEntry struct {
	ID       string `json:"id" yaml:"id"`
	Name     string `json:"name" yaml:"name"`
	URL      string `json:"url" yaml:"url"`
	Icon     string `json:"icon,omitempty" yaml:"icon,omitempty"`
	OpenMode string `json:"openMode" yaml:"openMode"`
}

type PluginRegistryConfig struct {
	URL string `json:"url,omitempty" yaml:"url,omitempty"`
}

type PluginSettings struct {
	// Enabled records an explicit administrator choice; absent means disabled.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
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
	Architecture      string       `json:"architecture"`
	Hostname          string       `json:"hostname"`
	CPUCount          int          `json:"cpuCount,omitempty"`
	CPUModel          string       `json:"cpuModel,omitempty"`
	LoadAverage       []float64    `json:"loadAverage,omitempty"`
	CPUAvailable      bool         `json:"cpuAvailable"`
	CPUPercent        float64      `json:"cpuPercent"`
	CPUAveragePercent float64      `json:"cpuAveragePercent"`
	GPUPercent        float64      `json:"gpuPercent"`
	GPUAveragePercent float64      `json:"gpuAveragePercent"`
	GPUAvailable      bool         `json:"gpuAvailable"`
	GPUName           string       `json:"gpuName,omitempty"`
	GPUMemoryUsed     uint64       `json:"gpuMemoryUsedBytes,omitempty"`
	GPUMemoryTotal    uint64       `json:"gpuMemoryTotalBytes,omitempty"`
	MemoryTotalBytes  uint64       `json:"memoryTotalBytes"`
	MemoryFreeBytes   uint64       `json:"memoryFreeBytes"`
	Disks             []DiskStatus `json:"disks"`
	// Container is set when RunPilot itself runs in a container. CPU, memory
	// and filesystem readings then describe the container, not the host.
	Container *ContainerStatus `json:"container,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// ContainerStatus describes the container RunPilot runs in. Zero limits mean
// the container may use the whole host for that resource.
type ContainerStatus struct {
	Runtime          string  `json:"runtime"`
	CPULimit         float64 `json:"cpuLimit,omitempty"`
	MemoryLimitBytes uint64  `json:"memoryLimitBytes,omitempty"`
	HostCPUCount     int     `json:"hostCpuCount,omitempty"`
	HostMemoryBytes  uint64  `json:"hostMemoryBytes,omitempty"`
	UptimeSeconds    int64   `json:"uptimeSeconds,omitempty"`
}

type HealthIssue struct {
	Source  string `json:"source"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

type Overview struct {
	Host   HostStatus    `json:"host"`
	Issues []HealthIssue `json:"issues"`
}
