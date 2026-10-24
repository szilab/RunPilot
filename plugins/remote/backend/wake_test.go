package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeMAC(t *testing.T) {
	for input, want := range map[string]string{
		"":                  "",
		"AA:BB:CC:DD:EE:FF": "aa:bb:cc:dd:ee:ff",
		"aa-bb-cc-dd-ee-ff": "aa:bb:cc:dd:ee:ff",
		"aabb.ccdd.eeff":    "aa:bb:cc:dd:ee:ff",
		" 3C7C3F123456 ":    "3c:7c:3f:12:34:56",
	} {
		if got, ok := normalizeMAC(input); !ok || got != want {
			t.Errorf("normalizeMAC(%q) = %q, %v", input, got, ok)
		}
	}
	for _, input := range []string{"aa:bb:cc", "aa:bb:cc:dd:ee:ff:00", "zz:bb:cc:dd:ee:ff", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff"} {
		if _, ok := normalizeMAC(input); ok {
			t.Errorf("normalizeMAC(%q) accepted", input)
		}
	}
}

// networkHost fakes storage and the network tool capabilities.
type networkHost struct {
	storage  map[string]json.RawMessage
	states   map[string]string // "host:port" -> probe state
	arp      map[string]string // host -> MAC
	wakes    []string
	lookups  []string
	refreshs int
}

func (h *networkHost) call(method string, params any, result any) error {
	args := map[string]any{}
	raw, _ := json.Marshal(params)
	_ = json.Unmarshal(raw, &args)
	reply := func(value any) error {
		encoded, _ := json.Marshal(value)
		return json.Unmarshal(encoded, result)
	}
	switch method {
	case "storage.get":
		value := h.storage[args["key"].(string)]
		if value == nil {
			value = json.RawMessage("null")
		}
		return json.Unmarshal(value, result)
	case "storage.set":
		encoded, _ := json.Marshal(args["value"])
		h.storage[args["key"].(string)] = encoded
		return nil
	case "network.probe":
		var results []map[string]any
		for _, item := range args["targets"].([]any) {
			target := item.(map[string]any)
			state := h.states[fmt.Sprintf("%s:%v", target["host"], target["port"])]
			if state == "" {
				state = "unreachable"
			}
			results = append(results, map[string]any{"state": state, "latencyMilliseconds": 3})
		}
		return reply(map[string]any{"results": results})
	case "network.neighbor.lookup":
		host := args["host"].(string)
		h.lookups = append(h.lookups, host)
		if refresh, _ := args["refresh"].(bool); refresh {
			h.refreshs++
		}
		if mac := h.arp[host]; mac != "" {
			return reply(map[string]string{"ip": host, "mac": mac})
		}
		return fmt.Errorf("not_found")
	case "network.wake":
		h.wakes = append(h.wakes, args["mac"].(string))
		return reply(map[string]any{"sentTo": []string{"255.255.255.255"}})
	}
	return fmt.Errorf("unexpected capability %s", method)
}

func newNetworkHost(t *testing.T) *networkHost {
	t.Helper()
	host := &networkHost{storage: map[string]json.RawMessage{}, states: map[string]string{}, arp: map[string]string{}}
	host.storage["targets"] = json.RawMessage(`{"version":1,"nextId":4,"targets":[
		{"id":"rdp-1","name":"Desk","options":{"host":"192.0.2.20","port":3389}},
		{"id":"rdp-2","name":"Laptop","options":{"host":"192.0.2.21"},"macAddress":"AA-BB-CC-00-00-02"},
		{"id":"rdp-3","name":"Gone","options":{"host":"192.0.2.22"}}]}`)
	host.storage["vncTargets"] = json.RawMessage(`{"version":1,"nextId":2,"targets":[{"id":"vnc-1","name":"Pi","host":"192.0.2.30","port":5900,"connectTimeoutSeconds":10}]}`)
	original := callHost
	callHost = host.call
	t.Cleanup(func() { callHost = original })
	return host
}

func TestTargetStatusesAndMACLearning(t *testing.T) {
	host := newNetworkHost(t)
	host.states["192.0.2.20:3389"] = "open"
	host.states["192.0.2.21:3389"] = "open"
	host.states["192.0.2.30:5900"] = "closed"
	host.arp["192.0.2.20"] = "3C:7C:3F:12:34:56"
	host.arp["192.0.2.21"] = "11:11:11:11:11:11"
	host.arp["192.0.2.30"] = "dc:a6:32:00:00:01"
	p := newPlugin()
	out, failure := p.handle("remote.targets.status", nil)
	if failure != nil {
		t.Fatal(failure.Message)
	}
	encoded, _ := json.Marshal(out)
	for _, want := range []string{`{"kind":"rdp","id":"rdp-1","state":"online","latencyMilliseconds":3}`, `{"kind":"rdp","id":"rdp-3","state":"offline"}`, `{"kind":"vnc","id":"vnc-1","state":"service-down"}`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("statuses %s miss %s", encoded, want)
		}
	}
	// Reachable targets without a configured MAC learn one; configured ones do not look up.
	if !strings.Contains(string(host.storage["targets"]), `"discoveredMac":"3c:7c:3f:12:34:56"`) || strings.Contains(string(host.storage["targets"]), "11:11:11:11:11:11") {
		t.Fatalf("RDP store = %s", host.storage["targets"])
	}
	if !strings.Contains(string(host.storage["vncTargets"]), `"discoveredMac":"dc:a6:32:00:00:01"`) {
		t.Fatalf("VNC store = %s", host.storage["vncTargets"])
	}
	if strings.Join(host.lookups, ",") != "192.0.2.20,192.0.2.30" {
		t.Fatalf("lookups = %v", host.lookups)
	}
}

