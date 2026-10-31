package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

type fakeXpraHost struct {
	storage      map[string]json.RawMessage
	started      []map[string]any
	terminated   []string
	removed      []string
	mkdirs       []string
	socketReady  bool
	streamOpened []map[string]any
	closed       []string
	published    []map[string]any
	runResult    map[string]any
	runError     error
}

func (h *fakeXpraHost) call(method string, params any, result any) error {
	arguments, _ := params.(map[string]any)
	switch method {
	case "storage.get":
		if value := h.storage[arguments["key"].(string)]; value != nil {
			return assignJSON(result, value)
		}
	case "storage.set":
		raw, err := json.Marshal(arguments["value"])
		h.storage[arguments["key"].(string)] = raw
		return err
	case "process.run":
		if h.runError != nil {
			return h.runError
		}
		raw, _ := json.Marshal(h.runResult)
		return assignJSON(result, raw)
	case "process.start":
		h.started = append(h.started, arguments)
		return assignJSON(result, []byte(`{"id":"process-1","pid":42,"running":true}`))
	case "process.terminate":
		h.terminated = append(h.terminated, params.(map[string]string)["id"])
	case "workspace.mkdir":
		h.mkdirs = append(h.mkdirs, arguments["path"].(string))
	case "workspace.remove":
		h.removed = append(h.removed, arguments["path"].(string))
	case "workspace.list":
		entries := `{"entries":[]}`
		if h.socketReady {
			entries = `{"entries":[{"name":"host-xpra.sock","directory":false,"symlink":false}]}`
		}
		return assignJSON(result, []byte(entries))
	case "network.stream.open":
		h.streamOpened = append(h.streamOpened, arguments)
		return assignJSON(result, []byte(`{"id":"stream-1"}`))
	case "network.stream.close":
		h.closed = append(h.closed, params.(map[string]string)["id"])
	case "events.publish":
		h.published = append(h.published, arguments)
	}
	return nil
}

func newXpraTestPlugin(t *testing.T) (*plugin, *fakeXpraHost) {
	t.Helper()
	host := &fakeXpraHost{storage: map[string]json.RawMessage{}}
	original := callHost
	callHost = host.call
	t.Cleanup(func() { callHost = original })
	return newPlugin(), host
}

func mustCall(t *testing.T, p *plugin, method string, params any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(params)
	result, failure := p.handle(method, raw)
	if failure != nil {
		t.Fatalf("%s failed: %+v", method, failure)
	}
	encoded, _ := json.Marshal(result)
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	return decoded
}

