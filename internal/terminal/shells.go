package terminal

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DiscoverShells returns only shell executables available to the RunPilot host.
func DiscoverShells() []Shell {
	type candidate struct{ id, name, command string }
	var candidates []candidate
	if runtime.GOOS == "windows" {
		candidates = []candidate{{"pwsh", "PowerShell 7", "pwsh.exe"}, {"powershell", "Windows PowerShell", "powershell.exe"}, {"cmd", "Command Prompt", "cmd.exe"}}
	} else if runtime.GOOS == "linux" {
		if configured := strings.TrimSpace(os.Getenv("SHELL")); configured != "" {
			candidates = append(candidates, candidate{shellFromPath(configured), filepathLabel(configured), configured})
		}
		candidates = append(candidates, candidate{"bash", "Bash", "bash"}, candidate{"sh", "Shell", "sh"}, candidate{"zsh", "Zsh", "zsh"})
	}
	seen := map[string]bool{}
	var shells []Shell
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate.command)
		if err != nil {
			continue
		}
		key := canonicalShellPath(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		shells = append(shells, Shell{ID: candidate.id, Name: candidate.name, Path: path, Available: true})
	}
	return shells
}

func canonicalShellPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func filepathLabel(value string) string {
	name := shellFromPath(value)
	if name == "" {
		return "Default shell"
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