func TestWakeTargetPrefersConfiguredThenDiscoveredThenLookup(t *testing.T) {
	host := newNetworkHost(t)
	p := newPlugin()
	wake := func(kind, id string) (map[string]any, *rpcError) {
		out, failure := p.handle("remote.targets.wake", json.RawMessage(`{"kind":"`+kind+`","id":"`+id+`"}`))
		if failure != nil {
			return nil, failure
		}
		return out.(map[string]any), nil
	}
	if out, failure := wake("rdp", "rdp-2"); failure != nil || out["mac"] != "aa:bb:cc:00:00:02" || out["source"] != "configured" {
		t.Fatalf("configured wake = %v %v", out, failure)
	}
	if _, failure := wake("rdp", "rdp-3"); failure == nil || failure.Code != "not_found" || !strings.Contains(failure.Message, "Gone") {
		t.Fatalf("unknown MAC wake = %v", failure)
	}
	if host.refreshs != 1 {
		t.Fatalf("lookup did not request an ARP refresh: %d", host.refreshs)
	}
	host.arp["192.0.2.22"] = "00:11:22:33:44:55"
	if out, failure := wake("rdp", "rdp-3"); failure != nil || out["mac"] != "00:11:22:33:44:55" || out["source"] != "discovered" {
		t.Fatalf("lookup wake = %v %v", out, failure)
	}
	delete(host.arp, "192.0.2.22")
	if out, failure := wake("rdp", "rdp-3"); failure != nil || out["mac"] != "00:11:22:33:44:55" {
		t.Fatalf("remembered MAC was not reused: %v %v", out, failure)
	}
	if strings.Join(host.wakes, ",") != "aa:bb:cc:00:00:02,00:11:22:33:44:55,00:11:22:33:44:55" {
		t.Fatalf("wakes = %v", host.wakes)
	}
	if _, failure := wake("ssh", "x"); failure == nil || failure.Code != "invalid_argument" {
		t.Fatalf("invalid kind = %v", failure)
	}
}

func TestSavingTargetsControlsMACFields(t *testing.T) {
	host := newNetworkHost(t)
	host.storage["targets"] = json.RawMessage(`{"version":1,"nextId":2,"targets":[{"id":"rdp-1","name":"Desk","options":{"host":"192.0.2.20","port":3389},"discoveredMac":"3c:7c:3f:12:34:56"}]}`)
	p := newPlugin()
	save := func(body string) *rpcError {
		_, failure := p.handle("rdp.targets.save", json.RawMessage(body))
		return failure
	}
	if failure := save(`{"target":{"id":"rdp-1","name":"Desk","options":{"host":"192.0.2.20","port":3389},"discoveredMac":"99:99:99:99:99:99"}}`); failure != nil {
		t.Fatal(failure.Message)
	}
	if stored := string(host.storage["targets"]); !strings.Contains(stored, "3c:7c:3f:12:34:56") || strings.Contains(stored, "99:99") {
		t.Fatalf("client changed the learned MAC: %s", stored)
	}
	if failure := save(`{"target":{"id":"rdp-1","name":"Desk","options":{"host":"192.0.2.99","port":3389},"macAddress":"AABB.CCDD.EEFF"}}`); failure != nil {
		t.Fatal(failure.Message)
	}
	if stored := string(host.storage["targets"]); strings.Contains(stored, "discoveredMac") || !strings.Contains(stored, `"macAddress":"aa:bb:cc:dd:ee:ff"`) {
		t.Fatalf("host change kept the learned MAC: %s", stored)
	}
	if failure := save(`{"target":{"name":"New","options":{"host":"192.0.2.50"},"macAddress":"nope"}}`); failure == nil || failure.Code != "invalid_argument" {
		t.Fatalf("invalid MAC = %v", failure)
	}
	_, failure := p.handle("vnc.targets.save", json.RawMessage(`{"target":{"id":"vnc-1","name":"Pi","host":"192.0.2.30","port":5900,"discoveredMac":"99:99:99:99:99:99","macAddress":"dc-a6-32-00-00-01"}}`))
	if failure != nil {
		t.Fatal(failure.Message)
	}
	if stored := string(host.storage["vncTargets"]); strings.Contains(stored, "99:99") || !strings.Contains(stored, `"macAddress":"dc:a6:32:00:00:01"`) {
		t.Fatalf("VNC store = %s", stored)
	}
}
