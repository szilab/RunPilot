package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

type fakeHost struct {
	files    map[string]string
	dirs     map[string]bool
	outputs  map[string]runResult
	commands [][]string
	sessions []map[string]any
}

func newFake() *fakeHost {
	return &fakeHost{files: map[string]string{}, dirs: map[string]bool{"projects": true}, outputs: map[string]runResult{}}
}
func (f *fakeHost) set(args []string, out string) {
	f.outputs[strings.Join(args, "\x00")] = runResult{Success: true, Stdout: out}
}
func assign(dst any, value any) error {
	if dst == nil {
		return nil
	}
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	return json.Unmarshal(raw, dst)
}
func (f *fakeHost) call(method string, params any, result any) error {
	raw, _ := json.Marshal(params)
	switch method {
	case "system.identity":
		return assign(result, map[string]string{"username": "alice"})
	case "process.run":
		var q struct {
			Command            string   `json:"command"`
			Args               []string `json:"args"`
			WorkspaceDirectory string   `json:"workspaceDirectory"`
			MaxOutputBytes     int      `json:"maxOutputBytes"`
			TailOutput         bool     `json:"tailOutput"`
		}
		_ = json.Unmarshal(raw, &q)
		if q.Command == "pwd" {
			return assign(result, runResult{Success: true, Stdout: "/runpilot/" + q.WorkspaceDirectory + "\n"})
		}
		if q.Command != "docker" {
			return errors.New("unexpected executable")
		}
		f.commands = append(f.commands, q.Args)
		out, ok := f.outputs[strings.Join(q.Args, "\x00")]
		if !ok {
			return errors.New("unexpected Docker command: " + strings.Join(q.Args, " "))
		}
		return assign(result, out)
	case "workspace.list":
		var q struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(raw, &q)
		if !f.dirs[q.Path] {
			return &pluginapi.Error{Code: "not_found", Message: "workspace path does not exist"}
		}
		prefix := q.Path + "/"
		entries := []map[string]any{}
		seen := map[string]bool{}
		for p := range f.dirs {
			if strings.HasPrefix(p, prefix) {
				rest := strings.TrimPrefix(p, prefix)
				if !strings.Contains(rest, "/") && !seen[rest] {
					entries = append(entries, map[string]any{"name": rest, "directory": true})
					seen[rest] = true
				}
			}
		}
		for p := range f.files {
			if strings.HasPrefix(p, prefix) {
				rest := strings.TrimPrefix(p, prefix)
				if !strings.Contains(rest, "/") && !seen[rest] {
					entries = append(entries, map[string]any{"name": rest, "directory": false})
					seen[rest] = true
				}
			}
		}
		return assign(result, map[string]any{"entries": entries})
	case "workspace.mkdir":
		var q struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(raw, &q)
		f.dirs[q.Path] = true
		return nil
	case "workspace.write":
		var q struct {
			Path string `json:"path"`
			Data string `json:"data"`
		}
		_ = json.Unmarshal(raw, &q)
		b, e := base64.StdEncoding.DecodeString(q.Data)
		if e != nil {
			return e
		}
		f.files[q.Path] = string(b)
		return nil
	case "workspace.read":
		var q struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(raw, &q)
		v, ok := f.files[q.Path]
		if !ok {
			return &pluginapi.Error{Code: "not_found", Message: "workspace path does not exist"}
		}
		return assign(result, map[string]any{"data": base64.StdEncoding.EncodeToString([]byte(v))})
	case "workspace.remove":
		var q struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(raw, &q)
		delete(f.dirs, q.Path)
		for p := range f.files {
			if p == q.Path || strings.HasPrefix(p, q.Path+"/") {
				delete(f.files, p)
			}
		}
		return nil
	case "process.session.create":
		var q map[string]any
		_ = json.Unmarshal(raw, &q)
		f.sessions = append(f.sessions, q)
		return assign(result, map[string]string{"id": "session-1", "state": "running"})
	case "process.session.write", "process.session.resize", "process.session.status", "process.session.terminate":
		return assign(result, map[string]any{"id": "session-1", "state": "running"})
	case "events.publish":
		return nil
	}
	return errors.New("unknown capability: " + method)
}
func fakePlugin(t *testing.T) (*plugin, *fakeHost) {
	t.Helper()
	f := newFake()
	old := callHost
	callHost = f.call
	t.Cleanup(func() { callHost = old })
	p := newPlugin()
	p.init()
	f.set([]string{"compose", "version"}, "Docker Compose version v2")
	f.set([]string{"info"}, "ready")
	f.set([]string{"compose", "ls", "--all", "--format", "json"}, "[]")
	return p, f
}
func mustSuccess(t *testing.T, operation func() (any, *rpcError)) any {
	v, e := operation()
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestDockerRuntimeStates(t *testing.T) {
	p, f := fakePlugin(t)
	if rt := p.runtime(); !rt.Available || rt.Identity != "alice" {
		t.Fatalf("ready: %+v", rt)
	}
	f.outputs["compose\x00version"] = runResult{Success: false, Stderr: "missing"}
	if rt := p.runtime(); rt.State != "compose-missing" {
		t.Fatalf("compose: %+v", rt)
	}
	f.set([]string{"compose", "version"}, "v2")
	f.outputs["info"] = runResult{Success: false, Stderr: "permission denied"}
	if rt := p.runtime(); rt.State != "permission-denied" {
		t.Fatalf("permission: %+v", rt)
	}
	f.outputs["info"] = runResult{Success: false, Stderr: "unavailable"}
	if rt := p.runtime(); rt.State != "daemon-unavailable" {
		t.Fatalf("daemon: %+v", rt)
	}
}
func TestDockerProjectsAndFiles(t *testing.T) {
	p, f := fakePlugin(t)
	mustSuccess(t, func() (any, *rpcError) { return p.createProject("jellyfin") })
	if f.files["projects/jellyfin/compose.yaml"] != "services: {}\n" {
		t.Fatal("missing compose file")
	}
	if _, e := p.createProject("jellyfin"); e == nil {
		t.Fatal("duplicate accepted")
	}
	if _, e := p.createProject("../escape"); e == nil {
		t.Fatal("invalid name accepted")
	}
	mustSuccess(t, func() (any, *rpcError) { return p.setFile("jellyfin", "env", "TOKEN=abc") })
	delete(f.files, "projects/jellyfin/compose.yaml")
	f.files["projects/jellyfin/compose.yml"] = "services: {legacy: {}}"
	compose := mustSuccess(t, func() (any, *rpcError) { return p.getFile("jellyfin", "compose") }).(map[string]any)
	if compose["file"] != "compose.yml" || compose["content"] != "services: {legacy: {}}" {
		t.Fatalf("legacy Compose file: %v", compose)
	}
	mustSuccess(t, func() (any, *rpcError) { return p.setFile("jellyfin", "compose", "services: {new: {}}") })
	if f.files["projects/jellyfin/compose.yml"] != "services: {new: {}}" {
		t.Fatal("legacy Compose file not updated")
	}
	v := mustSuccess(t, func() (any, *rpcError) { return p.getFile("jellyfin", "env") }).(map[string]any)
	if v["content"] != "TOKEN=abc" {
		t.Fatal(v)
	}
	if _, e := p.setFile("jellyfin", "env", strings.Repeat("x", 1<<20+1)); e == nil {
		t.Fatal("oversized file accepted")
	}
	f.set([]string{"ps", "-a", "--format", "{{json .}}"}, "")
	f.set([]string{"compose", "ls", "--all", "--format", "json"}, `[{"Name":"external","ConfigFiles":"/srv/ext/compose.yaml"},{"Name":"jellyfin","ConfigFiles":"/runpilot/projects/jellyfin/compose.yml"}]`)
	f.set([]string{"volume", "ls", "--format", "json"}, "")
	f.set([]string{"network", "ls", "--format", "json"}, "")
	f.set([]string{"ps", "-aq"}, "")
	snapshot := mustSuccess(t, func() (any, *rpcError) { return p.snapshot() }).(map[string]any)
	projects := snapshot["projects"].([]project)
	if len(projects) != 2 || !projects[0].Managed || !projects[1].ReadOnly {
		t.Fatalf("projects: %+v", projects)
	}
	f.set([]string{"compose", "--project-name", "jellyfin", "-f", "compose.yml", "up", "-d"}, "")
	mustSuccess(t, func() (any, *rpcError) { return p.projectAction("jellyfin", "up") })
	if _, e := p.projectAction("external", "up"); e == nil {
		t.Fatal("external project mutated")
	}
	mustSuccess(t, func() (any, *rpcError) { return p.deleteProject("jellyfin") })
	if e := p.requireProject("jellyfin"); e == nil {
		t.Fatal("project still exists")
	}
}
func TestDockerContainerOwnershipAndTerminal(t *testing.T) {
	p, f := fakePlugin(t)
	mustSuccess(t, func() (any, *rpcError) { return p.createProject("jellyfin") })
	id := "abcdef012345"
	full := id + strings.Repeat("0", 52)
	inspect := `[{"Id":"` + full + `","Name":"/jellyfin","State":{"Running":true},"Config":{"Labels":{"com.docker.compose.project":"jellyfin","com.docker.compose.project.working_dir":"/runpilot/projects/jellyfin","com.docker.compose.service":"web"}},"Mounts":[{"Type":"volume","Name":"data"}],"NetworkSettings":{"Networks":{"jellyfin_default":{}}}}]`
	f.set([]string{"inspect", id}, inspect)
	f.set([]string{"container", "stop", id}, "")
	mustSuccess(t, func() (any, *rpcError) { return p.containerAction(id, "stop") })
	if _, e := p.containerAction(id, "delete"); e == nil {
		t.Fatal("running container deleted")
	}
	mustSuccess(t, func() (any, *rpcError) { return p.terminalOpen(id, 24, 80) })
	args := f.sessions[0]["args"].([]any)
	if !reflect.DeepEqual(args, []any{"exec", "-it", id, "/bin/sh"}) {
		t.Fatalf("terminal args: %v", args)
	}
	if _, e := p.terminalOpen("bad", 24, 80); e == nil {
		t.Fatal("bad ID accepted")
	}
	f.set([]string{"inspect", id}, strings.Replace(inspect, `"com.docker.compose.project":"jellyfin"`, `"com.docker.compose.project":"external"`, 1))
	if _, e := p.containerAction(id, "stop"); e == nil {
		t.Fatal("external container mutated")
	}
	f.set([]string{"logs", "--tail", "800", id}, "external log")
	logs := mustSuccess(t, func() (any, *rpcError) { return p.containerLogs(id) }).(map[string]any)
	if logs["content"] != "external log" {
		t.Fatalf("external logs: %v", logs)
	}
	f.set([]string{"inspect", id}, strings.Replace(inspect, `"com.docker.compose.project.working_dir":"/runpilot/projects/jellyfin"`, `"com.docker.compose.project.working_dir":"/srv/external"`, 1))
	if _, e := p.containerAction(id, "stop"); e == nil || e.Code != "forbidden" {
		t.Fatal("container from colliding external directory mutated")
	}
}
func TestDockerProjectNameCollision(t *testing.T) {
	p, f := fakePlugin(t)
	f.set([]string{"compose", "ls", "--all", "--format", "json"}, `[{"Name":"external","ConfigFiles":"/srv/external/compose.yaml"}]`)
	if _, e := p.createProject("external"); e == nil || e.Code != "already_exists" {
		t.Fatalf("external project name accepted: %v", e)
	}
	mustSuccess(t, func() (any, *rpcError) { return p.createProject("owned") })
	f.set([]string{"compose", "ls", "--all", "--format", "json"}, `[{"Name":"owned","ConfigFiles":"/srv/external/compose.yaml"}]`)
	if _, e := p.projectAction("owned", "up"); e == nil || e.Code != "forbidden" {
		t.Fatalf("external project name collision was actionable: %v", e)
	}
	f.files["projects/owned/.runpilot-legacy-origin"] = "/srv/external"
	f.files["projects/owned/.env"] = "IMAGE=example\n"
	f.set([]string{"compose", "--project-name", "owned", "--project-directory", "/srv/external", "--env-file", "/runpilot/projects/owned/.env", "-f", "compose.yaml", "up", "-d"}, "")
	mustSuccess(t, func() (any, *rpcError) { return p.projectAction("owned", "up") })
}
func TestDockerResourceGuardsAndMalformedJSON(t *testing.T) {
	p, f := fakePlugin(t)
	f.set([]string{"volume", "ls", "--format", "json"}, `{"bad":true}`)
	if _, e := p.listVolumes(); e == nil {
		t.Fatal("malformed volume JSON accepted")
	}
	f.set([]string{"volume", "ls", "--format", "json"}, `[{"Name":"data","Driver":"local","Scope":"local","Labels":"com.runpilot.managed=true"}]`)
	f.set([]string{"network", "ls", "--format", "json"}, `[{"Name":"bridge"},{"Name":"appnet","Labels":"com.docker.compose.project=app"}]`)
	f.set([]string{"ps", "-aq"}, "")
	if _, e := p.resourceAction("docker.networks.delete", "bridge"); e == nil {
		t.Fatal("default network deleted")
	}
	if _, e := p.resourceAction("docker.networks.delete", "appnet"); e == nil {
		t.Fatal("Compose network deleted")
	}
	if _, e := p.resourceAction("docker.networks.create", "appnet"); e == nil {
		t.Fatal("duplicate network created")
	}
	f.set([]string{"volume", "create", "--driver", "local", "--label", "com.runpilot.managed=true", "newvol"}, "newvol")
	mustSuccess(t, func() (any, *rpcError) { return p.resourceAction("docker.volumes.create", "newvol") })
	f.set([]string{"ps", "-aq"}, "abcdef012345")
	f.set([]string{"inspect", "abcdef012345"}, `[{"Id":"abcdef0123450000000000000000000000000000000000000000000000000000","Name":"/container","State":{"Running":false},"Mounts":[{"Type":"volume","Name":"data"}],"NetworkSettings":{"Networks":{"appnet":{}}}}]`)
	if _, e := p.resourceAction("docker.volumes.delete", "data"); e == nil {
		t.Fatal("volume in use deleted")
	}
	if _, e := p.resourceAction("docker.networks.delete", "appnet"); e == nil {
		t.Fatal("network in use deleted")
	}
}
