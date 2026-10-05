package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/szilab/RunPilot/internal/pluginapi"
	"strings"
)

var callHost = pluginapi.CallHost

type rpcError struct{ Code, Message string }

func (e *rpcError) Error() string         { return e.Message }
func fail(code, message string) *rpcError { return &rpcError{code, message} }
func hostError(err error) *rpcError {
	if v, ok := pluginapi.AsCapabilityError(err); ok {
		return fail(v.Code, v.Message)
	}
	return fail("failed", err.Error())
}

type plugin struct {
	busy     map[string]bool
	sessions map[string]bool
}

func newPlugin() *plugin { return &plugin{busy: map[string]bool{}, sessions: map[string]bool{}} }
func (p *plugin) shutdown() {
	for id := range p.sessions {
		_ = callHost("process.session.terminate", map[string]any{"id": id, "force": true}, nil)
	}
}
func (p *plugin) event(name string, data json.RawMessage) *rpcError {
	if name != "process.session.output" && name != "process.session.exit" && name != "process.session.error" {
		return nil
	}
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &v) != nil || !p.sessions[v.ID] {
		return nil
	}
	if name != "process.session.output" {
		delete(p.sessions, v.ID)
	}
	if err := callHost("events.publish", map[string]any{"event": name, "data": json.RawMessage(data)}, nil); err != nil {
		return hostError(err)
	}
	return nil
}
func decode(raw json.RawMessage, v any) *rpcError {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fail("invalid_argument", "invalid request")
	}
	return nil
}
func (p *plugin) handle(method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "docker.snapshot":
		return p.snapshot()
	case "docker.projects.create":
		var q struct {
			Name string `json:"name"`
		}
		if e := decode(raw, &q); e != nil {
			return nil, e
		}
		return p.createProject(q.Name)
	case "docker.projects.delete":
		var q struct {
			Name string `json:"name"`
		}
		if e := decode(raw, &q); e != nil {
			return nil, e
		}
		return p.deleteProject(q.Name)
	case "docker.projects.action":
		var q struct {
			Name   string `json:"name"`
			Action string `json:"action"`
		}
		if e := decode(raw, &q); e != nil {
			return nil, e
		}
		return p.projectAction(q.Name, q.Action)
	case "docker.projects.file.get":
		var q struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		}
		if e := decode(raw, &q); e != nil {
			return nil, e
		}
		return p.getFile(q.Name, q.Kind)
	case "docker.projects.file.set":
		var q struct {
			Name    string `json:"name"`
			Kind    string `json:"kind"`
			Content string `json:"content"`
		}
		if e := decode(raw, &q); e != nil {
			return nil, e
		}
		return p.setFile(q.Name, q.Kind, q.Content)
	case "docker.containers.action":
		var q struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		}
		if e := decode(raw, &q); e != nil {
			return nil, e
		}
		return p.containerAction(q.ID, q.Action)
	case "docker.containers.logs":
		var q struct {
			ID string `json:"id"`
		}
		if e := decode(raw, &q); e != nil {
			return nil, e
		}
		return p.containerLogs(q.ID)
	case "docker.containers.terminal.open":
		var q struct {
			ID      string `json:"id"`
			Rows    int    `json:"rows"`
			Columns int    `json:"columns"`
		}
		if e := decode(raw, &q); e != nil {
			return nil, e
		}
		return p.terminalOpen(q.ID, q.Rows, q.Columns)
	case "docker.containers.terminal.write", "docker.containers.terminal.resize", "docker.containers.terminal.status", "docker.containers.terminal.close":
		return p.terminalCommand(method, raw)
	case "docker.volumes.create", "docker.volumes.delete", "docker.networks.create", "docker.networks.delete":
		var q struct {
			Name string `json:"name"`
		}
		if e := decode(raw, &q); e != nil {
			return nil, e
		}
		return p.resourceAction(method, q.Name)
	default:
		return nil, fail("unknown_method", "unknown Docker method")
	}
}

type runResult struct {
	ExitCode        int    `json:"exitCode"`
	Success         bool   `json:"success"`
	TimedOut        bool   `json:"timedOut"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdoutTruncated"`
	StderrTruncated bool   `json:"stderrTruncated"`
}

func run(args []string, dir string, max int, tail ...bool) (runResult, *rpcError) {
	var out runResult
	in := map[string]any{"command": "docker", "args": args, "timeoutSeconds": 100, "maxOutputBytes": max}
	if len(tail) > 0 && tail[0] {
		in["tailOutput"] = true
	}
	if dir != "" {
		in["workspaceDirectory"] = dir
	}
	if err := callHost("process.run", in, &out); err != nil {
		return out, hostError(err)
	}
	if out.TimedOut {
		return out, fail("timed_out", "Docker command timed out")
	}
	if !out.Success {
		message := strings.TrimSpace(out.Stderr)
		if message == "" {
			message = strings.TrimSpace(out.Stdout)
		}
		if len(message) > 8192 {
			message = message[:8192] + " (truncated)"
		}
		if message == "" {
			message = fmt.Sprintf("Docker command exited with code %d", out.ExitCode)
		}
		return out, fail("command_failed", message)
	}
	if out.StdoutTruncated && !(len(tail) > 0 && tail[0]) {
		return out, fail("resource_limit", "Docker output exceeds the configured limit")
	}
	return out, nil
}
func workspace(op, path string, extra map[string]any, out any) *rpcError {
	in := map[string]any{"path": path}
	for k, v := range extra {
		in[k] = v
	}
	if err := callHost("workspace."+op, in, out); err != nil {
		return hostError(err)
	}
	return nil
}
func readFile(path string) (string, *rpcError) {
	var out struct {
		Data string `json:"data"`
	}
	if e := workspace("read", path, nil, &out); e != nil {
		return "", e
	}
	b, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return "", fail("failed", "invalid workspace data")
	}
	return string(b), nil
}
func writeFile(path, content string) *rpcError {
	return workspace("write", path, map[string]any{"data": base64.StdEncoding.EncodeToString([]byte(content))}, nil)
}
func listDir(path string) ([]struct {
	Name      string `json:"name"`
	Directory bool   `json:"directory"`
	Symlink   bool   `json:"symlink"`
}, *rpcError) {
	var out struct {
		Entries []struct {
			Name      string `json:"name"`
			Directory bool   `json:"directory"`
			Symlink   bool   `json:"symlink"`
		} `json:"entries"`
	}
	if e := workspace("list", path, nil, &out); e != nil {
		return nil, e
	}
	return out.Entries, nil
}
func validName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && (r == '-' || r == '_')) {
			return false
		}
	}
	return true
}
func validResourceName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	for i, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || i > 0 && (r == '-' || r == '_' || r == '.')) {
			return false
		}
	}
	return true
}
func validID(id string) bool {
	if len(id) < 12 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
func permissionText(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "permission denied") || strings.Contains(s, "permissiondenied") || strings.Contains(s, "access is denied")
}
