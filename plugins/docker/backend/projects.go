package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type dockerRuntime struct {
	Supported bool   `json:"supported"`
	Available bool   `json:"available"`
	State     string `json:"state"`
	Message   string `json:"message"`
	Identity  string `json:"identity,omitempty"`
}
type container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Service string `json:"service,omitempty"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Health  string `json:"health,omitempty"`
	Tone    string `json:"tone"`
}
type project struct {
	Name              string      `json:"name"`
	Managed           bool        `json:"managed"`
	ReadOnly          bool        `json:"readOnly"`
	ConfigPath        string      `json:"configPath,omitempty"`
	ComposeFile       string      `json:"composeFile,omitempty"`
	ComposeFileExists bool        `json:"composeFileExists"`
	State             string      `json:"state"`
	Containers        []container `json:"containers"`
}

func (p *plugin) init() { _ = workspace("mkdir", "projects", nil, nil) }
func (p *plugin) runtime() dockerRuntime {
	rt := dockerRuntime{Supported: true, State: "ready", Message: "Docker Compose is ready."}
	var identity struct {
		Username string `json:"username"`
		UID      string `json:"uid"`
	}
	_ = callHost("system.identity", map[string]any{}, &identity)
	rt.Identity = identity.Username
	if rt.Identity == "" {
		rt.Identity = identity.UID
	}
	_, e := run([]string{"compose", "version"}, "", 8192)
	if e != nil {
		rt.State = "compose-missing"
		rt.Message = "Docker Compose v2 is not available."
		if e.Code == "not_found" {
			rt.State = "cli-missing"
			rt.Message = "Docker CLI is not installed."
		}
		return rt
	}
	_, e = run([]string{"info"}, "", 8192)
	if e != nil {
		rt.State = "daemon-unavailable"
		rt.Message = "Docker daemon is unavailable."
		if permissionText(e.Message) {
			rt.State = "permission-denied"
			rt.Message = "RunPilot cannot access the Docker daemon."
		}
		return rt
	}
	rt.Available = true
	return rt
}
func (p *plugin) managedProjects() ([]project, *rpcError) {
	entries, e := listDir("projects")
	if e != nil {
		return nil, e
	}
	out := []project{}
	for _, v := range entries {
		if !v.Directory || v.Symlink || !validName(v.Name) {
			continue
		}
		filename, exists := composeFile(v.Name)
		out = append(out, project{Name: v.Name, Managed: true, ComposeFile: filename, ConfigPath: filename, ComposeFileExists: exists, State: "down", Containers: []container{}})
	}
	return out, nil
}
func composeFile(name string) (string, bool) {
	if !validName(name) {
		return "", false
	}
	entries, e := listDir("projects/" + name)
	if e != nil {
		return "", false
	}
	for _, candidate := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		for _, entry := range entries {
			if entry.Name == candidate && !entry.Directory && !entry.Symlink {
				return candidate, true
			}
		}
	}
	return "compose.yaml", false
}
func (p *plugin) requireProject(name string) *rpcError {
	if !validName(name) {
		return fail("invalid_argument", "invalid Compose project name")
	}
	entries, e := listDir("projects")
	if e != nil {
		return e
	}
	for _, v := range entries {
		if v.Name == name && v.Directory && !v.Symlink {
			return nil
		}
	}
	return fail("not_found", "unknown managed Compose project")
}
func (p *plugin) createProject(name string) (any, *rpcError) {
	if !validName(name) {
		return nil, fail("invalid_argument", "invalid Compose project name")
	}
	entries, e := listDir("projects")
	if e != nil {
		return nil, e
	}
	for _, v := range entries {
		if v.Name == name {
			return nil, fail("already_exists", "Compose project already exists")
		}
	}
	if p.runtime().Available {
		paths, e := p.activeProjectPaths(name)
		if e != nil {
			return nil, e
		}
		if len(paths) > 0 {
			return nil, fail("already_exists", "an external Docker Compose project already uses this name")
		}
	}
	dir := "projects/" + name
	if e = workspace("mkdir", dir, nil, nil); e != nil {
		return nil, e
	}
	if e = writeFile(dir+"/compose.yaml", "services: {}\n"); e != nil {
		_ = workspace("remove", dir, map[string]any{"recursive": true}, nil)
		return nil, e
	}
	return project{Name: name, Managed: true, ComposeFile: "compose.yaml", ComposeFileExists: true, State: "down", Containers: []container{}}, nil
}
func (p *plugin) snapshot() (any, *rpcError) {
	rt := p.runtime()
	projects, e := p.managedProjects()
	if e != nil {
		return nil, e
	}
	volumes := []volume{}
	networks := []network{}
	if !rt.Available {
		return map[string]any{"runtime": rt, "projects": projects, "volumes": volumes, "networks": networks}, nil
	}
	byName := map[string]*project{}
	for i := range projects {
		byName[projects[i].Name] = &projects[i]
	}
	cs, e := p.containers()
	if e != nil {
		return nil, e
	}
	for name, items := range cs {
		if item := byName[name]; item != nil {
			item.Containers = items
			item.State = projectState(items)
		}
	}
	out, e := run([]string{"compose", "ls", "--all", "--format", "json"}, "", 2<<20)
	if e != nil {
		return nil, e
	}
	var external []struct {
		Name        string          `json:"Name"`
		ConfigFiles json.RawMessage `json:"ConfigFiles"`
	}
	if strings.TrimSpace(out.Stdout) != "" && json.Unmarshal([]byte(out.Stdout), &external) != nil {
		return nil, fail("invalid_data", "invalid Docker Compose project JSON")
	}
	for _, v := range external {
		if v.Name == "" || byName[v.Name] != nil {
			continue
		}
		item := project{Name: v.Name, ReadOnly: true, State: projectState(cs[v.Name]), Containers: cs[v.Name]}
		_ = json.Unmarshal(v.ConfigFiles, &item.ConfigPath)
		if item.ConfigPath == "" {
			var paths []string
			if json.Unmarshal(v.ConfigFiles, &paths) == nil && len(paths) > 0 {
				item.ConfigPath = paths[0]
			}
		}
		projects = append(projects, item)
		byName[v.Name] = &projects[len(projects)-1]
	}
	for name, items := range cs {
		if byName[name] == nil {
			projects = append(projects, project{Name: name, ReadOnly: true, State: projectState(items), Containers: items})
		}
	}
	volumes, e = p.listVolumes()
	if e != nil {
		return nil, e
	}
	networks, e = p.listNetworks()
	if e != nil {
		return nil, e
	}
	return map[string]any{"runtime": rt, "projects": projects, "volumes": volumes, "networks": networks}, nil
}
func (p *plugin) projectAction(name, action string) (any, *rpcError) {
	if e := p.requireProject(name); e != nil {
		return nil, e
	}
	if action != "up" && action != "start" && action != "stop" && action != "down" {
		return nil, fail("invalid_argument", "invalid Compose action")
	}
	file, exists := composeFile(name)
	if !exists {
		return nil, fail("failed_precondition", "Compose file is missing")
	}
	if !p.runtime().Available {
		return nil, fail("unavailable", "Docker runtime is unavailable")
	}
	if e := p.checkProjectOwnership(name); e != nil {
		return nil, e
	}
	if p.busy[name] {
		return nil, fail("busy", "Compose project operation is already in progress")
	}
	p.busy[name] = true
	defer delete(p.busy, name)
	args := []string{"compose", "--project-name", name, "-f", file, action}
	if action == "up" {
		args = append(args, "-d")
	}
	_, e := run(args, "projects/"+name, 2<<20)
	if e != nil {
		return nil, e
	}
	return map[string]any{"ok": true}, nil
}

// A Compose project name is a Docker-wide identity. Check Docker's config paths
// before a managed action can affect containers started from another directory.
func (p *plugin) activeProjectPaths(name string) ([]string, *rpcError) {
	out, e := run([]string{"compose", "ls", "--all", "--format", "json"}, "", 2<<20)
	if e != nil {
		return nil, e
	}
	var rows []struct {
		Name        string          `json:"Name"`
		ConfigFiles json.RawMessage `json:"ConfigFiles"`
	}
	if strings.TrimSpace(out.Stdout) != "" && json.Unmarshal([]byte(out.Stdout), &rows) != nil {
		return nil, fail("invalid_data", "invalid Docker Compose project JSON")
	}
	for _, row := range rows {
		if row.Name != name {
			continue
		}
		var paths []string
		var joined string
		if json.Unmarshal(row.ConfigFiles, &joined) == nil {
			paths = strings.Split(joined, ",")
		} else if json.Unmarshal(row.ConfigFiles, &paths) != nil {
			return nil, fail("invalid_data", "invalid Docker Compose config paths")
		}
		if len(paths) == 0 {
			return []string{""}, nil
		}
		return paths, nil
	}
	return nil, nil
}
func (p *plugin) projectDirectories(name string) ([]string, *rpcError) {
	var out runResult
	err := callHost("process.run", map[string]any{"command": "pwd", "args": []string{"-P"}, "workspaceDirectory": "projects/" + name, "timeoutSeconds": 5, "maxOutputBytes": 4096}, &out)
	if err != nil || !out.Success || out.TimedOut {
		return nil, fail("failed", "cannot resolve managed project directory")
	}
	dir := strings.TrimSpace(out.Stdout)
	if !filepath.IsAbs(dir) {
		return nil, fail("invalid_data", "invalid managed project directory")
	}
	dirs := []string{filepath.Clean(dir)}
	if origin, e := readFile("projects/" + name + "/.runpilot-legacy-origin"); e == nil {
		origin = strings.TrimSpace(origin)
		if filepath.IsAbs(origin) {
			dirs = append(dirs, filepath.Clean(origin))
		}
	} else if e.Code != "not_found" {
		return nil, e
	}
	return dirs, nil
}
func (p *plugin) checkProjectOwnership(name string) *rpcError {
	paths, e := p.activeProjectPaths(name)
	if e != nil || len(paths) == 0 {
		return e
	}
	dirs, e := p.projectDirectories(name)
	if e != nil {
		return e
	}
	for _, config := range paths {
		config = filepath.Clean(strings.TrimSpace(config))
		for _, dir := range dirs {
			if filepath.Dir(config) == dir {
				return nil
			}
		}
	}
	return fail("forbidden", "external Docker Compose project already uses this name")
}
func (p *plugin) deleteProject(name string) (any, *rpcError) {
	if e := p.requireProject(name); e != nil {
		return nil, e
	}
	if p.busy[name] {
		return nil, fail("busy", "Compose project operation is already in progress")
	}
	if !p.runtime().Available {
		return nil, fail("unavailable", "Docker runtime is unavailable")
	}
	cs, e := p.containers()
	if e != nil {
		return nil, e
	}
	if len(cs[name]) > 0 {
		return nil, fail("failed_precondition", "project must be down before deletion")
	}
	if e = workspace("remove", "projects/"+name, map[string]any{"recursive": true}, nil); e != nil {
		return nil, e
	}
	return map[string]any{"ok": true}, nil
}
func kindFile(name, kind string) string {
	if kind == "compose" {
		file, _ := composeFile(name)
		return file
	}
	if kind == "env" {
		return ".env"
	}
	return ""
}
func (p *plugin) getFile(name, kind string) (any, *rpcError) {
	if e := p.requireProject(name); e != nil {
		return nil, e
	}
	filename := kindFile(name, kind)
	if filename == "" {
		return nil, fail("invalid_argument", "invalid project file")
	}
	content, e := readFile("projects/" + name + "/" + filename)
	if e != nil {
		if e.Code == "not_found" {
			return map[string]any{"content": "", "file": filename}, nil
		}
		return nil, e
	}
	return map[string]any{"content": content, "file": filename}, nil
}
func (p *plugin) setFile(name, kind, content string) (any, *rpcError) {
	if e := p.requireProject(name); e != nil {
		return nil, e
	}
	filename := kindFile(name, kind)
	if filename == "" {
		return nil, fail("invalid_argument", "invalid project file")
	}
	if len(content) > 1<<20 {
		return nil, fail("resource_limit", "Compose file exceeds 1 MiB")
	}
	if e := writeFile("projects/"+name+"/"+filename, content); e != nil {
		return nil, e
	}
	return map[string]any{"ok": true}, nil
}
func labels(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}
func (p *plugin) containers() (map[string][]container, *rpcError) {
	out, e := run([]string{"ps", "-a", "--format", "{{json .}}"}, "", 2<<20)
	if e != nil {
		return nil, e
	}
	result := map[string][]container{}
	for _, line := range strings.Split(out.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var v struct {
			ID     string `json:"ID"`
			Names  string `json:"Names"`
			Image  string `json:"Image"`
			State  string `json:"State"`
			Status string `json:"Status"`
			Labels string `json:"Labels"`
		}
		if json.Unmarshal([]byte(line), &v) != nil {
			return nil, fail("invalid_data", "invalid Docker container JSON")
		}
		label := labels(v.Labels)
		project := label["com.docker.compose.project"]
		if project == "" {
			continue
		}
		c := container{ID: v.ID, Name: v.Names, Service: label["com.docker.compose.service"], Image: v.Image, State: strings.ToLower(v.State), Status: v.Status}
		low := strings.ToLower(c.Status)
		if strings.Contains(low, "(unhealthy)") {
			c.Health = "unhealthy"
		} else if strings.Contains(low, "(healthy)") {
			c.Health = "healthy"
		}
		c.Tone = containerTone(c)
		result[project] = append(result[project], c)
	}
	return result, nil
}
func containerTone(c container) string {
	s := strings.ToLower(c.State + " " + c.Health)
	if strings.Contains(s, "unhealthy") || strings.Contains(s, "dead") || strings.Contains(s, "exited") {
		return "red"
	}
	if strings.Contains(s, "running") {
		return "green"
	}
	if strings.Contains(s, "created") || strings.Contains(s, "restarting") || strings.Contains(s, "paused") {
		return "yellow"
	}
	return "gray"
}
func projectState(cs []container) string {
	if len(cs) == 0 {
		return "down"
	}
	running := 0
	degraded := false
	transition := false
	for _, c := range cs {
		if c.Health == "unhealthy" || c.State == "dead" {
			degraded = true
		}
		if c.State == "running" && c.Tone == "green" {
			running++
		}
		if c.Tone == "yellow" {
			transition = true
		}
	}
	if degraded {
		return "degraded"
	}
	if running == len(cs) {
		return "running"
	}
	if running == 0 && !transition {
		return "stopped"
	}
	return "partial"
}
func (p *plugin) debug() string { return fmt.Sprint(len(p.busy)) }
