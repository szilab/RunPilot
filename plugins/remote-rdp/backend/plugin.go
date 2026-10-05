package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

var callHost = pluginapi.CallHost

const (
	maxGuacElement      = 1 << 20
	maxGuacInstruction  = 64 << 10
	guacReadWindow      = 4 << 10
	maxGuacArgs         = 512
	maxRDPClients       = 8
	streamChunk         = 48 << 10
	maxDiagnostics      = 100
	maxDiagnosticScopes = 32
)

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(code, message string) *rpcError { return &rpcError{Code: code, Message: message} }

func hostError(err error) *rpcError {
	if value, ok := pluginapi.AsCapabilityError(err); ok {
		return fail(value.Code, value.Message)
	}
	return fail("failed", "network stream operation failed")
}

type guacdSettings struct {
	Version               int    `json:"version"`
	Host                  string `json:"host"`
	Port                  int    `json:"port"`
	TLS                   bool   `json:"tls"`
	ConnectTimeoutSeconds int    `json:"connectTimeoutSeconds"`
}

type rdpOptions struct {
	Host                   string `json:"host"`
	Port                   int    `json:"port"`
	Username               string `json:"username"`
	Domain                 string `json:"domain"`
	SecurityMode           string `json:"securityMode"`
	Clipboard              *bool  `json:"clipboard"`
	DynamicResize          *bool  `json:"dynamicResize"`
	ServerLayout           string `json:"serverLayout"`
	ResizeMethod           string `json:"resizeMethod"`
	DPIMode                string `json:"dpiMode"`
	DPI                    int    `json:"dpi"`
	ColorDepth             int    `json:"colorDepth"`
	CertificatePolicy      string `json:"certificatePolicy"`
	CertificateFingerprint string `json:"certificateFingerprint"`
	Copy                   *bool  `json:"copy"`
	Paste                  *bool  `json:"paste"`
	ClipboardNormalization string `json:"clipboardNormalization"`
	PerformanceProfile     string `json:"performanceProfile"`
	TimeoutSeconds         int    `json:"timeoutSeconds"`
	TimeZone               string `json:"timeZone"`
}

type credentials struct {
	Username string `json:"username"`
	Domain   string `json:"domain"`
	Password string `json:"password"`
}

type clientInfo struct {
	Width          int      `json:"width"`
	Height         int      `json:"height"`
	DPI            int      `json:"dpi"`
	TimeZone       string   `json:"timeZone"`
	Name           string   `json:"clientName"`
	AudioMimetypes []string `json:"audioMimetypes"`
	VideoMimetypes []string `json:"videoMimetypes"`
	ImageMimetypes []string `json:"imageMimetypes"`
}

type session struct {
	ID           string `json:"id"`
	TargetID     string `json:"targetId"`
	TargetName   string `json:"targetName"`
	DiagnosticID string `json:"diagnosticId"`
	StreamID     string `json:"streamId"`
	ConnectionID string `json:"connectionId"`
	State        string `json:"state"`
}

type rdpTarget struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Options rdpOptions `json:"options"`
}

type storedTargets struct {
	Version int         `json:"version"`
	NextID  uint64      `json:"nextId"`
	Targets []rdpTarget `json:"targets"`
}

type plugin struct {
	sessions       map[string]session
	diagnostics    map[string][]string
	diagnosticIDs  []string
	sessionDiagIDs map[string]string
	nextDiagnostic uint64
}

func newPlugin() *plugin {
	return &plugin{sessions: map[string]session{}, diagnostics: map[string][]string{}, sessionDiagIDs: map[string]string{}, nextDiagnostic: 1}
}

