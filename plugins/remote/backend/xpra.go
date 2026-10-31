package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Xpra runs an application or a virtual desktop on the RunPilot host inside an
// Xpra server owned by this plugin. The server listens only on a Unix socket in
// the plugin workspace; the browser reaches it through a network stream, like
// RDP and VNC targets.

const (
	maxXpraTargets   = 256
	maxXpraSessions  = 4
	maxXpraLogLines  = 200
	maxXpraLogLine   = 512
	xpraWorkspaceDir = "xpra"
	xpraModeApp      = "application"
	xpraModeDesktop  = "desktop"
)

type xpraTarget struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Mode             string `json:"mode"`
	Command          string `json:"command"`
	WorkingDirectory string `json:"workingDirectory,omitempty"`
	Clipboard        *bool  `json:"clipboard,omitempty"`
	DPI              int    `json:"dpi,omitempty"`
}

type xpraTargetStore struct {
	Version int          `json:"version"`
	NextID  uint64       `json:"nextId"`
	Targets []xpraTarget `json:"targets"`
}

type xpraSession struct {
	ID         string `json:"id"`
	TargetID   string `json:"targetId"`
	TargetName string `json:"targetName"`
	Mode       string `json:"mode"`
	State      string `json:"state"`
	StreamID   string `json:"streamId,omitempty"`
	Message    string `json:"message,omitempty"`
	Clipboard  bool   `json:"clipboard"`
	processID  string
	log        []string
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func normalizeXpraTarget(target xpraTarget) (xpraTarget, *rpcError) {
	target.Name = strings.TrimSpace(target.Name)
	target.Command = strings.TrimSpace(target.Command)
	target.WorkingDirectory = strings.TrimSpace(target.WorkingDirectory)
	if target.Name == "" || len(target.Name) > 100 || !utf8.ValidString(target.Name) {
		return xpraTarget{}, fail("invalid_argument", "target name must contain 1 to 100 valid characters")
	}
	if target.Mode == "" {
		target.Mode = xpraModeApp
	}
	if target.Mode != xpraModeApp && target.Mode != xpraModeDesktop {
		return xpraTarget{}, fail("invalid_argument", "mode must be application or desktop")
	}
	if target.Command == "" || len(target.Command) > 2048 || !utf8.ValidString(target.Command) || strings.ContainsAny(target.Command, "\r\n\x00") {
		return xpraTarget{}, fail("invalid_argument", "command must be a single line of at most 2048 characters")
	}
	if target.WorkingDirectory != "" && (!strings.HasPrefix(target.WorkingDirectory, "/") || len(target.WorkingDirectory) > 1024 || !utf8.ValidString(target.WorkingDirectory) || strings.ContainsAny(target.WorkingDirectory, "\r\n\x00")) {
		return xpraTarget{}, fail("invalid_argument", "working directory must be an absolute path")
	}
	if target.DPI != 0 && (target.DPI < 48 || target.DPI > 480) {
		return xpraTarget{}, fail("invalid_argument", "DPI must be between 48 and 480, or 0 for automatic")
	}
	clipboard := boolValue(target.Clipboard, true)
	target.Clipboard = &clipboard
	return target, nil
}

func (p *plugin) readXpraTargets() (xpraTargetStore, *rpcError) {
	stored := xpraTargetStore{Version: 1, NextID: 1, Targets: []xpraTarget{}}
	var raw json.RawMessage
	if err := callHost("storage.get", map[string]any{"key": "xpraTargets"}, &raw); err != nil {
		return stored, hostError(err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return stored, nil
	}
	var decoded xpraTargetStore
	if failure := decode(raw, &decoded); failure != nil || decoded.Version != 1 || decoded.Targets == nil || len(decoded.Targets) > maxXpraTargets {
		return xpraTargetStore{}, fail("invalid_configuration", "stored Xpra targets are unreadable")
	}
	if decoded.NextID == 0 {
		decoded.NextID = 1
	}
	seen := make(map[string]bool, len(decoded.Targets))
	for index := range decoded.Targets {
		normalized, failure := normalizeXpraTarget(decoded.Targets[index])
		id := decoded.Targets[index].ID
		if id == "" || len(id) > 100 || seen[id] || failure != nil {
			return xpraTargetStore{}, fail("invalid_configuration", "stored Xpra targets are invalid")
		}
		seen[id] = true
		decoded.Targets[index] = normalized
	}
	return decoded, nil
}

func (p *plugin) writeXpraTargets(stored xpraTargetStore) *rpcError {
	if err := callHost("storage.set", map[string]any{"key": "xpraTargets", "value": stored}, nil); err != nil {
		return hostError(err)
	}
	return nil
}

func (p *plugin) findXpraTarget(id string) (xpraTarget, *rpcError) {
	stored, failure := p.readXpraTargets()
	if failure != nil {
		return xpraTarget{}, failure
	}
	for _, target := range stored.Targets {
		if target.ID == id {
			return target, nil
		}
	}
	return xpraTarget{}, fail("not_found", "unknown Xpra target")
}

func (p *plugin) saveXpraTarget(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Target xpraTarget `json:"target"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	target, failure := normalizeXpraTarget(request.Target)
	if failure != nil {
		return nil, failure
	}
	stored, failure := p.readXpraTargets()
	if failure != nil {
		return nil, failure
	}
	if target.ID == "" {
		if len(stored.Targets) >= maxXpraTargets {
			return nil, fail("resource_limit", "Xpra target limit reached")
		}
		for {
			target.ID = fmt.Sprintf("xpra-%d", stored.NextID)
			stored.NextID++
			taken := false
			for _, existing := range stored.Targets {
				taken = taken || existing.ID == target.ID
			}
			if !taken {
				break
			}
		}
		stored.Targets = append(stored.Targets, target)
	} else {
		found := false
		for index := range stored.Targets {
			if stored.Targets[index].ID == target.ID {
				stored.Targets[index], found = target, true
			}
		}
		if !found {
			return nil, fail("not_found", "unknown Xpra target")
		}
	}
	if failure := p.writeXpraTargets(stored); failure != nil {
		return nil, failure
	}
	return map[string]any{"target": target}, nil
}

func (p *plugin) deleteXpraTarget(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		ID string `json:"id"`
	}
	if failure := decode(raw, &request); failure != nil || request.ID == "" {
		return nil, fail("invalid_argument", "target id is required")
	}
	for _, active := range p.xpraSessions {
		if active.TargetID == request.ID && active.State != "exited" {
			return nil, fail("failed_precondition", "target has an active session")
		}
	}
	stored, failure := p.readXpraTargets()
	if failure != nil {
		return nil, failure
	}
	for index, target := range stored.Targets {
		if target.ID == request.ID {
			stored.Targets = append(stored.Targets[:index], stored.Targets[index+1:]...)
			if failure := p.writeXpraTargets(stored); failure != nil {
				return nil, failure
			}
			return map[string]any{"ok": true}, nil
		}
	}
	return nil, fail("not_found", "unknown Xpra target")
}

// xpraStatus reports whether the xpra server is installed for the RunPilot
// service account. Xpra servers are Linux-only.
func (p *plugin) xpraStatus() (any, *rpcError) {
	var result struct {
		Success  bool   `json:"success"`
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	err := callHost("process.run", map[string]any{"command": "xpra", "args": []string{"--version"}, "timeoutSeconds": 10, "maxOutputBytes": 4096}, &result)
	if err != nil {
		if failure := hostError(err); failure.Code == "not_found" {
			return map[string]any{"available": false, "message": "The xpra server is not installed for the RunPilot service account. Install Xpra (Linux only), then restart RunPilot."}, nil
		}
		return map[string]any{"available": false, "message": "Xpra could not be queried."}, nil
	}
	if !result.Success {
		return map[string]any{"available": false, "message": "Xpra is installed but did not report its version."}, nil
	}
	return map[string]any{"available": true, "version": strings.TrimSpace(result.Stdout)}, nil
}

// xpraArgs builds the server command. The environment is cleaned so the
// session never binds to a physical display or the host desktop D-Bus.
func xpraArgs(target xpraTarget) []string {
	// Xpra splits start commands like a shell but runs them without one, and
	// from its private session directory. Run the command line through sh so
	// shell syntax works, starting in the configured directory or in the
	// service account's home directory.
	script := `cd -- "${HOME:-/}" 2>/dev/null; ` + target.Command
	if target.WorkingDirectory != "" {
		script = "cd -- " + shellQuote(target.WorkingDirectory) + " && " + target.Command
	}
	command := "sh -c " + shellQuote(script)
	clipboard := "no"
	if boolValue(target.Clipboard, true) {
		clipboard = "yes"
	}
	args := []string{
		"-u", "DISPLAY", "-u", "WAYLAND_DISPLAY", "-u", "DBUS_SESSION_BUS_ADDRESS", "-u", "XAUTHORITY",
		"-u", "XDG_SESSION_TYPE", "-u", "XDG_CURRENT_DESKTOP", "-u", "XDG_SESSION_DESKTOP",
		"-u", "GDK_BACKEND", "-u", "QT_QPA_PLATFORM",
		"xpra", "start",
		"--daemon=no", "--mdns=no", "--html=off", "--systemd-run=no",
		// A relative bind name is created as "<hostname>-xpra.sock" in the
		// working directory, which is the session's workspace directory.
		"--bind=xpra.sock", "--socket-dir=.",
		"--resize-display=yes", "--start-new-commands=no", "--clipboard=" + clipboard,
		"--file-transfer=no", "--printing=no", "--speaker=off", "--microphone=off", "--webcam=no",
		"--notifications=no", "--env=GDK_BACKEND=x11", "--env=QT_QPA_PLATFORM=xcb", "--env=XDG_SESSION_TYPE=x11",
	}
	if target.DPI > 0 {
		args = append(args, fmt.Sprintf("--dpi=%d", target.DPI))
	}
	if target.Mode == xpraModeDesktop {
		for index, value := range args {
			if value == "start" {
				args[index] = "start-desktop"
				break
			}
		}
		args = append(args, "--start-child="+command, "--exit-with-children=yes")
	} else {
		// Application launchers often fork; RunPilot stops the server itself.
		args = append(args, "--start="+command)
	}
	return args
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }

func (p *plugin) xpraSessionDir(id string) string { return xpraWorkspaceDir + "/" + id }

func (p *plugin) openXpraSession(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		TargetID string `json:"targetId"`
	}
	if failure := decode(raw, &request); failure != nil || request.TargetID == "" {
		return nil, fail("invalid_argument", "targetId is required")
	}
	active := 0
	for _, current := range p.xpraSessions {
		if current.State != "exited" {
			active++
		}
	}
	if active >= maxXpraSessions {
		return nil, fail("resource_limit", "Xpra session limit reached")
	}
	target, failure := p.findXpraTarget(request.TargetID)
	if failure != nil {
		return nil, failure
	}
	if active == 0 {
		// Nothing runs, so leftovers from an earlier RunPilot process are stale.
		_ = callHost("workspace.remove", map[string]any{"path": xpraWorkspaceDir, "recursive": true}, nil)
	}
	p.nextXpraSession++
	id := fmt.Sprintf("xpra-session-%d", p.nextXpraSession)
	dir := p.xpraSessionDir(id)
	_ = callHost("workspace.remove", map[string]any{"path": dir, "recursive": true}, nil)
	if err := callHost("workspace.mkdir", map[string]any{"path": dir}, nil); err != nil {
		return nil, hostError(err)
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := callHost("process.start", map[string]any{"command": "env", "args": xpraArgs(target), "workspaceDirectory": dir}, &started); err != nil {
		_ = callHost("workspace.remove", map[string]any{"path": dir, "recursive": true}, nil)
		failure := hostError(err)
		if failure.Code == "not_found" {
			return nil, fail("not_found", "Xpra could not be started; check that it is installed for the RunPilot service account")
		}
		return nil, failure
	}
	session := &xpraSession{ID: id, TargetID: target.ID, TargetName: target.Name, Mode: target.Mode, State: "starting", Clipboard: boolValue(target.Clipboard, true), processID: started.ID}
	p.xpraSessions[id] = session
	return map[string]any{"session": session}, nil
}

func (p *plugin) xpraSessionByID(raw json.RawMessage) (*xpraSession, *rpcError) {
	var request struct {
		ID string `json:"id"`
	}
	if failure := decode(raw, &request); failure != nil || request.ID == "" {
		return nil, fail("invalid_argument", "session id is required")
	}
	session, ok := p.xpraSessions[request.ID]
	if !ok {
		return nil, fail("not_found", "unknown Xpra session")
	}
	return session, nil
}

// attachXpraSession opens the browser stream once the server socket accepts
// connections. Until then it reports ready=false and the browser retries.
func (p *plugin) attachXpraSession(raw json.RawMessage) (any, *rpcError) {
	session, failure := p.xpraSessionByID(raw)
	if failure != nil {
		return nil, failure
	}
	if session.State == "exited" {
		return nil, fail("failed", session.failureMessage())
	}
	if session.StreamID != "" {
		return nil, fail("failed_precondition", "session is already attached")
	}
	var listing struct {
		Entries []struct {
			Name      string `json:"name"`
			Directory bool   `json:"directory"`
			Symlink   bool   `json:"symlink"`
		} `json:"entries"`
	}
	if err := callHost("workspace.list", map[string]any{"path": p.xpraSessionDir(session.ID)}, &listing); err != nil {
		return nil, hostError(err)
	}
	socket := ""
	for _, entry := range listing.Entries {
		if !entry.Directory && !entry.Symlink && strings.HasSuffix(entry.Name, "xpra.sock") {
			socket = entry.Name
		}
	}
	if socket == "" {
		return map[string]any{"ready": false, "session": session}, nil
	}
	var opened struct {
		ID string `json:"id"`
	}
	if err := callHost("network.stream.open", map[string]any{"workspaceSocket": p.xpraSessionDir(session.ID) + "/" + socket, "connectTimeoutSeconds": 2}, &opened); err != nil {
		code := hostError(err).Code
		if code == "not_found" || code == "failed" || code == "timeout" {
			return map[string]any{"ready": false, "session": session}, nil
		}
		return nil, hostError(err)
	}
	session.StreamID, session.State = opened.ID, "running"
	return map[string]any{"ready": true, "session": session, "streamId": opened.ID}, nil
}

func (p *plugin) closeXpraSession(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		ID string `json:"id"`
	}
	if failure := decode(raw, &request); failure != nil || request.ID == "" {
		return nil, fail("invalid_argument", "session id is required")
	}
	session, ok := p.xpraSessions[request.ID]
	if !ok {
		return map[string]any{"ok": true}, nil
	}
	p.stopXpraSession(session)
	if session.State == "exited" {
		p.forgetXpraSession(session)
	}
	return map[string]any{"ok": true}, nil
}

// stopXpraSession closes the stream and asks Xpra to exit. SIGTERM lets Xpra
// stop its virtual display and children; the exit event cleans up.
func (p *plugin) stopXpraSession(session *xpraSession) {
	if session.StreamID != "" {
		_ = callHost("network.stream.close", map[string]string{"id": session.StreamID}, nil)
		session.StreamID = ""
	}
	if session.State != "exited" {
		session.State = "stopping"
		_ = callHost("process.terminate", map[string]string{"id": session.processID}, nil)
	}
}

func (p *plugin) forgetXpraSession(session *xpraSession) {
	_ = callHost("workspace.remove", map[string]any{"path": p.xpraSessionDir(session.ID), "recursive": true}, nil)
	delete(p.xpraSessions, session.ID)
}

func (s *xpraSession) failureMessage() string {
	message := s.Message
	if message == "" {
		message = "Xpra stopped"
	}
	for index := len(s.log) - 1; index >= 0; index-- {
		line := strings.TrimSpace(s.log[index])
		if strings.Contains(strings.ToLower(line), "error") {
			return message + ": " + line
		}
	}
	return message
}

func (p *plugin) xpraDiagnostics(raw json.RawMessage) (any, *rpcError) {
	session, failure := p.xpraSessionByID(raw)
	if failure != nil {
		return nil, failure
	}
	return map[string]any{"log": strings.Join(session.log, "\n"), "sessionState": session.State, "message": session.Message}, nil
}

func (p *plugin) handleXpra(method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "xpra.status":
		return p.xpraStatus()
	case "xpra.targets.list":
		stored, failure := p.readXpraTargets()
		if failure != nil {
			return nil, failure
		}
		return map[string]any{"targets": stored.Targets}, nil
	case "xpra.targets.save":
		return p.saveXpraTarget(raw)
	case "xpra.targets.delete":
		return p.deleteXpraTarget(raw)
	case "xpra.session.open":
		return p.openXpraSession(raw)
	case "xpra.session.attach":
		return p.attachXpraSession(raw)
	case "xpra.session.diagnostics":
		return p.xpraDiagnostics(raw)
	case "xpra.session.close":
		return p.closeXpraSession(raw)
	default:
		return nil, fail("unknown_method", "unknown Xpra method")
	}
}

func (p *plugin) xpraSessionForProcess(processID string) *xpraSession {
	for _, session := range p.xpraSessions {
		if session.processID == processID {
			return session
		}
	}
	return nil
}

// xpraEvent handles process callbacks for Xpra servers. It reports whether
// the event belonged to an Xpra session.
func (p *plugin) xpraEvent(name string, raw json.RawMessage) bool {
	switch name {
	case "process.stdout", "process.stderr":
		var event struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &event) != nil {
			return false
		}
		session := p.xpraSessionForProcess(event.ID)
		if session == nil {
			return false
		}
		for _, line := range strings.Split(strings.TrimRight(event.Text, "\n"), "\n") {
			if len(line) > maxXpraLogLine {
				line = line[:maxXpraLogLine]
			}
			if !utf8.ValidString(line) {
				line = strings.ToValidUTF8(line, "?")
			}
			session.log = append(session.log, line)
		}
		if len(session.log) > maxXpraLogLines {
			session.log = append([]string(nil), session.log[len(session.log)-maxXpraLogLines:]...)
		}
		return true
	case "process.exit":
		var event struct {
			ID       string `json:"id"`
			ExitCode int    `json:"exitCode"`
		}
		if json.Unmarshal(raw, &event) != nil {
			return false
		}
		session := p.xpraSessionForProcess(event.ID)
		if session == nil {
			return false
		}
		stopping := session.State == "stopping"
		session.State = "exited"
		if session.StreamID != "" {
			_ = callHost("network.stream.close", map[string]string{"id": session.StreamID}, nil)
			session.StreamID = ""
		}
		if stopping {
			p.forgetXpraSession(session)
			return true
		}
		session.Message = fmt.Sprintf("Xpra exited with code %d", event.ExitCode)
		_ = callHost("events.publish", map[string]any{"event": "xpra.session.exited", "data": map[string]string{"id": session.ID, "message": session.failureMessage()}}, nil)
		return true
	case "network.stream.closed":
		var event struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &event) != nil {
			return false
		}
		for _, session := range p.xpraSessions {
			if session.StreamID == event.ID {
				session.StreamID = ""
				return true
			}
		}
	}
	return false
}

func (p *plugin) shutdownXpra() {
	for _, session := range p.xpraSessions {
		p.stopXpraSession(session)
	}
	p.xpraSessions = map[string]*xpraSession{}
}
