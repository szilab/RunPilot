package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

type inspectedContainer struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	Mounts []struct {
		Type string `json:"Type"`
		Name string `json:"Name"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (p *plugin) inspectContainer(id string) (inspectedContainer, *rpcError) {
	var zero inspectedContainer
	if !validID(id) {
		return zero, fail("invalid_argument", "invalid container ID")
	}
	out, e := run([]string{"inspect", id}, "", 2<<20)
	if e != nil {
		return zero, e
	}
	var items []inspectedContainer
	if json.Unmarshal([]byte(out.Stdout), &items) != nil || len(items) != 1 || items[0].ID == "" || !strings.HasPrefix(strings.ToLower(items[0].ID), strings.ToLower(id)) {
		return zero, fail("invalid_data", "invalid Docker container metadata")
	}
	return items[0], nil
}
func (p *plugin) managedContainer(id string) (inspectedContainer, *rpcError) {
	v, e := p.inspectContainer(id)
	if e != nil {
		return v, e
	}
	project := v.Config.Labels["com.docker.compose.project"]
	if project == "" || p.requireProject(project) != nil {
		return inspectedContainer{}, fail("forbidden", "container is not part of a managed Compose project")
	}
	dirs, e := p.projectDirectories(project)
	if e != nil {
		return inspectedContainer{}, e
	}
	workingDir := v.Config.Labels["com.docker.compose.project.working_dir"]
	owned := false
	for _, dir := range dirs {
		if filepath.Clean(workingDir) == dir {
			owned = true
			break
		}
	}
	if !owned {
		return inspectedContainer{}, fail("forbidden", "container belongs to a different Compose directory")
	}
	return v, nil
}
func (p *plugin) containerAction(id, action string) (any, *rpcError) {
	if action != "start" && action != "stop" && action != "delete" {
		return nil, fail("invalid_argument", "invalid container action")
	}
	if !p.runtime().Available {
		return nil, fail("unavailable", "Docker runtime is unavailable")
	}
	c, e := p.managedContainer(id)
	if e != nil {
		return nil, e
	}
	if action == "delete" && c.State.Running {
		return nil, fail("failed_precondition", "container must be stopped before deletion")
	}
	if (action == "start" && c.State.Running) || (action == "stop" && !c.State.Running) {
		return map[string]any{"ok": true}, nil
	}
	sub := action
	if action == "delete" {
		sub = "rm"
	}
	_, e = run([]string{"container", sub, id}, "", 2<<20)
	if e != nil {
		return nil, e
	}
	return map[string]any{"ok": true}, nil
}
func (p *plugin) containerLogs(id string) (any, *rpcError) {
	if !p.runtime().Available {
		return nil, fail("unavailable", "Docker runtime is unavailable")
	}
	if _, e := p.inspectContainer(id); e != nil {
		return nil, e
	}
	out, e := run([]string{"logs", "--tail", "800", id}, "", 8192, true)
	if e != nil {
		return nil, e
	}
	s := out.Stdout + out.Stderr
	if len(s) > 8192 {
		s = "(earlier log output truncated)\n" + s[len(s)-8192:]
	}
	return map[string]any{"content": s, "truncated": out.StdoutTruncated || out.StderrTruncated}, nil
}
func (p *plugin) terminalOpen(id string, rows, columns int) (any, *rpcError) {
	if !p.runtime().Available {
		return nil, fail("unavailable", "Docker runtime is unavailable")
	}
	c, e := p.managedContainer(id)
	if e != nil {
		return nil, e
	}
	if !c.State.Running {
		return nil, fail("failed_precondition", "container is not running")
	}
	if rows < 1 || rows > 300 || columns < 1 || columns > 500 {
		return nil, fail("invalid_argument", "invalid terminal size")
	}
	var out struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := callHost("process.session.create", map[string]any{"command": "docker", "args": []string{"exec", "-it", id, "/bin/sh"}, "size": map[string]int{"rows": rows, "columns": columns}}, &out); err != nil {
		return nil, hostError(err)
	}
	if out.ID == "" {
		return nil, fail("failed", "missing process session ID")
	}
	p.sessions[out.ID] = true
	return out, nil
}
func (p *plugin) terminalCommand(method string, raw json.RawMessage) (any, *rpcError) {
	var q struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Rows    int    `json:"rows"`
		Columns int    `json:"columns"`
		Force   bool   `json:"force"`
	}
	if e := decode(raw, &q); e != nil {
		return nil, e
	}
	if !p.sessions[q.ID] {
		return nil, fail("not_found", "unknown Docker terminal session")
	}
	hostMethod := "process.session." + strings.TrimPrefix(method, "docker.containers.terminal.")
	if strings.HasSuffix(method, ".close") {
		hostMethod = "process.session.terminate"
	}
	var out json.RawMessage
	if err := callHost(hostMethod, q, &out); err != nil {
		return nil, hostError(err)
	}
	if strings.HasSuffix(method, ".close") {
		delete(p.sessions, q.ID)
	}
	return out, nil
}