func validDiagnosticID(value string) bool {
	if len(value) == 0 || len(value) > 96 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

func (p *plugin) newDiagnosticID() string {
	id := fmt.Sprintf("rdp-%d", p.nextDiagnostic)
	p.nextDiagnostic++
	if p.nextDiagnostic == 0 {
		p.nextDiagnostic = 1
	}
	return id
}

func (p *plugin) addDiagnostic(id, stage string) {
	if !validDiagnosticID(id) || stage == "" || len(stage) > 256 || !utf8.ValidString(stage) || strings.ContainsAny(stage, "\r\n") {
		return
	}
	if _, exists := p.diagnostics[id]; !exists {
		p.diagnosticIDs = append(p.diagnosticIDs, id)
	}
	lines := append(p.diagnostics[id], stage)
	if len(lines) > maxDiagnostics {
		lines = append([]string(nil), lines[len(lines)-maxDiagnostics:]...)
	}
	p.diagnostics[id] = lines
	for len(p.diagnosticIDs) > maxDiagnosticScopes {
		oldest := p.diagnosticIDs[0]
		p.diagnosticIDs = p.diagnosticIDs[1:]
		delete(p.diagnostics, oldest)
		for sessionID, diagnosticID := range p.sessionDiagIDs {
			if diagnosticID == oldest {
				delete(p.sessionDiagIDs, sessionID)
			}
		}
	}
	_ = callHost("events.publish", map[string]any{"event": "rdp.session.diagnostic", "data": map[string]string{"diagnosticId": id, "stage": stage}}, nil)
}

func (p *plugin) getDiagnostics(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		ID           string `json:"id"`
		DiagnosticID string `json:"diagnosticId"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	diagnosticID := request.DiagnosticID
	if request.ID != "" {
		session, ok := p.sessions[request.ID]
		if ok {
			diagnosticID = session.DiagnosticID
		} else if storedID := p.sessionDiagIDs[request.ID]; storedID != "" {
			diagnosticID = storedID
		} else {
			return nil, fail("not_found", "unknown RDP session")
		}
	}
	if !validDiagnosticID(diagnosticID) {
		return nil, fail("invalid_argument", "diagnosticId is required")
	}
	lines, ok := p.diagnostics[diagnosticID]
	if !ok {
		return nil, fail("not_found", "unknown RDP diagnostics")
	}
	var active *session
	for _, current := range p.sessions {
		if current.DiagnosticID == diagnosticID {
			copy := current
			active = &copy
			break
		}
	}
	result := map[string]any{"diagnosticId": diagnosticID, "log": strings.Join(lines, "\n"), "sessionState": "starting"}
	if active != nil {
		result["sessionId"] = active.ID
		result["targetName"] = active.TargetName
		result["sessionState"] = active.State
	}
	return result, nil
}

func decode(raw json.RawMessage, value any) *rpcError {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return fail("invalid_argument", "invalid request")
	}
	return nil
}

func (p *plugin) handle(method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "rdp.settings.get":
		settings, failure := p.readSettings()
		if failure != nil {
			return nil, failure
		}
		return map[string]any{"settings": settings}, nil
	case "rdp.settings.set":
		return p.setSettings(raw)
	case "rdp.settings.test":
		return p.testSettings(raw)
	case "rdp.targets.list":
		return p.listTargets()
	case "rdp.targets.get":
		return p.getTarget(raw)
	case "rdp.targets.save":
		return p.saveTarget(raw)
	case "rdp.targets.delete":
		return p.deleteTarget(raw)
	case "rdp.session.open":
		return p.open(raw)
	case "rdp.session.status":
		return p.status(raw)
	case "rdp.session.diagnostics":
		return p.getDiagnostics(raw)
	case "rdp.session.close":
		return p.close(raw)
	default:
		return nil, fail("unknown_method", "unknown RDP method")
	}
}

func defaultSettings() guacdSettings {
	return guacdSettings{Version: 1, Host: "127.0.0.1", Port: 4822, ConnectTimeoutSeconds: 5}
}

func validateSettings(settings guacdSettings) *rpcError {
	if !validHost(settings.Host) || settings.Port < 1 || settings.Port > 65535 || settings.ConnectTimeoutSeconds < 1 || settings.ConnectTimeoutSeconds > 30 {
		return fail("invalid_argument", "guacd host, port or timeout is invalid")
	}
	return nil
}

func (p *plugin) readSettings() (guacdSettings, *rpcError) {
	settings := defaultSettings()
	var raw json.RawMessage
	if err := callHost("storage.get", map[string]any{"key": "settings"}, &raw); err != nil {
		return settings, hostError(err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return settings, nil
	}
	if json.Unmarshal(raw, &settings) != nil || settings.Version != 1 {
		return guacdSettings{}, fail("invalid_configuration", "stored guacd settings are unreadable")
	}
	if failure := validateSettings(settings); failure != nil {
		return guacdSettings{}, fail("invalid_configuration", "stored guacd settings are invalid")
	}
	return settings, nil
}

func (p *plugin) setSettings(raw json.RawMessage) (any, *rpcError) {
	if _, failure := p.readSettings(); failure != nil {
		return nil, failure
	}
	settings := defaultSettings()
	if failure := decode(raw, &settings); failure != nil {
		return nil, failure
	}
	settings.Version = 1
	if failure := validateSettings(settings); failure != nil {
		return nil, failure
	}
	if err := callHost("storage.set", map[string]any{"key": "settings", "value": settings}, nil); err != nil {
		return nil, hostError(err)
	}
	return map[string]any{"settings": settings}, nil
}

func (p *plugin) testSettings(raw json.RawMessage) (any, *rpcError) {
	settings := defaultSettings()
	if failure := decode(raw, &settings); failure != nil {
		return nil, failure
	}
	settings.Version = 1
	if failure := validateSettings(settings); failure != nil {
		return nil, failure
	}
	var opened struct {
		ID string `json:"id"`
	}
	if err := callHost("network.stream.open", streamOpenParams(settings), &opened); err != nil {
		return nil, hostError(err)
	}
	if opened.ID == "" {
		return nil, fail("failed", "could not test guacd connection")
	}
	if err := callHost("network.stream.close", map[string]string{"id": opened.ID}, nil); err != nil {
		return nil, hostError(err)
	}
	return map[string]any{"available": true}, nil
}

func (p *plugin) readTargets() (storedTargets, *rpcError) {
	stored := storedTargets{Version: 1, NextID: 1, Targets: []rdpTarget{}}
	var raw json.RawMessage
	if err := callHost("storage.get", map[string]any{"key": "targets"}, &raw); err != nil {
		return stored, hostError(err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return stored, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&stored) != nil || stored.Version != 1 || stored.Targets == nil || len(stored.Targets) > 256 {
		return storedTargets{}, fail("invalid_configuration", "stored RDP targets are unreadable")
	}
	if stored.NextID == 0 {
		stored.NextID = 1
	}
	seen := make(map[string]bool, len(stored.Targets))
	for index := range stored.Targets {
		target := &stored.Targets[index]
		target.Name = strings.TrimSpace(target.Name)
		if target.ID == "" || seen[target.ID] || target.Name == "" || len(target.Name) > 100 {
			return storedTargets{}, fail("invalid_configuration", "stored RDP targets are invalid")
		}
		seen[target.ID] = true
		options, failure := normalizeOptions(target.Options)
		if failure != nil {
			return storedTargets{}, fail("invalid_configuration", "stored RDP targets are invalid")
		}
		target.Options = options
	}
	return stored, nil
}

func (p *plugin) listTargets() (any, *rpcError) {
	stored, failure := p.readTargets()
	if failure != nil {
		return nil, failure
	}
	return map[string]any{"targets": stored.Targets}, nil
}

func (p *plugin) getTarget(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		ID string `json:"id"`
	}
	if failure := decode(raw, &request); failure != nil || request.ID == "" {
		return nil, fail("invalid_argument", "target id is required")
	}
	stored, failure := p.readTargets()
	if failure != nil {
		return nil, failure
	}
	for _, target := range stored.Targets {
		if target.ID == request.ID {
			return map[string]any{"target": target}, nil
		}
	}
	return nil, fail("not_found", "unknown RDP target")
}

func (p *plugin) saveTarget(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Target rdpTarget `json:"target"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	target := request.Target
	target.Name = strings.TrimSpace(target.Name)
	if target.Name == "" || len(target.Name) > 100 {
		return nil, fail("invalid_argument", "target name must contain 1 to 100 characters")
	}
	options, failure := normalizeOptions(target.Options)
	if failure != nil {
		return nil, failure
	}
	target.Options = options
	stored, failure := p.readTargets()
	if failure != nil {
		return nil, failure
	}
	if target.ID == "" {
		if stored.NextID == ^uint64(0) {
			return nil, fail("resource_limit", "RDP target ID sequence is exhausted")
		}
		for {
			target.ID = fmt.Sprintf("rdp-%d", stored.NextID)
			stored.NextID++
			found := false
			for _, existing := range stored.Targets {
				if existing.ID == target.ID {
					found = true
					break
				}
			}
			if !found {
				break
			}
			if stored.NextID == ^uint64(0) {
				return nil, fail("resource_limit", "RDP target ID sequence is exhausted")
			}
		}
		stored.Targets = append(stored.Targets, target)
	} else {
		found := false
		for index := range stored.Targets {
			if stored.Targets[index].ID == target.ID {
				stored.Targets[index] = target
				found = true
				break
			}
		}
		if !found {
			return nil, fail("not_found", "unknown RDP target")
		}
	}
	if len(stored.Targets) > 256 {
		return nil, fail("resource_limit", "RDP target limit reached")
	}
	if err := callHost("storage.set", map[string]any{"key": "targets", "value": stored}, nil); err != nil {
		return nil, hostError(err)
	}
	return map[string]any{"target": target}, nil
}

func (p *plugin) deleteTarget(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		ID string `json:"id"`
	}
	if failure := decode(raw, &request); failure != nil || request.ID == "" {
		return nil, fail("invalid_argument", "target id is required")
	}
	for _, active := range p.sessions {
		if active.TargetID == request.ID {
			return nil, fail("failed_precondition", "target has an active session")
		}
	}
	stored, failure := p.readTargets()
	if failure != nil {
		return nil, failure
	}
	for index, target := range stored.Targets {
		if target.ID == request.ID {
			stored.Targets = append(stored.Targets[:index], stored.Targets[index+1:]...)
			if err := callHost("storage.set", map[string]any{"key": "targets", "value": stored}, nil); err != nil {
				return nil, hostError(err)
			}
			return map[string]any{"ok": true}, nil
		}
	}
	return nil, fail("not_found", "unknown RDP target")
}

