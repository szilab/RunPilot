package platform

import "runtime"

// Capabilities describes the OS integrations exposed by this RunPilot build.
// It is deliberately small so UI and API clients need not guess from GOOS.
type Capabilities struct {
	OS                  string   `json:"os"`
	Windows             bool     `json:"windows"`
	Linux               bool     `json:"linux"`
	ServiceManager      string   `json:"serviceManager,omitempty"`
	CommandInterpreters []string `json:"commandInterpreters"`
	Robocopy            bool     `json:"robocopy"`
	Scoop               bool     `json:"scoop"`
	Terminal            bool     `json:"terminal"`
	DockerCompose       bool     `json:"dockerCompose"`
}

func CurrentCapabilities() Capabilities {
	c := Capabilities{OS: runtime.GOOS, Windows: runtime.GOOS == "windows", Linux: runtime.GOOS == "linux"}
	c.CommandInterpreters = []string{"direct", "python", "powershell"}
	if c.Windows {
		c.ServiceManager = "windows-scm"
		c.CommandInterpreters = append(c.CommandInterpreters, "cmd")
		c.Robocopy, c.Scoop = true, true
	} else if c.Linux {
		c.ServiceManager = "systemd"
		c.CommandInterpreters = append(c.CommandInterpreters, "sh", "bash", "sh-inline")
	}
	c.Terminal = c.Windows || c.Linux
	c.DockerCompose = c.Linux
	return c
}
