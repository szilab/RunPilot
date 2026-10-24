package main

import (
	"encoding/json"
	"strings"
)

// Target status and Wake-on-LAN. Reachability, neighbor (ARP) lookups and
// the magic packet itself are generic host capabilities (network.probe,
// network.neighbor.lookup, network.wake); this file owns the Remote Desktop
// semantics: which targets to check, when to remember a MAC address and
// which address to wake.

const probeBatch = 64

// normalizeMAC accepts aa:bb:cc:dd:ee:ff, AA-BB-CC-DD-EE-FF, aabb.ccdd.eeff
// and aabbccddeeff and returns the lowercase colon form. Empty stays empty.
func normalizeMAC(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	hex := make([]byte, 0, 12)
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			hex = append(hex, c)
		case c >= 'A' && c <= 'F':
			hex = append(hex, c+'a'-'A')
		case c == ':' || c == '-' || c == '.':
		default:
			return "", false
		}
	}
	if len(hex) != 12 {
		return "", false
	}
	out := make([]byte, 0, 17)
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hex[i], hex[i+1])
	}
	if string(out) == "00:00:00:00:00:00" || string(out) == "ff:ff:ff:ff:ff:ff" {
		return "", false
	}
	return string(out), true
}

// discoveredFor keeps a learned MAC address only while the target keeps
// pointing at the same host.
func discoveredFor(previousHost, previousMAC, host string) string {
	if previousHost != host {
		return ""
	}
	return previousMAC
}

type targetRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type targetStatus struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	State string `json:"state"`
	// LatencyMilliseconds is the TCP connect time for online targets.
	LatencyMilliseconds int64 `json:"latencyMilliseconds,omitempty"`
}

type probeEndpoint struct {
	ref  targetRef
	host string
	port int
}

// targetStatuses probes every RDP and VNC target's connection port. States:
// online (port accepts connections), service-down (the host answered but the
// port is closed), offline (no answer or unknown name).
func (p *plugin) targetStatuses() (any, *rpcError) {
	rdp, failure := p.readTargets()
	if failure != nil {
		return nil, failure
	}
	vnc, failure := p.readVNCTargets()
	if failure != nil {
		return nil, failure
	}
	endpoints := make([]probeEndpoint, 0, len(rdp.Targets)+len(vnc.Targets))
	for _, target := range rdp.Targets {
		endpoints = append(endpoints, probeEndpoint{targetRef{"rdp", target.ID}, target.Options.Host, target.Options.Port})
	}
	for _, target := range vnc.Targets {
		endpoints = append(endpoints, probeEndpoint{targetRef{"vnc", target.ID}, target.Host, target.Port})
	}
	statuses := make([]targetStatus, 0, len(endpoints))
	for start := 0; start < len(endpoints); start += probeBatch {
		end := start + probeBatch
		if end > len(endpoints) {
			end = len(endpoints)
		}
		batch := endpoints[start:end]
		request := make([]map[string]any, len(batch))
		for i, endpoint := range batch {
			request[i] = map[string]any{"host": endpoint.host, "port": endpoint.port}
		}
		var response struct {
			Results []struct {
				State               string `json:"state"`
				LatencyMilliseconds int64  `json:"latencyMilliseconds"`
			} `json:"results"`
		}
		if err := callHost("network.probe", map[string]any{"targets": request, "timeoutMilliseconds": 1500}, &response); err != nil {
			return nil, hostError(err)
		}
		if len(response.Results) != len(batch) {
			return nil, fail("failed", "network probe returned an unexpected result count")
		}
		for i, result := range response.Results {
			status := targetStatus{Kind: batch[i].ref.Kind, ID: batch[i].ref.ID, State: "offline"}
			switch result.State {
			case "open":
				status.State, status.LatencyMilliseconds = "online", result.LatencyMilliseconds
			case "closed":
				status.State = "service-down"
			}
			statuses = append(statuses, status)
		}
	}
	p.learnMACAddresses(&rdp, &vnc, statuses)
	return map[string]any{"statuses": statuses}, nil
}

