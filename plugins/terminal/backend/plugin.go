package main

import (
	"encoding/json"
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
	ID      string `json:"id"`
	State   string `json:"state"`
	Command string `json:"command,omitempty"`
}
type plugin struct{ sessions map[string]session }

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
func (p *plugin) open(raw json.RawMessage) (any, *rpcError) {
	var req struct {
		WorkingDirectory string `json:"workingDirectory"`
		Rows             int    `json:"rows"`
		Columns          int    `json:"columns"`
	}
	if e := decode(raw, &req); e != nil {
		return nil, e
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
	var created session
	err := callHost("process.session.create", map[string]any{"command": command, "args": args, "workingDirectory": req.WorkingDirectory, "size": map[string]int{"rows": rows, "columns": cols}}, &created)
	if err != nil && host.OS == "windows" && command == "cmd.exe" {
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
		return nil, hostError(err)
	}
	var view map[string]any
	_ = json.Unmarshal(out, &view)
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
	var out json.RawMessage
	if err := callHost("process.session.terminate", map[string]any{"id": r.ID, "force": r.Force}, &out); err != nil {
		return nil, hostError(err)
	}
	delete(p.sessions, r.ID)
	return map[string]any{"ok": true}, nil
}
func (p *plugin) event(name string, raw json.RawMessage) {
	if name != "process.session.output" && name != "process.session.exit" && name != "process.session.error" {
		return
	}
	var data struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return
	}
	if _, ok := p.sessions[data.ID]; !ok {
		return
	}
	_ = callHost("events.publish", map[string]any{"event": name, "data": json.RawMessage(raw)}, nil)
	if name == "process.session.exit" || name == "process.session.error" {
		delete(p.sessions, data.ID)
	}
}
func (p *plugin) shutdown() {
	for id := range p.sessions {
		_ = callHost("process.session.terminate", map[string]any{"id": id, "force": true}, nil)
	}
	p.sessions = map[string]session{}
}