func TestXpraTargetValidationAndDefaults(t *testing.T) {
	p, _ := newXpraTestPlugin(t)
	saved := mustCall(t, p, "xpra.targets.save", map[string]any{"target": map[string]any{"name": " Terminal ", "command": "xterm"}})
	target := saved["target"].(map[string]any)
	if target["id"] != "xpra-1" || target["mode"] != "application" || target["clipboard"] != true || target["name"] != "Terminal" {
		t.Fatalf("saved target = %v", target)
	}
	for name, candidate := range map[string]map[string]any{
		"empty command":     {"name": "x", "command": " "},
		"multiline command": {"name": "x", "command": "xterm\nrm -rf /"},
		"bad mode":          {"name": "x", "command": "xterm", "mode": "shadow"},
		"relative cwd":      {"name": "x", "command": "xterm", "workingDirectory": "home"},
		"bad dpi":           {"name": "x", "command": "xterm", "dpi": 7},
	} {
		raw, _ := json.Marshal(map[string]any{"target": candidate})
		if _, failure := p.handle("xpra.targets.save", raw); failure == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	listed := mustCall(t, p, "xpra.targets.list", nil)
	if len(listed["targets"].([]any)) != 1 {
		t.Fatalf("targets = %v", listed)
	}
}

func TestXpraArgsKeepServerPrivateAndModeSpecific(t *testing.T) {
	clipboard := false
	app := strings.Join(xpraArgs(xpraTarget{Mode: xpraModeApp, Command: "firefox --new-window", WorkingDirectory: "/home/user/it's"}), " ")
	for _, want := range []string{"-u DISPLAY", "-u DBUS_SESSION_BUS_ADDRESS", "xpra start ", "--bind=xpra.sock", "--socket-dir=.", "--html=off", "--daemon=no", "--start=sh -c ", "--clipboard=yes"} {
		if !strings.Contains(app, want) {
			t.Errorf("application args lack %q: %s", want, app)
		}
	}
	if strings.Contains(app, "bind-tcp") || strings.Contains(app, "bind-ws") || strings.Contains(app, "exit-with-children") {
		t.Errorf("application args expose TCP or exit with children: %s", app)
	}
	desktop := strings.Join(xpraArgs(xpraTarget{Mode: xpraModeDesktop, Command: "startxfce4", Clipboard: &clipboard, DPI: 120}), " ")
	for _, want := range []string{"xpra start-desktop ", "--start-child=sh -c ", "startxfce4", "--exit-with-children=yes", "--clipboard=no", "--dpi=120"} {
		if !strings.Contains(desktop, want) {
			t.Errorf("desktop args lack %q: %s", want, desktop)
		}
	}
}

func TestXpraSessionLifecycle(t *testing.T) {
	p, host := newXpraTestPlugin(t)
	mustCall(t, p, "xpra.targets.save", map[string]any{"target": map[string]any{"name": "Terminal", "command": "xterm"}})
	opened := mustCall(t, p, "xpra.session.open", map[string]any{"targetId": "xpra-1"})
	session := opened["session"].(map[string]any)
	id := session["id"].(string)
	if session["state"] != "starting" || len(host.started) != 1 {
		t.Fatalf("open = %v, started %v", opened, host.started)
	}
	if host.started[0]["command"] != "env" || host.started[0]["workspaceDirectory"] != "xpra/"+id {
		t.Fatalf("process.start = %v", host.started[0])
	}
	if host.mkdirs[0] != "xpra/"+id {
		t.Fatalf("mkdir = %v", host.mkdirs)
	}

	// The server socket does not exist yet: the browser retries.
	if attached := mustCall(t, p, "xpra.session.attach", map[string]any{"id": id}); attached["ready"] != false {
		t.Fatalf("attach before socket = %v", attached)
	}
	host.socketReady = true
	attached := mustCall(t, p, "xpra.session.attach", map[string]any{"id": id})
	if attached["ready"] != true || attached["streamId"] != "stream-1" {
		t.Fatalf("attach = %v", attached)
	}
	if host.streamOpened[0]["workspaceSocket"] != "xpra/"+id+"/host-xpra.sock" || host.streamOpened[0]["host"] != nil {
		t.Fatalf("stream open = %v", host.streamOpened[0])
	}
	raw, _ := json.Marshal(map[string]any{"id": id})
	if _, failure := p.handle("xpra.session.attach", raw); failure == nil || failure.Code != "failed_precondition" {
		t.Fatalf("second attach = %+v", failure)
	}

	// The browser cannot delete a target while its session runs.
	raw, _ = json.Marshal(map[string]any{"id": "xpra-1"})
	if _, failure := p.handle("xpra.targets.delete", raw); failure == nil {
		t.Fatal("deleted a target with an active session")
	}

	p.event("process.stderr", json.RawMessage(`{"id":"process-1","text":"starting\nError: something broke\n"}`))
	diagnostics := mustCall(t, p, "xpra.session.diagnostics", map[string]any{"id": id})
	if !strings.Contains(diagnostics["log"].(string), "something broke") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}

	mustCall(t, p, "xpra.session.close", map[string]any{"id": id})
	if len(host.closed) != 1 || host.closed[0] != "stream-1" || len(host.terminated) != 1 || host.terminated[0] != "process-1" {
		t.Fatalf("close: streams %v processes %v", host.closed, host.terminated)
	}
	p.event("process.exit", json.RawMessage(`{"id":"process-1","exitCode":0}`))
	if _, ok := p.xpraSessions[id]; ok {
		t.Fatal("stopped session was kept")
	}
	if host.removed[len(host.removed)-1] != "xpra/"+id {
		t.Fatalf("session directory was not removed: %v", host.removed)
	}
	if len(host.published) != 0 {
		t.Fatalf("a requested stop was reported as a failure: %v", host.published)
	}
}