// learnMACAddresses remembers the MAC address of reachable targets that have
// none configured, so Wake-on-LAN still works once they are switched off.
// It never fails the status request.
func (p *plugin) learnMACAddresses(rdp *storedTargets, vnc *vncTargetStore, statuses []targetStatus) {
	reachable := map[targetRef]bool{}
	for _, status := range statuses {
		if status.State != "offline" {
			reachable[targetRef{status.Kind, status.ID}] = true
		}
	}
	lookup := func(host string) string {
		var response struct {
			MAC string `json:"mac"`
		}
		if callHost("network.neighbor.lookup", map[string]any{"host": host}, &response) != nil {
			return ""
		}
		mac, ok := normalizeMAC(response.MAC)
		if !ok {
			return ""
		}
		return mac
	}
	rdpChanged := false
	for i := range rdp.Targets {
		target := &rdp.Targets[i]
		if reachable[targetRef{"rdp", target.ID}] && target.MACAddress == "" {
			if mac := lookup(target.Options.Host); mac != "" && mac != target.DiscoveredMAC {
				target.DiscoveredMAC, rdpChanged = mac, true
			}
		}
	}
	if rdpChanged {
		_ = callHost("storage.set", map[string]any{"key": "targets", "value": rdp}, nil)
	}
	vncChanged := false
	for i := range vnc.Targets {
		target := &vnc.Targets[i]
		if reachable[targetRef{"vnc", target.ID}] && target.MACAddress == "" {
			if mac := lookup(target.Host); mac != "" && mac != target.DiscoveredMAC {
				target.DiscoveredMAC, vncChanged = mac, true
			}
		}
	}
	if vncChanged {
		_ = p.writeVNCTargets(*vnc)
	}
}

// wakeTarget sends a Wake-on-LAN packet to the configured MAC address, else
// to the one learned while the target was online, else to one looked up now.
func (p *plugin) wakeTarget(raw json.RawMessage) (any, *rpcError) {
	var request targetRef
	if failure := decode(raw, &request); failure != nil || request.ID == "" || (request.Kind != "rdp" && request.Kind != "vnc") {
		return nil, fail("invalid_argument", "kind (rdp or vnc) and id are required")
	}
	var name, host, configured, discovered string
	var remember func(string)
	switch request.Kind {
	case "rdp":
		stored, failure := p.readTargets()
		if failure != nil {
			return nil, failure
		}
		for i := range stored.Targets {
			if stored.Targets[i].ID == request.ID {
				target := &stored.Targets[i]
				name, host, configured, discovered = target.Name, target.Options.Host, target.MACAddress, target.DiscoveredMAC
				remember = func(mac string) {
					target.DiscoveredMAC = mac
					_ = callHost("storage.set", map[string]any{"key": "targets", "value": stored}, nil)
				}
			}
		}
	case "vnc":
		stored, failure := p.readVNCTargets()
		if failure != nil {
			return nil, failure
		}
		for i := range stored.Targets {
			if stored.Targets[i].ID == request.ID {
				target := &stored.Targets[i]
				name, host, configured, discovered = target.Name, target.Host, target.MACAddress, target.DiscoveredMAC
				remember = func(mac string) {
					target.DiscoveredMAC = mac
					_ = p.writeVNCTargets(stored)
				}
			}
		}
	}
	if remember == nil {
		return nil, fail("not_found", "unknown target")
	}
	mac, source := configured, "configured"
	if mac == "" && discovered != "" {
		mac, source = discovered, "discovered"
	}
	if mac == "" {
		var response struct {
			MAC string `json:"mac"`
		}
		if err := callHost("network.neighbor.lookup", map[string]any{"host": host, "refresh": true}, &response); err == nil {
			if found, ok := normalizeMAC(response.MAC); ok && found != "" {
				mac, source = found, "discovered"
				remember(found)
			}
		}
	}
	if mac == "" {
		return nil, fail("not_found", "No MAC address is known for "+name+". Enter it in the target settings; RunPilot can detect it only while the computer is on and on the local network.")
	}
	var sent struct {
		SentTo []string `json:"sentTo"`
	}
	if err := callHost("network.wake", map[string]any{"mac": mac}, &sent); err != nil {
		return nil, hostError(err)
	}
	return map[string]any{"mac": mac, "source": source, "sentTo": sent.SentTo}, nil
}
