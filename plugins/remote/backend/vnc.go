package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxVNCClients = 8
	maxVNCTargets = 256
)

type vncTarget struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Host                  string `json:"host"`
	Port                  int    `json:"port"`
	Username              string `json:"username,omitempty"`
	ConnectTimeoutSeconds int    `json:"connectTimeoutSeconds"`
	MACAddress            string `json:"macAddress,omitempty"`
	DiscoveredMAC         string `json:"discoveredMac,omitempty"`
}

type vncTargetStore struct {
	Version int         `json:"version"`
	NextID  uint64      `json:"nextId"`
	Targets []vncTarget `json:"targets"`
}

type vncSession struct {
	ID         string `json:"id"`
	TargetID   string `json:"targetId"`
	TargetName string `json:"targetName"`
	State      string `json:"state"`
	StreamID   string `json:"streamId"`
}

func normalizeVNCTarget(target vncTarget) (vncTarget, *rpcError) {
	target.Name = strings.TrimSpace(target.Name)
	target.Host = strings.TrimSpace(target.Host)
	target.Username = strings.TrimSpace(target.Username)
	if target.Name == "" || len(target.Name) > 100 || !utf8.ValidString(target.Name) {
		return vncTarget{}, fail("invalid_argument", "target name must contain 1 to 100 valid characters")
	}
	if !validHost(target.Host) {
		return vncTarget{}, fail("invalid_argument", "VNC host is invalid")
	}
	if target.Port == 0 {
		target.Port = 5900
	}
	if target.Port < 1 || target.Port > 65535 {
		return vncTarget{}, fail("invalid_argument", "VNC port is invalid")
	}
	if len(target.Username) > 256 || !utf8.ValidString(target.Username) {
		return vncTarget{}, fail("invalid_argument", "VNC username is invalid")
	}
	if target.ConnectTimeoutSeconds == 0 {
		target.ConnectTimeoutSeconds = 10
	}
	if target.ConnectTimeoutSeconds < 1 || target.ConnectTimeoutSeconds > 30 {
		return vncTarget{}, fail("invalid_argument", "VNC connect timeout must be between 1 and 30 seconds")
	}
	mac, macOK := normalizeMAC(target.MACAddress)
	discovered, discoveredOK := normalizeMAC(target.DiscoveredMAC)
	if !macOK || !discoveredOK {
		return vncTarget{}, fail("invalid_argument", "MAC address must look like aa:bb:cc:dd:ee:ff")
	}
	target.MACAddress, target.DiscoveredMAC = mac, discovered
	return target, nil
}