func TestXpraUnexpectedExitIsReported(t *testing.T) {
	p, host := newXpraTestPlugin(t)
	mustCall(t, p, "xpra.targets.save", map[string]any{"target": map[string]any{"name": "Terminal", "command": "xterm"}})
	id := mustCall(t, p, "xpra.session.open", map[string]any{"targetId": "xpra-1"})["session"].(map[string]any)["id"].(string)
	p.event("process.stderr", json.RawMessage(`{"id":"process-1","text":"Error: cannot open display"}`))
	p.event("process.exit", json.RawMessage(`{"id":"process-1","exitCode":1}`))
	if len(host.published) != 1 || host.published[0]["event"] != "xpra.session.exited" {
		t.Fatalf("published = %v", host.published)
	}
	data := host.published[0]["data"].(map[string]string)
	if data["id"] != id || !strings.Contains(data["message"], "cannot open display") {
		t.Fatalf("exit event = %v", data)
	}
	raw, _ := json.Marshal(map[string]any{"id": id})
	if _, failure := p.handle("xpra.session.attach", raw); failure == nil || !strings.Contains(failure.Message, "cannot open display") {
		t.Fatalf("attach after exit = %+v", failure)
	}
	mustCall(t, p, "xpra.session.close", map[string]any{"id": id})
	if _, ok := p.xpraSessions[id]; ok {
		t.Fatal("closed exited session was kept")
	}
}

func TestXpraStatusReportsMissingInstall(t *testing.T) {
	p, host := newXpraTestPlugin(t)
	host.runError = &pluginapi.Error{Code: "not_found", Message: "missing"}
	status := mustCall(t, p, "xpra.status", nil)
	if status["available"] != false || !strings.Contains(status["message"].(string), "not installed") {
		t.Fatalf("status = %v", status)
	}
	host.runError = nil
	host.runResult = map[string]any{"success": true, "stdout": "xpra v6.5.3\n"}
	status = mustCall(t, p, "xpra.status", nil)
	if status["available"] != true || status["version"] != "xpra v6.5.3" {
		t.Fatalf("status = %v", status)
	}
}

func TestXpraSessionLimit(t *testing.T) {
	p, _ := newXpraTestPlugin(t)
	mustCall(t, p, "xpra.targets.save", map[string]any{"target": map[string]any{"name": "Terminal", "command": "xterm"}})
	for index := 0; index < maxXpraSessions; index++ {
		mustCall(t, p, "xpra.session.open", map[string]any{"targetId": "xpra-1"})
	}
	raw, _ := json.Marshal(map[string]any{"targetId": "xpra-1"})
	if _, failure := p.handle("xpra.session.open", raw); failure == nil || failure.Code != "resource_limit" {
		t.Fatalf("over limit = %+v", failure)
	}
}

// The start command is a shell-quoted command line that Xpra splits and runs
// without a shell; sh -c evaluates it the same way.
func TestXpraStartCommandRunsInChosenDirectory(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	startArg := func(target xpraTarget) string {
		for _, arg := range xpraArgs(target) {
			if value, ok := strings.CutPrefix(arg, "--start="); ok {
				return value
			}
		}
		t.Fatal("no --start argument")
		return ""
	}
	dir := filepath.Join(t.TempDir(), "it's here")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", startArg(xpraTarget{Mode: xpraModeApp, Command: `pwd && echo "$((1+1))"`, WorkingDirectory: dir})).Output()
	if err != nil || strings.TrimSpace(string(out)) != dir+"\n2" {
		t.Fatalf("working directory output %q, %v", out, err)
	}
	home := t.TempDir()
	command := exec.Command("sh", "-c", startArg(xpraTarget{Mode: xpraModeApp, Command: "pwd"}))
	command.Env = append(os.Environ(), "HOME="+home)
	command.Dir = t.TempDir()
	out, err = command.Output()
	if err != nil || strings.TrimSpace(string(out)) != home {
		t.Fatalf("home directory output %q, %v", out, err)
	}
}
