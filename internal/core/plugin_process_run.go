package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/plugins"
)

const maxRunTimeoutSeconds = 110
const maxRunOutputBytes = 2 << 20

type runCapture struct {
	mu        sync.Mutex
	b         []byte
	max       int
	truncated bool
	tail      bool
}

func (c *runCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if c.tail {
		c.b = append(c.b, p...)
		if len(c.b) > c.max {
			c.b = append([]byte(nil), c.b[len(c.b)-c.max:]...)
			c.truncated = true
		}
		return n, nil
	}
	space := c.max - len(c.b)
	if space > 0 {
		if space > n {
			space = n
		}
		c.b = append(c.b, p[:space]...)
	}
	if space < n {
		c.truncated = true
	}
	return n, nil
}

type pluginRunInput struct {
	Command            string            `json:"command"`
	Args               []string          `json:"args"`
	WorkingDirectory   string            `json:"workingDirectory"`
	WorkspaceDirectory string            `json:"workspaceDirectory"`
	Environment        map[string]string `json:"environment"`
	TimeoutSeconds     int               `json:"timeoutSeconds"`
	MaxOutputBytes     int               `json:"maxOutputBytes"`
	TailOutput         bool              `json:"tailOutput"`
}

func (h controllerPluginHost) ProcessRun(ctx context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.pluginProcessRun(ctx, owner, raw)
}
func (c *Controller) pluginProcessRun(ctx context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in pluginRunInput
	if len(raw) > 1<<20 || json.Unmarshal(raw, &in) != nil || in.Command == "" || len(in.Command) > maxPluginProcessString || len(in.Args) > maxPluginProcessArgs || len(in.Environment) > maxPluginProcessEnvironment || len(in.WorkingDirectory) > maxPluginProcessString || len(in.WorkspaceDirectory) > 1024 || in.TimeoutSeconds < 0 || in.TimeoutSeconds > maxRunTimeoutSeconds || in.MaxOutputBytes < 0 || in.MaxOutputBytes > maxRunOutputBytes {
		return nil, invalidProcess("invalid bounded process request")
	}
	for _, v := range in.Args {
		if len(v) > maxPluginProcessString {
			return nil, invalidProcess("argument is too long")
		}
	}
	for k, v := range in.Environment {
		if len(k) > 1024 || len(v) > maxPluginProcessString {
			return nil, invalidProcess("environment value is too long")
		}
	}
	if err := model.ValidateEnvironment(in.Environment); err != nil {
		return nil, invalidProcess("%v", err)
	}
	if in.WorkspaceDirectory != "" && in.WorkingDirectory != "" {
		return nil, invalidProcess("choose one working directory")
	}
	dir := in.WorkingDirectory
	if in.WorkspaceDirectory != "" {
		if !validWorkspacePath(in.WorkspaceDirectory, true) {
			return nil, workspaceInvalid("invalid workspace directory")
		}
		root, err := c.workspaceRoot(owner)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		name := in.WorkspaceDirectory
		if name == "" {
			name = "."
		}
		fi, err := root.Stat(name)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			return nil, workspaceInvalid("workspace working directory is not a directory")
		}
		// Reject symlink components: exec.Cmd.Dir is a host path, so no component
		// may change the process working directory beyond the owner workspace.
		part := ""
		for _, p := range strings.Split(name, "/") {
			if p == "." {
				continue
			}
			if part != "" {
				part += "/"
			}
			part += p
			fi, err := root.Lstat(part)
			if err != nil {
				return nil, err
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				return nil, workspaceInvalid("workspace working directory contains a symlink")
			}
		}
		dir, err = c.workspaceHostPath(owner, name)
		if err != nil {
			return nil, err
		}
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = 30
	}
	if in.MaxOutputBytes == 0 {
		in.MaxOutputBytes = 256 << 10
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, in.Command, in.Args...)
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	platform.ConfigureCommand(cmd)
	// CommandContext's default cancellation only kills the immediate process.
	cmd.Cancel = func() error { return platform.KillProcessTree(cmd.Process.Pid) }
	cmd.Env = os.Environ()
	for k, v := range in.Environment {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdout := &runCapture{max: in.MaxOutputBytes, tail: in.TailOutput}
	stderr := &runCapture{max: in.MaxOutputBytes, tail: in.TailOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, &plugins.HostFailure{Code: "not_found", Message: "executable not found"}
	}
	var pathErr *exec.Error
	if errors.As(err, &pathErr) {
		return nil, &plugins.HostFailure{Code: "not_found", Message: "executable not found"}
	}
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	} else if err != nil {
		return nil, &plugins.HostFailure{Code: "failed", Message: err.Error()}
	}
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	return json.Marshal(map[string]any{"exitCode": code, "success": err == nil && !timedOut, "timedOut": timedOut, "stdout": string(stdout.b), "stderr": string(stderr.b), "stdoutTruncated": stdout.truncated, "stderrTruncated": stderr.truncated})
}
