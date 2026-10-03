package main

import (
	"encoding/json"
	"strings"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

var callHost = pluginapi.CallHost

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(code, message string) *rpcError { return &rpcError{code, message} }
func hostError(err error) *rpcError {
	if e, ok := pluginapi.AsCapabilityError(err); ok {
		return fail(e.Code, e.Message)
	}
	return fail("host_failure", err.Error())
}

type session struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Command  string `json:"command,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Reason   string `json:"reason,omitempty"`
}
type plugin struct{ sessions map[string]session }

type terminalSettings struct {
	Version int      `json:"version"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func newPlugin() *plugin { return &plugin{sessions: map[string]session{}} }
func decode(raw json.RawMessage, v any) *rpcError {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fail("invalid_argument", "invalid request: "+err.Error())
	}
	return nil
}
func (p *plugin) handle(method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "terminal.settings.get":
		return p.getSettings()
	case "terminal.settings.set":
		return p.setSettings(raw)
	case "terminal.settings.test":
		return p.testSettings(raw)
	case "terminal.open":
		return p.open(raw)
	case "terminal.write":
		return p.write(raw)
	case "terminal.resize":
		return p.resize(raw)
	case "terminal.status":
		return p.status(raw)
	case "terminal.close":
		return p.close(raw)
	default:
		return nil, fail("unknown_method", "unknown terminal method")
	}
}

