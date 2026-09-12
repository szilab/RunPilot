package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
)

func Build(spec model.CommandSpec) (*exec.Cmd, error) {
	if strings.TrimSpace(spec.Path) == "" {
		return nil, fmt.Errorf("command path is required")
	}
	if err := model.ValidateCommand(spec); err != nil {
		return nil, err
	}

	interpreter := strings.ToLower(strings.TrimSpace(spec.Interpreter))
	if interpreter == "" || interpreter == "auto" {
		interpreter = inferInterpreter(spec.Path)
	}

	var cmd *exec.Cmd
	switch interpreter {
	case "direct":
		cmd = exec.Command(spec.Path, spec.Args...)
	case "powershell":
		args := append([]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", spec.Path}, spec.Args...)
		exe := "powershell.exe"
		if runtime.GOOS != "windows" {
			exe = "pwsh"
		}
		cmd = exec.Command(exe, args...)
	case "cmd":
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("cmd interpreter is only available on Windows")
		}
		line := windowsCommandLine(spec.Path, spec.Args)
		cmd = exec.Command("cmd.exe", "/d", "/s", "/c", line)
	case "sh", "bash":
		if runtime.GOOS == "windows" {
			return nil, fmt.Errorf("%s interpreter is only available on Unix-like systems", interpreter)
		}
		args := append([]string{spec.Path}, spec.Args...)
		cmd = exec.Command(interpreter, args...)
	case "python":
		exe := "python.exe"
		if runtime.GOOS != "windows" {
			exe = "python3"
		}
		args := append([]string{spec.Path}, spec.Args...)
		cmd = exec.Command(exe, args...)
	default:
		return nil, fmt.Errorf("unsupported interpreter %q", interpreter)
	}

	if spec.WorkingDirectory != "" {
		cmd.Dir = spec.WorkingDirectory
	}
	cmd.Env = mergeEnvironment(os.Environ(), spec.Environment, runtime.GOOS == "windows")
	platform.ConfigureCommand(cmd)
	return cmd, nil
}

// mergeEnvironment returns inherited entries with configured values applied.
// Case-insensitive matching is used for Windows, which prevents PATH and Path
// from being passed to a child as competing environment variables.
func mergeEnvironment(inherited []string, configured map[string]string, caseInsensitive bool) []string {
	if len(configured) == 0 {
		return append([]string(nil), inherited...)
	}

	canonicalName := func(name string) string {
		if caseInsensitive {
			return strings.ToUpper(name)
		}
		return name
	}
	overrides := make(map[string]struct{}, len(configured))
	keys := make([]string, 0, len(configured))
	for name := range configured {
		overrides[canonicalName(name)] = struct{}{}
		keys = append(keys, name)
	}
	sort.Strings(keys)

	merged := make([]string, 0, len(inherited)+len(configured))
	for _, entry := range inherited {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, overridden := overrides[canonicalName(name)]; overridden {
				continue
			}
		}
		merged = append(merged, entry)
	}
	for _, name := range keys {
		merged = append(merged, name+"="+configured[name])
	}
	return merged
}

func inferInterpreter(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ps1":
		return "powershell"
	case ".bat", ".cmd":
		return "cmd"
	case ".py":
		return "python"
	case ".sh":
		return "sh"
	case ".bash":
		return "bash"
	default:
		return "direct"
	}
}

func windowsCommandLine(path string, args []string) string {
	parts := []string{quoteCMD(path)}
	for _, a := range args {
		parts = append(parts, quoteCMD(a))
	}
	return strings.Join(parts, " ")
}

func quoteCMD(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t&()[]{}^=;!'+,`~") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
