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
	files         map[string]string
	dirs          map[string]bool
	outputs       map[string]runResult
	commandErrors map[string]error
	commands      [][]string
	sessions      []map[string]any
	deployment    string
}

func newFake() *fakeHost {
	return &fakeHost{files: map[string]string{}, dirs: map[string]bool{"projects": true}, outputs: map[string]runResult{}, commandErrors: map[string]error{}}
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
		return assign(result, map[string]string{"username": "alice", "deployment": f.deployment})
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
		key := q.Command + "\x00" + strings.Join(q.Args, "\x00")
		if err, ok := f.commandErrors[key]; ok {
			return err
		}
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
	if rt := p.runtime(); !rt.Available || rt.Identity != "alice" || rt.Deployment != "" {
		t.Fatalf("ready: %+v", rt)
	}
	f.outputs["compose\x00version"] = runResult{Success: false, Stderr: "missing"}
	if rt := p.runtime(); rt.State != "compose-missing" || rt.Diagnostic == "" {
		t.Fatalf("compose: %+v", rt)
	}
	f.set([]string{"compose", "version"}, "v2")
	for _, test := range []struct {
		name       string
		stderr     string
		code       string
		state      string
		diagnostic string
	}{
		{"socket permission", "permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock", "command_failed", "permission-denied", "Docker endpoint permission denied (EACCES)"},
		{"operation permission", "operation not permitted", "command_failed", "permission-denied", "Docker endpoint permission denied (EACCES)"},
		{"missing socket", "dial unix /var/run/docker.sock: connect: no such file or directory", "command_failed", "socket-missing", "Unix socket path not found (ENOENT)"},
		{"daemon down", "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", "command_failed", "daemon-unavailable", "Docker Engine connection refused, unavailable, or timed out"},
		{"invalid endpoint", "unable to resolve docker endpoint: invalid endpoint", "command_failed", "endpoint-invalid", "Docker endpoint configuration rejected by the CLI"},
		{"api mismatch", "client version 1.20 is too old. Maximum supported API version is 1.41", "command_failed", "api-incompatible", "Docker Engine API version mismatch"},
		{"network failure", "dial tcp 192.0.2.1:2376: i/o timeout", "command_failed", "connection-failed", "Docker Engine network connection failed"},
		{"unclassified", "daemon-specific diagnostic", "command_failed", "daemon-error", "Unclassified Docker CLI status error; run docker info as the RunPilot service account for local details"},
		{"timed out", "Docker command timed out", "timed_out", "daemon-unavailable", "Docker Engine connection refused, unavailable, or timed out"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.outputs["info"] = runResult{Success: false, Stderr: test.stderr}
			if test.code == "timed_out" {
				// run() derives this code from the host timeout result.
				f.outputs["info"] = runResult{TimedOut: true}
			}
			rt := p.runtime()
			if rt.State != test.state || rt.Diagnostic != test.diagnostic {
				t.Fatalf("runtime = %+v, want state %q and diagnostic %q", rt, test.state, test.diagnostic)
			}
			if strings.Contains(rt.Message, test.stderr) || strings.Contains(rt.Diagnostic, test.stderr) {
				t.Fatalf("raw Docker output escaped into diagnostics: %+v", rt)
			}
		})
	}
	f.deployment = "asustor-appcentral"
	f.outputs["info"] = runResult{Success: false, Stderr: "permission denied while opening unix:///var/run/docker.sock"}
	if rt := p.runtime(); rt.Deployment != "asustor-appcentral" || rt.State != "permission-denied" {
		t.Fatalf("ASUSTOR permission state: %+v", rt)
	}
}

func TestDockerRuntimeKeepsSocketPermissionFailureVisibleInSnapshot(t *testing.T) {
	p, f := fakePlugin(t)
	f.deployment = "asustor-appcentral"
	f.outputs["info"] = runResult{Success: false, Stderr: "permission denied while connecting to unix:///var/run/docker.sock"}
	value := mustSuccess(t, func() (any, *rpcError) { return p.snapshot() }).(map[string]any)
	runtime := value["runtime"].(dockerRuntime)
	if runtime.Available || runtime.State != "permission-denied" || runtime.Deployment != "asustor-appcentral" || runtime.Diagnostic == "" {
		t.Fatalf("runtime: %+v", runtime)
	}
	if len(value["projects"].([]project)) != 0 || len(value["volumes"].([]volume)) != 0 || len(value["networks"].([]network)) != 0 {
		t.Fatalf("failed Docker status was presented as healthy: %#v", value)
	}
}

func TestDockerCLIAndComposeFailuresRemainDistinct(t *testing.T) {
	p, f := fakePlugin(t)
	f.commandErrors["docker\x00compose\x00version"] = &pluginapi.Error{Code: "not_found", Message: "executable not found"}
	if rt := p.runtime(); rt.State != "cli-missing" {
		t.Fatalf("missing CLI: %+v", rt)
	}

	delete(f.commandErrors, "docker\x00compose\x00version")
	f.outputs["compose\x00version"] = runResult{Success: false, Stderr: "docker: 'compose' is not a docker command"}
	if rt := p.runtime(); rt.State != "compose-missing" {
		t.Fatalf("missing Compose: %+v", rt)
	}

	f.commandErrors["docker\x00compose\x00version"] = &pluginapi.Error{Code: "failed", Message: "fork/exec /usr/bin/docker: permission denied"}
	if rt := p.runtime(); rt.State != "cli-permission-denied" {
		t.Fatalf("inaccessible CLI: %+v", rt)
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