func (p *plugin) readSettings() (terminalSettings, *rpcError) {
	settings := terminalSettings{Version: 1, Args: []string{}}
	var raw json.RawMessage
	if err := callHost("storage.get", map[string]any{"key": "settings"}, &raw); err != nil {
		return settings, hostError(err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return settings, nil
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return settings, fail("invalid_configuration", "stored terminal settings are unreadable")
	}
	if settings.Version != 1 {
		return settings, fail("invalid_configuration", "unsupported terminal settings version")
	}
	if settings.Args == nil {
		settings.Args = []string{}
	}
	return settings, validateSettings(settings)
}

func validateSettings(settings terminalSettings) *rpcError {
	if strings.ContainsRune(settings.Command, 0) {
		return fail("invalid_argument", "terminal command must not contain NUL characters")
	}
	if settings.Command == "" && len(settings.Args) > 0 {
		return fail("invalid_argument", "terminal arguments require a command")
	}
	for _, argument := range settings.Args {
		if strings.ContainsRune(argument, 0) {
			return fail("invalid_argument", "terminal arguments must not contain NUL characters")
		}
	}
	return nil
}

func (p *plugin) getSettings() (any, *rpcError) {
	settings, failure := p.readSettings()
	if failure != nil {
		return nil, failure
	}
	var host struct {
		OS string `json:"os"`
	}
	if err := callHost("system.status", map[string]any{}, &host); err != nil {
		return nil, hostError(err)
	}
	return map[string]any{"settings": settings, "defaultCommand": defaultShell(host.OS)}, nil
}

func (p *plugin) setSettings(raw json.RawMessage) (any, *rpcError) {
	if _, failure := p.readSettings(); failure != nil {
		return nil, failure
	}
	settings := terminalSettings{Version: 1, Args: []string{}}
	if failure := decode(raw, &settings); failure != nil {
		return nil, failure
	}
	settings.Version = 1
	settings.Command = strings.TrimSpace(settings.Command)
	if failure := validateSettings(settings); failure != nil {
		return nil, failure
	}
	if err := callHost("storage.set", map[string]any{"key": "settings", "value": settings}, nil); err != nil {
		return nil, hostError(err)
	}
	return map[string]any{"settings": settings}, nil
}

func (p *plugin) testSettings(raw json.RawMessage) (any, *rpcError) {
	settings := terminalSettings{Args: []string{}}
	if failure := decode(raw, &settings); failure != nil {
		return nil, failure
	}
	settings.Command = strings.TrimSpace(settings.Command)
	if failure := validateSettings(settings); failure != nil {
		return nil, failure
	}
	if len(p.sessions) >= 8 {
		return nil, fail("resource_limit", "terminal session limit reached")
	}
	var host struct {
		OS string `json:"os"`
	}
	if err := callHost("system.status", map[string]any{}, &host); err != nil {
		return nil, hostError(err)
	}
	command, args := settings.Command, settings.Args
	if command == "" {
		command, args = defaultShell(host.OS), defaultShellArgs(host.OS)
	}
	var created session
	err := callHost("process.session.create", map[string]any{"command": command, "args": args, "size": map[string]int{"rows": 24, "columns": 80}}, &created)
	if err != nil && host.OS == "windows" && settings.Command == "" {
		err = callHost("process.session.create", map[string]any{"command": "powershell.exe", "size": map[string]int{"rows": 24, "columns": 80}}, &created)
	}
	if err != nil {
		return nil, hostError(err)
	}
	p.sessions[created.ID] = created
	request, err := json.Marshal(map[string]any{"id": created.ID, "force": true})
	if err != nil {
		return nil, hostError(err)
	}
	if _, failure := p.close(request); failure != nil {
		return nil, failure
	}
	return map[string]any{"ok": true}, nil
}

func (p *plugin) open(raw json.RawMessage) (any, *rpcError) {
	var req struct {
		WorkingDirectory string `json:"workingDirectory"`
		Rows             int    `json:"rows"`
		Columns          int    `json:"columns"`
	}
	if e := decode(raw, &req); e != nil {
		return nil, e
	}
	for id, current := range p.sessions {
		if current.State != "running" {
			delete(p.sessions, id)
		}
	}
	if len(p.sessions) >= 8 {
		return nil, fail("resource_limit", "terminal session limit reached")
	}
	rows, cols := req.Rows, req.Columns
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}
	var host struct {
		OS string `json:"os"`
	}
	if err := callHost("system.status", map[string]any{}, &host); err != nil {
		return nil, hostError(err)
	}
	command := defaultShell(host.OS)
	args := defaultShellArgs(host.OS)
	settings, failure := p.readSettings()
	if failure != nil {
		return nil, failure
	}
	if settings.Command != "" {
		command, args = settings.Command, settings.Args
	}
	var created session
	err := callHost("process.session.create", map[string]any{"command": command, "args": args, "workingDirectory": req.WorkingDirectory, "size": map[string]int{"rows": rows, "columns": cols}}, &created)
	if err != nil && host.OS == "windows" && settings.Command == "" && command == "cmd.exe" {
		command = "powershell.exe"
		args = nil
		err = callHost("process.session.create", map[string]any{"command": command, "workingDirectory": req.WorkingDirectory, "size": map[string]int{"rows": rows, "columns": cols}}, &created)
	}
	if err != nil {
		return nil, hostError(err)
	}
	created.Command = command
	p.sessions[created.ID] = created
	return map[string]any{"session": created}, nil
}
func defaultShell(os string) string {
	if os == "windows" {
		return "cmd.exe"
	}
	return "/bin/sh"
}
func defaultShellArgs(os string) []string {
	if os == "linux" {
		// The session inherits the RunPilot service environment. Honor its shell
		// when executable, with /bin/sh as the portable fallback.
		return []string{"-c", `if [ -n "$SHELL" ] && [ -x "$SHELL" ]; then exec "$SHELL"; else exec /bin/sh; fi`}
	}
	return nil
}
func (p *plugin) owned(id string) *rpcError {
	if id == "" {
		return fail("invalid_argument", "session id is required")
	}
	if _, ok := p.sessions[id]; !ok {
		return fail("not_found", "unknown terminal session")
	}
	return nil
}
func (p *plugin) write(raw json.RawMessage) (any, *rpcError) {
	var r struct{ ID, Data string }
	if e := decode(raw, &r); e != nil {
		return nil, e
	}
	if e := p.owned(r.ID); e != nil {
		return nil, e
	}
	if err := callHost("process.session.write", map[string]string{"id": r.ID, "data": r.Data}, nil); err != nil {
		return nil, hostError(err)
	}
	return map[string]any{"ok": true}, nil
}
func (p *plugin) resize(raw json.RawMessage) (any, *rpcError) {
	var r struct {
		ID      string `json:"id"`
		Rows    int    `json:"rows"`
		Columns int    `json:"columns"`
	}
	if e := decode(raw, &r); e != nil {
		return nil, e
	}
	if e := p.owned(r.ID); e != nil {
		return nil, e
	}
	if err := callHost("process.session.resize", map[string]any{"id": r.ID, "rows": r.Rows, "columns": r.Columns}, nil); err != nil {
		return nil, hostError(err)
	}
	return map[string]any{"ok": true}, nil
}
func (p *plugin) status(raw json.RawMessage) (any, *rpcError) {
	var r struct {
		ID string `json:"id"`
	}
	if e := decode(raw, &r); e != nil {
		return nil, e
	}
	if e := p.owned(r.ID); e != nil {
		return nil, e
	}
	var out json.RawMessage
	if err := callHost("process.session.status", map[string]string{"id": r.ID}, &out); err != nil {
		if current, ok := p.sessions[r.ID]; ok && current.State != "running" {
			return map[string]any{"id": current.ID, "state": current.State, "exitCode": current.ExitCode, "reason": current.Reason}, nil
		}
		return nil, hostError(err)
	}
	var view map[string]any
	_ = json.Unmarshal(out, &view)
	if current, ok := p.sessions[r.ID]; ok {
		if state, ok := view["state"].(string); ok {
			current.State = state
		}
		if reason, ok := view["reason"].(string); ok {
			current.Reason = reason
		}
		if code, ok := view["exitCode"].(float64); ok {
			value := int(code)
			current.ExitCode = &value
		}
		p.sessions[r.ID] = current
	}
	return view, nil
}
func (p *plugin) close(raw json.RawMessage) (any, *rpcError) {
	var r struct {
		ID    string `json:"id"`
		Force bool   `json:"force"`
	}
	if e := decode(raw, &r); e != nil {
		return nil, e
	}
	if e := p.owned(r.ID); e != nil {
		return nil, e
	}
	if current := p.sessions[r.ID]; current.State != "running" {
		delete(p.sessions, r.ID)
		return map[string]any{"ok": true}, nil
	}
	var out json.RawMessage
	if err := callHost("process.session.terminate", map[string]any{"id": r.ID, "force": r.Force}, &out); err != nil {
		if cap, ok := pluginapi.AsCapabilityError(err); ok && cap.Code == "not_found" {
			delete(p.sessions, r.ID)
			return map[string]any{"ok": true}, nil
		}
		return nil, hostError(err)
	}
	delete(p.sessions, r.ID)
	return map[string]any{"ok": true}, nil
}
func (p *plugin) event(name string, raw json.RawMessage) *rpcError {
	if name != "process.session.output" && name != "process.session.exit" && name != "process.session.error" {
		return nil
	}
	var data struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return fail("invalid_argument", "invalid process session event")
	}
	if _, ok := p.sessions[data.ID]; !ok {
		return nil
	}
	if err := callHost("events.publish", map[string]any{"event": name, "data": json.RawMessage(raw)}, nil); err != nil {
		return hostError(err)
	}
	if name == "process.session.exit" || name == "process.session.error" {
		current := p.sessions[data.ID]
		if name == "process.session.exit" {
			var exit struct {
				State    string `json:"state"`
				ExitCode *int   `json:"exitCode"`
				Reason   string `json:"reason"`
			}
			if json.Unmarshal(raw, &exit) == nil {
				current.State, current.ExitCode, current.Reason = exit.State, exit.ExitCode, exit.Reason
			}
		} else {
			current.State, current.Reason = "exited", "io_error"
		}
		p.sessions[data.ID] = current
	}
	return nil
}
func (p *plugin) shutdown() {
	for id, session := range p.sessions {
		if session.State == "running" {
			_ = callHost("process.session.terminate", map[string]any{"id": id, "force": true}, nil)
		}
	}
	p.sessions = map[string]session{}
}
