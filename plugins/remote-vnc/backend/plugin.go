package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"unicode/utf8"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

var callHost = pluginapi.CallHost

const (
	maxTargets  = 256
	maxSessions = 8
	maxName     = 100
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

type target struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Host                  string `json:"host"`
	Port                  int    `json:"port"`
	Username              string `json:"username,omitempty"`
	ConnectTimeoutSeconds int    `json:"connectTimeoutSeconds"`
}
type targetStore struct {
	Version int      `json:"version"`
	NextID  uint64   `json:"nextId"`
	Targets []target `json:"targets"`
}
type session struct {
	ID         string `json:"id"`
	TargetID   string `json:"targetId"`
	TargetName string `json:"targetName"`
	State      string `json:"state"`
	StreamID   string `json:"streamId"`
}
type plugin struct{ sessions map[string]session }

func newPlugin() *plugin { return &plugin{sessions: map[string]session{}} }

func decode(raw json.RawMessage, out any) *rpcError {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return fail("invalid_argument", "invalid request")
	}
	return nil
}
func validHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " /\\\t\r\n") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func normalizeTarget(t target) (target, *rpcError) {
	t.Name = strings.TrimSpace(t.Name)
	t.Host = strings.TrimSpace(t.Host)
	t.Username = strings.TrimSpace(t.Username)
	if t.Name == "" || len(t.Name) > maxName || !utf8.ValidString(t.Name) {
		return target{}, fail("invalid_argument", "target name must contain 1 to 100 valid characters")
	}
	if !validHost(t.Host) {
		return target{}, fail("invalid_argument", "VNC host is invalid")
	}
	if t.Port == 0 {
		t.Port = 5900
	}
	if t.Port < 1 || t.Port > 65535 {
		return target{}, fail("invalid_argument", "VNC port is invalid")
	}
	if len(t.Username) > 256 || !utf8.ValidString(t.Username) {
		return target{}, fail("invalid_argument", "VNC username is invalid")
	}
	if t.ConnectTimeoutSeconds == 0 {
		t.ConnectTimeoutSeconds = 10
	}
	if t.ConnectTimeoutSeconds < 1 || t.ConnectTimeoutSeconds > 30 {
		return target{}, fail("invalid_argument", "VNC connect timeout must be between 1 and 30 seconds")
	}
	return t, nil
}
func (p *plugin) readTargets() (targetStore, *rpcError) {
	stored := targetStore{Version: 1, NextID: 1, Targets: []target{}}
	var raw json.RawMessage
	if err := callHost("storage.get", map[string]any{"key": "targets"}, &raw); err != nil {
		return stored, hostError(err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return stored, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&stored) != nil || stored.Version != 1 || stored.Targets == nil || len(stored.Targets) > maxTargets {
		return targetStore{}, fail("invalid_configuration", "stored VNC targets are unreadable")
	}
	if stored.NextID == 0 {
		stored.NextID = 1
	}
	seen := map[string]bool{}
	for i := range stored.Targets {
		item := &stored.Targets[i]
		normalized, failure := normalizeTarget(*item)
		if item.ID == "" || seen[item.ID] || failure != nil {
			return targetStore{}, fail("invalid_configuration", "stored VNC targets are invalid")
		}
		seen[item.ID] = true
		*item = normalized
	}
	return stored, nil
}
func (p *plugin) save(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Target target `json:"target"`
	}
	if f := decode(raw, &request); f != nil {
		return nil, f
	}
	t, f := normalizeTarget(request.Target)
	if f != nil {
		return nil, f
	}
	stored, f := p.readTargets()
	if f != nil {
		return nil, f
	}
	if t.ID == "" {
		if stored.NextID == ^uint64(0) {
			return nil, fail("resource_limit", "VNC target ID sequence is exhausted")
		}
		for {
			t.ID = fmt.Sprintf("vnc-%d", stored.NextID)
			stored.NextID++
			duplicate := false
			for _, old := range stored.Targets {
				if old.ID == t.ID {
					duplicate = true
					break
				}
			}
			if !duplicate {
				break
			}
			if stored.NextID == ^uint64(0) {
				return nil, fail("resource_limit", "VNC target ID sequence is exhausted")
			}
		}
		stored.Targets = append(stored.Targets, t)
	} else {
		found := false
		for i := range stored.Targets {
			if stored.Targets[i].ID == t.ID {
				stored.Targets[i] = t
				found = true
				break
			}
		}
		if !found {
			return nil, fail("not_found", "unknown VNC target")
		}
	}
	if len(stored.Targets) > maxTargets {
		return nil, fail("resource_limit", "VNC target limit reached")
	}
	if err := callHost("storage.set", map[string]any{"key": "targets", "value": stored}, nil); err != nil {
		return nil, hostError(err)
	}
	return map[string]any{"target": t}, nil
}
func (p *plugin) handle(method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "vnc.targets.list":
		stored, f := p.readTargets()
		if f != nil {
			return nil, f
		}
		return map[string]any{"targets": stored.Targets}, nil
	case "vnc.targets.get":
		var q struct {
			ID string `json:"id"`
		}
		if f := decode(raw, &q); f != nil || q.ID == "" {
			return nil, fail("invalid_argument", "target id is required")
		}
		stored, f := p.readTargets()
		if f != nil {
			return nil, f
		}
		for _, t := range stored.Targets {
			if t.ID == q.ID {
				return map[string]any{"target": t}, nil
			}
		}
		return nil, fail("not_found", "unknown VNC target")
	case "vnc.targets.save":
		return p.save(raw)
	case "vnc.targets.delete":
		var q struct {
			ID string `json:"id"`
		}
		if f := decode(raw, &q); f != nil || q.ID == "" {
			return nil, fail("invalid_argument", "target id is required")
		}
		for _, active := range p.sessions {
			if active.TargetID == q.ID {
				return nil, fail("failed_precondition", "target has an active session")
			}
		}
		stored, f := p.readTargets()
		if f != nil {
			return nil, f
		}
		for i, t := range stored.Targets {
			if t.ID == q.ID {
				stored.Targets = append(stored.Targets[:i], stored.Targets[i+1:]...)
				if err := callHost("storage.set", map[string]any{"key": "targets", "value": stored}, nil); err != nil {
					return nil, hostError(err)
				}
				return map[string]any{"ok": true}, nil
			}
		}
		return nil, fail("not_found", "unknown VNC target")
	case "vnc.session.open":
		var q struct {
			TargetID string `json:"targetId"`
		}
		if f := decode(raw, &q); f != nil {
			return nil, f
		}
		if q.TargetID == "" {
			return nil, fail("invalid_argument", "targetId is required")
		}
		if len(p.sessions) >= maxSessions {
			return nil, fail("resource_limit", "VNC session limit reached")
		}
		stored, f := p.readTargets()
		if f != nil {
			return nil, f
		}
		var selected *target
		for i := range stored.Targets {
			if stored.Targets[i].ID == q.TargetID {
				snapshot := stored.Targets[i]
				selected = &snapshot
				break
			}
		}
		if selected == nil {
			return nil, fail("not_found", "unknown VNC target")
		}
		var opened struct {
			ID string `json:"id"`
		}
		err := callHost("network.stream.open", map[string]any{"host": selected.Host, "port": selected.Port, "connectTimeoutSeconds": selected.ConnectTimeoutSeconds}, &opened)
		if err != nil {
			return nil, hostError(err)
		}
		if opened.ID == "" {
			return nil, fail("failed", "could not open VNC stream")
		}
		s := session{ID: opened.ID, TargetID: selected.ID, TargetName: selected.Name, State: "connected", StreamID: opened.ID}
		p.sessions[s.ID] = s
		return map[string]any{"session": s, "streamId": s.StreamID, "username": selected.Username}, nil
	case "vnc.session.status":
		var q struct {
			ID string `json:"id"`
		}
		if f := decode(raw, &q); f != nil || q.ID == "" {
			return nil, fail("invalid_argument", "session id is required")
		}
		s, ok := p.sessions[q.ID]
		if !ok {
			return nil, fail("not_found", "unknown VNC session")
		}
		return map[string]any{"session": s}, nil
	case "vnc.session.close":
		var q struct {
			ID string `json:"id"`
		}
		if f := decode(raw, &q); f != nil || q.ID == "" {
			return nil, fail("invalid_argument", "session id is required")
		}
		s, ok := p.sessions[q.ID]
		if !ok {
			return map[string]any{"ok": true}, nil
		}
		delete(p.sessions, q.ID)
		if err := callHost("network.stream.close", map[string]string{"id": s.StreamID}, nil); err != nil {
			return nil, hostError(err)
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, fail("unknown_method", "unknown VNC method")
	}
}
func (p *plugin) event(name string, raw json.RawMessage) *rpcError {
	if name != "network.stream.closed" {
		return nil
	}
	var event struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return fail("invalid_argument", "invalid network stream event")
	}
	delete(p.sessions, event.ID)
	return nil
}
func (p *plugin) shutdown() {
	for id, s := range p.sessions {
		_ = callHost("network.stream.close", map[string]string{"id": s.StreamID}, nil)
		delete(p.sessions, id)
	}
}