func streamOpenParams(settings guacdSettings) map[string]any {
	return map[string]any{
		"host": settings.Host, "port": settings.Port, "connectTimeoutSeconds": settings.ConnectTimeoutSeconds,
		"tls": map[string]any{"enabled": settings.TLS, "serverName": ""},
	}
}

func (p *plugin) open(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		TargetID     string      `json:"targetId"`
		DiagnosticID string      `json:"diagnosticId"`
		Credentials  credentials `json:"credentials"`
		Client       clientInfo  `json:"client"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	if len(p.sessions) >= maxRDPClients {
		return nil, fail("resource_limit", "RDP session limit reached")
	}
	if request.TargetID == "" {
		return nil, fail("invalid_argument", "targetId is required")
	}
	diagnosticID := request.DiagnosticID
	if diagnosticID == "" {
		diagnosticID = p.newDiagnosticID()
	}
	if !validDiagnosticID(diagnosticID) {
		return nil, fail("invalid_argument", "diagnosticId is invalid")
	}
	p.addDiagnostic(diagnosticID, "RDP session open requested")
	targets, failure := p.readTargets()
	if failure != nil {
		return nil, failure
	}
	var target *rdpTarget
	for index := range targets.Targets {
		if targets.Targets[index].ID == request.TargetID {
			target = &targets.Targets[index]
			break
		}
	}
	if target == nil {
		p.addDiagnostic(diagnosticID, "target lookup failed (not_found)")
		return nil, fail("not_found", "unknown RDP target")
	}
	p.addDiagnostic(diagnosticID, "target loaded")
	options, failure := normalizeOptions(target.Options)
	if failure != nil {
		p.addDiagnostic(diagnosticID, "target validation failed (invalid_configuration)")
		return nil, fail("invalid_configuration", "stored RDP target is invalid")
	}
	credentialState := "omitted"
	if request.Credentials.Password != "" {
		credentialState = "supplied"
	}
	p.addDiagnostic(diagnosticID, fmt.Sprintf("target endpoint %s:%d; security %s; password %s", options.Host, options.Port, options.SecurityMode, credentialState))
	identity := request.Credentials
	request.Credentials.Password = ""
	defer func() { identity.Password = "" }()
	if identity.Username == "" {
		identity.Username = options.Username
	}
	if identity.Domain == "" {
		identity.Domain = options.Domain
	}
	if (options.SecurityMode == "nla" || options.SecurityMode == "nla-ext") && identity.Password == "" {
		p.addDiagnostic(diagnosticID, "session validation failed (failed_precondition)")
		return nil, fail("failed_precondition", "this RDP security mode requires a password")
	}
	p.addDiagnostic(diagnosticID, "opening guacd stream")
	settings, failure := p.readSettings()
	if failure != nil {
		p.addDiagnostic(diagnosticID, "guacd settings unavailable ("+failure.Code+")")
		return nil, failure
	}
	transportSecurity := "TLS disabled"
	if settings.TLS {
		transportSecurity = "TLS enabled"
	}
	p.addDiagnostic(diagnosticID, fmt.Sprintf("guacd endpoint %s:%d; %s", settings.Host, settings.Port, transportSecurity))
	var opened struct {
		ID string `json:"id"`
	}
	if err := callHost("network.stream.open", streamOpenParams(settings), &opened); err != nil {
		failure := hostError(err)
		p.addDiagnostic(diagnosticID, "guacd stream open failed ("+failure.Code+")")
		return nil, failure
	}
	if opened.ID == "" {
		p.addDiagnostic(diagnosticID, "guacd stream open returned no id")
		return nil, fail("failed", "could not open guacd stream")
	}
	p.addDiagnostic(diagnosticID, "guacd stream connected")
	stream := guacStream{id: opened.ID}
	connectionID, failure := p.negotiate(&stream, options, identity, request.Client, func(stage string) {
		p.addDiagnostic(diagnosticID, stage)
	})
	if failure != nil {
		p.addDiagnostic(diagnosticID, "Guacamole negotiation failed ("+failure.Code+")")
		_ = callHost("network.stream.close", map[string]string{"id": opened.ID}, nil)
		identity.Password = ""
		return nil, failure
	}
	identity.Password = ""
	p.sessionDiagIDs[opened.ID] = diagnosticID
	session := session{ID: opened.ID, TargetID: target.ID, TargetName: target.Name, DiagnosticID: diagnosticID, StreamID: opened.ID, ConnectionID: connectionID, State: "ready"}
	p.sessions[session.ID] = session
	p.addDiagnostic(diagnosticID, "RDP session ready")
	return map[string]any{"session": session, "streamId": opened.ID}, nil
}

func normalizeOptions(input rdpOptions) (rdpOptions, *rpcError) {
	input.Host = strings.TrimSpace(input.Host)
	if !validHost(input.Host) {
		return rdpOptions{}, fail("invalid_argument", "RDP host is invalid")
	}
	if input.Port == 0 {
		input.Port = 3389
	}
	if input.Port < 1 || input.Port > 65535 {
		return rdpOptions{}, fail("invalid_argument", "RDP port is invalid")
	}
	if input.SecurityMode == "" {
		input.SecurityMode = "automatic"
	}
	switch input.SecurityMode {
	case "automatic", "nla", "nla-ext", "tls", "rdp":
	default:
		return rdpOptions{}, fail("invalid_argument", "RDP security mode is invalid")
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 10
	}
	if input.TimeoutSeconds < 1 || input.TimeoutSeconds > 120 {
		return rdpOptions{}, fail("invalid_argument", "RDP timeout is invalid")
	}
	if input.ResizeMethod == "" {
		input.ResizeMethod = "display-update"
	}
	if input.DynamicResize != nil && !*input.DynamicResize && input.ResizeMethod == "display-update" {
		input.ResizeMethod = "fixed"
	}
	if input.ResizeMethod != "display-update" && input.ResizeMethod != "reconnect" && input.ResizeMethod != "fixed" {
		return rdpOptions{}, fail("invalid_argument", "RDP resize method is invalid")
	}
	if input.DPIMode == "" {
		input.DPIMode = "auto"
	}
	if input.DPIMode != "auto" && input.DPIMode != "96" && input.DPIMode != "custom" {
		return rdpOptions{}, fail("invalid_argument", "RDP DPI mode is invalid")
	}
	if input.DPIMode == "96" {
		input.DPI = 96
	}
	if input.DPIMode == "custom" && (input.DPI < 72 || input.DPI > 240) {
		return rdpOptions{}, fail("invalid_argument", "RDP DPI is invalid")
	}
	if input.ColorDepth != 0 && input.ColorDepth != 8 && input.ColorDepth != 16 && input.ColorDepth != 24 {
		return rdpOptions{}, fail("invalid_argument", "RDP color depth is invalid")
	}
	if input.CertificatePolicy == "" {
		input.CertificatePolicy = "validate"
	}
	switch input.CertificatePolicy {
	case "validate", "tofu", "ignore", "fingerprint":
	default:
		return rdpOptions{}, fail("invalid_argument", "RDP certificate policy is invalid")
	}
	if input.CertificatePolicy == "fingerprint" && strings.TrimSpace(input.CertificateFingerprint) == "" {
		return rdpOptions{}, fail("invalid_argument", "RDP certificate fingerprint is required")
	}
	if input.ClipboardNormalization == "" {
		input.ClipboardNormalization = "preserve"
	}
	if input.ClipboardNormalization != "preserve" && input.ClipboardNormalization != "unix" && input.ClipboardNormalization != "windows" {
		return rdpOptions{}, fail("invalid_argument", "clipboard normalization is invalid")
	}
	if input.PerformanceProfile == "" {
		input.PerformanceProfile = "balanced"
	}
	if input.PerformanceProfile != "balanced" && input.PerformanceProfile != "quality" && input.PerformanceProfile != "low-bandwidth" && input.PerformanceProfile != "custom" {
		return rdpOptions{}, fail("invalid_argument", "RDP performance profile is invalid")
	}
	if len(input.TimeZone) > 128 || strings.ContainsAny(input.TimeZone, "\x00\r\n") {
		return rdpOptions{}, fail("invalid_argument", "RDP timezone is invalid")
	}
	if input.ServerLayout != "" && !validServerLayout(input.ServerLayout) {
		return rdpOptions{}, fail("invalid_argument", "RDP server keyboard layout is invalid")
	}
	if input.Copy == nil {
		value := true
		input.Copy = &value
	}
	if input.Paste == nil {
		value := true
		input.Paste = &value
	}
	if input.Clipboard != nil {
		value := *input.Clipboard
		input.Copy, input.Paste = &value, &value
	}
	return input, nil
}

func validServerLayout(value string) bool {
	switch value {
	case "en-us-qwerty", "en-gb-qwerty", "de-de-qwertz", "de-ch-qwertz", "fr-fr-azerty", "fr-be-azerty", "fr-ch-qwertz", "hu-hu-qwertz", "it-it-qwerty", "es-es-qwerty", "es-latam-qwerty", "sv-se-qwerty", "no-no-qwerty", "tr-tr-qwerty", "pt-br-qwerty", "ja-jp-qwerty", "failsafe":
		return true
	}
	return false
}

func validHost(host string) bool {
	if host == "" || len(host) > 253 || strings.TrimSpace(host) != host {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.ContainsAny(host, ":/?#@[]\\\x00") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
		}
	}
	return true
}

func (p *plugin) negotiate(stream *guacStream, options rdpOptions, identity credentials, client clientInfo, stage func(string)) (string, *rpcError) {
	if client.Width == 0 {
		client.Width = 1280
	}
	if client.Height == 0 {
		client.Height = 800
	}
	if client.DPI == 0 {
		client.DPI = 96
	}
	if client.Width < 1 || client.Width > 8192 || client.Height < 1 || client.Height > 8192 || client.DPI < 1 || client.DPI > 240 || len(client.Name) > 128 {
		return "", fail("invalid_argument", "RDP client dimensions or name are invalid")
	}
	if stage != nil {
		stage(fmt.Sprintf("negotiating desktop size %dx%d at %d DPI", client.Width, client.Height, client.DPI))
	}
	for _, values := range [][]string{client.AudioMimetypes, client.VideoMimetypes, client.ImageMimetypes} {
		if len(values) > 32 {
			return "", fail("invalid_argument", "too many RDP client media types")
		}
		for _, value := range values {
			if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
				return "", fail("invalid_argument", "RDP client media type is invalid")
			}
		}
	}
	if failure := stream.writeInstruction("select", "rdp"); failure != nil {
		return "", failure
	}
	if stage != nil {
		stage("sent select rdp")
	}
	args, failure := stream.readInstruction()
	if failure != nil {
		return "", failure
	}
	if args.Opcode != "args" {
		return "", fail("failed", "guacd did not accept RDP negotiation")
	}
	if stage != nil {
		stage(fmt.Sprintf("received args (%d parameters)", len(args.Args)))
	}
	values := connectionValues(options, identity)
	connect := make([]string, len(args.Args))
	defer func() {
		values["password"] = ""
		for index := range connect {
			connect[index] = ""
		}
	}()
	version := protocolVersion{major: 1}
	for index, name := range args.Args {
		if index == 0 {
			if advertised, ok := parseProtocolVersion(name); ok {
				version = advertised
				if version.atLeast(protocolVersion{major: 1, minor: 5}) {
					version = protocolVersion{major: 1, minor: 5}
				}
				connect[index] = version.String()
				if stage != nil {
					stage("negotiated protocol version " + version.String())
				}
				continue
			}
		}
		connect[index] = values[name]
	}
	for _, instruction := range []struct {
		opcode string
		args   []string
	}{
		{"size", []string{strconv.Itoa(client.Width), strconv.Itoa(client.Height), strconv.Itoa(client.DPI)}},
		{"audio", client.AudioMimetypes},
		{"video", client.VideoMimetypes},
		{"image", client.ImageMimetypes},
	} {
		if failure := stream.writeInstruction(instruction.opcode, instruction.args...); failure != nil {
			return "", failure
		}
		if stage != nil {
			stage("sent " + instruction.opcode)
		}
	}
	timeZone := options.TimeZone
	if timeZone == "" {
		timeZone = client.TimeZone
	}
	if version.atLeast(protocolVersion{major: 1, minor: 1}) && timeZone != "" {
		if failure := stream.writeInstruction("timezone", timeZone); failure != nil {
			return "", failure
		}
		if stage != nil {
			stage("sent timezone")
		}
	}
	if version.atLeast(protocolVersion{major: 1, minor: 5}) && client.Name != "" {
		if failure := stream.writeInstruction("name", client.Name); failure != nil {
			return "", failure
		}
		if stage != nil {
			stage("sent client name")
		}
	}
	if failure := stream.writeInstruction("connect", connect...); failure != nil {
		return "", failure
	}
	if stage != nil {
		stage("sent connect instruction")
	}
	values["password"] = ""
	for index := range connect {
		connect[index] = ""
	}
	ready, failure := stream.readInstruction()
	if failure != nil || ready.Opcode != "ready" || len(ready.Args) == 0 || ready.Args[0] == "" {
		return "", fail("failed", "guacd did not establish the RDP session")
	}
	if stage != nil {
		stage("received ready")
	}
	return ready.Args[0], nil
}

type protocolVersion struct{ major, minor, patch int }

func (v protocolVersion) String() string {
	return fmt.Sprintf("VERSION_%d_%d_%d", v.major, v.minor, v.patch)
}

func (v protocolVersion) atLeast(other protocolVersion) bool {
	if v.major != other.major {
		return v.major > other.major
	}
	if v.minor != other.minor {
		return v.minor > other.minor
	}
	return v.patch >= other.patch
}

func parseProtocolVersion(value string) (protocolVersion, bool) {
	if !strings.HasPrefix(value, "VERSION_") {
		return protocolVersion{}, false
	}
	parts := strings.Split(strings.TrimPrefix(value, "VERSION_"), "_")
	if len(parts) != 3 {
		return protocolVersion{}, false
	}
	values := [3]int{}
	for i, part := range parts {
		if part == "" {
			return protocolVersion{}, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return protocolVersion{}, false
			}
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return protocolVersion{}, false
		}
		values[i] = value
	}
	return protocolVersion{major: values[0], minor: values[1], patch: values[2]}, true
}

func connectionValues(options rdpOptions, identity credentials) map[string]string {
	security := options.SecurityMode
	if security == "automatic" {
		security = "any"
	}
	values := map[string]string{
		"hostname": options.Host, "port": strconv.Itoa(options.Port), "username": identity.Username,
		"password": identity.Password, "domain": identity.Domain, "security": security,
		"timeout": strconv.Itoa(options.TimeoutSeconds), "enable-drive": "false", "enable-printing": "false",
		"disable-audio": "true", "disable-copy": strconv.FormatBool(!*options.Copy), "disable-paste": strconv.FormatBool(!*options.Paste),
		"normalize-clipboard": options.ClipboardNormalization,
	}
	if options.ServerLayout != "" {
		values["server-layout"] = options.ServerLayout
	}
	if options.ResizeMethod != "fixed" {
		values["resize-method"] = options.ResizeMethod
	}
	if options.ColorDepth != 0 {
		values["color-depth"] = strconv.Itoa(options.ColorDepth)
	}
	if options.TimeZone != "" {
		values["timezone"] = options.TimeZone
	}
	switch options.CertificatePolicy {
	case "tofu":
		values["cert-tofu"] = "true"
	case "ignore":
		values["ignore-cert"] = "true"
	case "fingerprint":
		values["cert-fingerprints"] = options.CertificateFingerprint
	}
	wallpaper, theming, smoothing, drag, composition, animations := "false", "false", "true", "false", "false", "false"
	if options.PerformanceProfile == "quality" {
		wallpaper, theming, drag, composition, animations = "true", "true", "true", "true", "true"
	} else if options.PerformanceProfile == "low-bandwidth" {
		smoothing = "false"
	}
	values["enable-wallpaper"], values["enable-theming"], values["enable-font-smoothing"] = wallpaper, theming, smoothing
	values["enable-full-window-drag"], values["enable-desktop-composition"], values["enable-menu-animations"] = drag, composition, animations
	return values
}

func (p *plugin) status(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		ID string `json:"id"`
	}
	if failure := decode(raw, &request); failure != nil || request.ID == "" {
		return nil, fail("invalid_argument", "session id is required")
	}
	session, ok := p.sessions[request.ID]
	if !ok {
		return nil, fail("not_found", "unknown RDP session")
	}
	return session, nil
}

func (p *plugin) close(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		ID string `json:"id"`
	}
	if failure := decode(raw, &request); failure != nil || request.ID == "" {
		return nil, fail("invalid_argument", "session id is required")
	}
	session, ok := p.sessions[request.ID]
	if !ok {
		return map[string]any{"ok": true}, nil
	}
	if err := callHost("network.stream.close", map[string]string{"id": session.StreamID}, nil); err != nil {
		if failure, ok := pluginapi.AsCapabilityError(err); !ok || failure.Code != "not_found" {
			return nil, hostError(err)
		}
	}
	p.addDiagnostic(session.DiagnosticID, "session closed")
	delete(p.sessions, request.ID)
	return map[string]any{"ok": true}, nil
}

func (p *plugin) event(name string, raw json.RawMessage) *rpcError {
	if name != "network.stream.closed" {
		return nil
	}
	var event struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &event) != nil || event.ID == "" {
		return fail("invalid_argument", "invalid network stream event")
	}
	if current, ok := p.sessions[event.ID]; ok {
		p.addDiagnostic(current.DiagnosticID, "network stream closed")
		delete(p.sessions, event.ID)
	}
	return nil
}

func (p *plugin) shutdown() {
	p.sessions = map[string]session{}
	p.diagnostics = map[string][]string{}
	p.diagnosticIDs = nil
	p.sessionDiagIDs = map[string]string{}
}

type guacInstruction struct {
	Opcode string
	Args   []string
}

type guacStream struct{ id string }

func (s *guacStream) readInstruction() (guacInstruction, *rpcError) {
	data := make([]byte, 0, guacReadWindow)
	for len(data) < maxGuacInstruction {
		chunk, eof, failure := s.readWindow(len(data), min(guacReadWindow, maxGuacInstruction-len(data)))
		if failure != nil {
			return guacInstruction{}, failure
		}
		if len(chunk) == 0 {
			return guacInstruction{}, fail("failed", "guacd closed during RDP negotiation")
		}
		data = append(data, chunk...)
		instruction, consumed, complete, parseFailure := parseGuacInstruction(data)
		if parseFailure != nil {
			return guacInstruction{}, parseFailure
		}
		if complete {
			if failure := s.consume(consumed); failure != nil {
				return guacInstruction{}, failure
			}
			return instruction, nil
		}
		if eof {
			return guacInstruction{}, fail("failed", "guacd closed during RDP negotiation")
		}
	}
	return guacInstruction{}, fail("failed", "guacamole instruction exceeds limit")
}

func (s *guacStream) readWindow(offset, maximum int) ([]byte, bool, *rpcError) {
	var response struct {
		Data string `json:"data"`
		EOF  bool   `json:"eof"`
	}
	if err := callHost("network.stream.read", map[string]any{"id": s.id, "maxBytes": maximum, "timeoutMilliseconds": 5000, "peek": true, "offset": offset}, &response); err != nil {
		return nil, false, hostError(err)
	}
	data, err := base64.StdEncoding.DecodeString(response.Data)
	if err != nil || len(data) > maximum {
		return nil, false, fail("failed", "invalid guacd stream response")
	}
	return data, response.EOF, nil
}

func (s *guacStream) consume(length int) *rpcError {
	for length > 0 {
		count := length
		if count > 64<<10 {
			count = 64 << 10
		}
		var response struct {
			Data string `json:"data"`
		}
		if err := callHost("network.stream.read", map[string]any{"id": s.id, "maxBytes": count, "timeoutMilliseconds": 5000}, &response); err != nil {
			return hostError(err)
		}
		data, err := base64.StdEncoding.DecodeString(response.Data)
		if err != nil || len(data) == 0 || len(data) > count {
			return fail("failed", "invalid guacd stream response")
		}
		length -= len(data)
	}
	return nil
}

func parseGuacInstruction(data []byte) (guacInstruction, int, bool, *rpcError) {
	values := make([]string, 0, 8)
	offset, total := 0, 0
	for {
		start := offset
		for offset < len(data) && data[offset] >= '0' && data[offset] <= '9' {
			if offset-start >= 8 {
				return guacInstruction{}, 0, false, fail("failed", "invalid guacamole element length")
			}
			offset++
		}
		if offset == len(data) {
			return guacInstruction{}, 0, false, nil
		}
		if offset == start || data[offset] != '.' {
			return guacInstruction{}, 0, false, fail("failed", "invalid guacamole element length")
		}
		length, err := strconv.Atoi(string(data[start:offset]))
		if err != nil || length > maxGuacElement {
			return guacInstruction{}, 0, false, fail("failed", "guacamole element exceeds limit")
		}
		offset++
		if len(data)-offset < length+1 {
			return guacInstruction{}, 0, false, nil
		}
		end := offset + length
		separator := data[end]
		if separator != ',' && separator != ';' {
			return guacInstruction{}, 0, false, fail("failed", "invalid guacamole instruction separator")
		}
		value := data[offset:end]
		if !utf8.Valid(value) {
			return guacInstruction{}, 0, false, fail("failed", "guacamole instruction is not UTF-8")
		}
		total += length
		if total > maxGuacInstruction || len(values) >= maxGuacArgs {
			return guacInstruction{}, 0, false, fail("failed", "guacamole instruction exceeds limit")
		}
		values = append(values, string(value))
		offset = end + 1
		if separator == ';' {
			if len(values) == 0 || values[0] == "" {
				return guacInstruction{}, 0, false, fail("failed", "guacamole opcode is missing")
			}
			return guacInstruction{Opcode: values[0], Args: values[1:]}, offset, true, nil
		}
	}
}

func (s *guacStream) writeInstruction(opcode string, args ...string) *rpcError {
	if opcode == "" || len(args)+1 > maxGuacArgs {
		return fail("invalid_argument", "invalid guacamole instruction")
	}
	var output strings.Builder
	for index, value := range append([]string{opcode}, args...) {
		if !utf8.ValidString(value) || len(value) > maxGuacElement {
			return fail("invalid_argument", "invalid guacamole element")
		}
		if index > 0 {
			output.WriteByte(',')
		}
		output.WriteString(strconv.Itoa(len(value)))
		output.WriteByte('.')
		output.WriteString(value)
	}
	output.WriteByte(';')
	data := []byte(output.String())
	for len(data) > 0 {
		count := len(data)
		if count > streamChunk {
			count = streamChunk
		}
		if err := callHost("network.stream.write", map[string]any{"id": s.id, "data": base64.StdEncoding.EncodeToString(data[:count])}, nil); err != nil {
			return hostError(err)
		}
		data = data[count:]
	}
	return nil
}