func (p *plugin) readVNCTargets() (vncTargetStore, *rpcError) {
	stored := vncTargetStore{Version: 1, NextID: 1, Targets: []vncTarget{}}
	var raw json.RawMessage
	if err := callHost("storage.get", map[string]any{"key": "vncTargets"}, &raw); err != nil {
		return stored, hostError(err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return stored, nil
	}
	var decoded vncTargetStore
	if failure := decode(raw, &decoded); failure != nil || decoded.Version != 1 || decoded.Targets == nil || len(decoded.Targets) > maxVNCTargets {
		return vncTargetStore{}, fail("invalid_configuration", "stored VNC targets are unreadable")
	}
	if decoded.NextID == 0 {
		decoded.NextID = 1
	}
	seen := make(map[string]bool, len(decoded.Targets))
	for index := range decoded.Targets {
		target := &decoded.Targets[index]
		normalized, failure := normalizeVNCTarget(*target)
		if target.ID == "" || len(target.ID) > 100 || seen[target.ID] || failure != nil {
			return vncTargetStore{}, fail("invalid_configuration", "stored VNC targets are invalid")
		}
		seen[target.ID] = true
		*target = normalized
	}
	return decoded, nil
}

func (p *plugin) writeVNCTargets(stored vncTargetStore) *rpcError {
	if err := callHost("storage.set", map[string]any{"key": "vncTargets", "value": stored}, nil); err != nil {
		return hostError(err)
	}
	return nil
}

func (p *plugin) nextVNCTargetID(stored *vncTargetStore) string {
	for {
		id := fmt.Sprintf("vnc-%d", stored.NextID)
		stored.NextID++
		if stored.NextID == 0 {
			stored.NextID = 1
		}
		found := false
		for _, target := range stored.Targets {
			if target.ID == id {
				found = true
				break
			}
		}
		if !found {
			return id
		}
	}
}

func (p *plugin) saveVNCTarget(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Target vncTarget `json:"target"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	request.Target.DiscoveredMAC = "" // learned by the plugin, never set by clients
	target, failure := normalizeVNCTarget(request.Target)
	if failure != nil {
		return nil, failure
	}
	stored, failure := p.readVNCTargets()
	if failure != nil {
		return nil, failure
	}
	if target.ID == "" {
		if len(stored.Targets) >= maxVNCTargets {
			return nil, fail("resource_limit", "VNC target limit reached")
		}
		target.ID = p.nextVNCTargetID(&stored)
		stored.Targets = append(stored.Targets, target)
	} else {
		found := false
		for index := range stored.Targets {
			if stored.Targets[index].ID == target.ID {
				previous := stored.Targets[index]
				target.DiscoveredMAC = discoveredFor(previous.Host, previous.DiscoveredMAC, target.Host)
				stored.Targets[index] = target
				found = true
				break
			}
		}
		if !found {
			return nil, fail("not_found", "unknown VNC target")
		}
	}
	if failure := p.writeVNCTargets(stored); failure != nil {
		return nil, failure
	}
	return map[string]any{"target": target}, nil
}

func (p *plugin) handleVNC(method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "vnc.targets.list":
		stored, failure := p.readVNCTargets()
		if failure != nil {
			return nil, failure
		}
		return map[string]any{"targets": stored.Targets}, nil
	case "vnc.targets.get":
		var request struct {
			ID string `json:"id"`
		}
		if failure := decode(raw, &request); failure != nil || request.ID == "" {
			return nil, fail("invalid_argument", "target id is required")
		}
		stored, failure := p.readVNCTargets()
		if failure != nil {
			return nil, failure
		}
		for _, target := range stored.Targets {
			if target.ID == request.ID {
				return map[string]any{"target": target}, nil
			}
		}
		return nil, fail("not_found", "unknown VNC target")
	case "vnc.targets.save":
		return p.saveVNCTarget(raw)
	case "vnc.targets.delete":
		var request struct {
			ID string `json:"id"`
		}
		if failure := decode(raw, &request); failure != nil || request.ID == "" {
			return nil, fail("invalid_argument", "target id is required")
		}
		for _, active := range p.vncSessions {
			if active.TargetID == request.ID {
				return nil, fail("failed_precondition", "target has an active session")
			}
		}
		stored, failure := p.readVNCTargets()
		if failure != nil {
			return nil, failure
		}
		for index, target := range stored.Targets {
			if target.ID == request.ID {
				stored.Targets = append(stored.Targets[:index], stored.Targets[index+1:]...)
				if failure := p.writeVNCTargets(stored); failure != nil {
					return nil, failure
				}
				return map[string]any{"ok": true}, nil
			}
		}
		return nil, fail("not_found", "unknown VNC target")
	case "vnc.session.open":
		var request struct {
			TargetID string `json:"targetId"`
		}
		if failure := decode(raw, &request); failure != nil || request.TargetID == "" {
			return nil, fail("invalid_argument", "targetId is required")
		}
		if len(p.vncSessions) >= maxVNCClients {
			return nil, fail("resource_limit", "VNC session limit reached")
		}
		stored, failure := p.readVNCTargets()
		if failure != nil {
			return nil, failure
		}
		var selected *vncTarget
		for index := range stored.Targets {
			if stored.Targets[index].ID == request.TargetID {
				copy := stored.Targets[index]
				selected = &copy
				break
			}
		}
		if selected == nil {
			return nil, fail("not_found", "unknown VNC target")
		}
		var opened struct {
			ID string `json:"id"`
		}
		if err := callHost("network.stream.open", map[string]any{"host": selected.Host, "port": selected.Port, "connectTimeoutSeconds": selected.ConnectTimeoutSeconds}, &opened); err != nil {
			return nil, hostError(err)
		}
		if opened.ID == "" {
			return nil, fail("failed", "could not open VNC stream")
		}
		session := vncSession{ID: opened.ID, TargetID: selected.ID, TargetName: selected.Name, State: "connected", StreamID: opened.ID}
		p.vncSessions[session.ID] = session
		return map[string]any{"session": session, "streamId": session.StreamID, "username": selected.Username}, nil
	case "vnc.session.status":
		var request struct {
			ID string `json:"id"`
		}
		if failure := decode(raw, &request); failure != nil || request.ID == "" {
			return nil, fail("invalid_argument", "session id is required")
		}
		session, ok := p.vncSessions[request.ID]
		if !ok {
			return nil, fail("not_found", "unknown VNC session")
		}
		return map[string]any{"session": session}, nil
	case "vnc.session.close":
		var request struct {
			ID string `json:"id"`
		}
		if failure := decode(raw, &request); failure != nil || request.ID == "" {
			return nil, fail("invalid_argument", "session id is required")
		}
		session, ok := p.vncSessions[request.ID]
		if !ok {
			return map[string]any{"ok": true}, nil
		}
		if err := callHost("network.stream.close", map[string]string{"id": session.StreamID}, nil); err != nil {
			return nil, hostError(err)
		}
		delete(p.vncSessions, request.ID)
		return map[string]any{"ok": true}, nil
	default:
		return nil, fail("unknown_method", "unknown VNC method")
	}
}
