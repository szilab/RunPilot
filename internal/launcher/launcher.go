package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
)

func Build(spec model.CommandSpec) (*exec.Cmd, error) {
	if strings.TrimSpace(spec.Path) == "" {
		return nil, fmt.Errorf("command path is required")
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
	cmd.Env = os.Environ()
	for k, v := range spec.Environment {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	platform.ConfigureCommand(cmd)
	return cmd, nil
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
