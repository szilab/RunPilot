package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type fakeHost struct {
	storage               json.RawMessage
	opened                map[string]any
	opens, closes, writes int
	closeIDs              []string
}

func (h *fakeHost) call(method string, params any, result any) error {
	args, _ := params.(map[string]any)
	switch method {
	case "storage.get":
		if result != nil && h.storage != nil {
			return json.Unmarshal(h.storage, result)
		}
		return nil
	case "storage.set":
		h.writes++
		data, err := json.Marshal(args["value"])
		h.storage = data
		return err
	case "network.stream.open":
		h.opens++
		h.opened = args
		return json.Unmarshal([]byte(`{"id":"owned-stream"}`), result)
	case "network.stream.close":
		h.closes++
		id, _ := args["id"].(string)
		if id == "" {
			if values, ok := params.(map[string]string); ok {
				id = values["id"]
			}
		}
		h.closeIDs = append(h.closeIDs, id)
		return nil
	default:
		return fmt.Errorf("unexpected host call %s", method)
	}
}
func installHost(t *testing.T, host *fakeHost) {
	t.Helper()
	previous := callHost
	callHost = host.call
	t.Cleanup(func() { callHost = previous })
}
func call(t *testing.T, p *plugin, method, request string) (any, *rpcError) {
	t.Helper()
	return p.handle(method, json.RawMessage(request))
}

func TestTargetCRUDUsesOwnerStorageAndRejectsInvalidInput(t *testing.T) {
	host := &fakeHost{}
	installHost(t, host)
	p := newPlugin()
	result, f := call(t, p, "vnc.targets.save", `{"target":{"name":" Desk ","host":"vnc.example","username":"operator"}}`)
	if f != nil {
		t.Fatal(f)
	}
	saved := result.(map[string]any)["target"].(target)
	if saved.ID != "vnc-1" || saved.Name != "Desk" || saved.Port != 5900 || saved.ConnectTimeoutSeconds != 10 {
		t.Fatalf("normalized target=%+v", saved)
	}
	if host.writes != 1 || !strings.Contains(string(host.storage), `"operator"`) {
		t.Fatalf("target was not stored: writes=%d data=%s", host.writes, host.storage)
	}
	if _, f = call(t, p, "vnc.targets.save", `{"target":{"name":"bad","host":"127.0.0.1/evil"}}`); f == nil || f.Code != "invalid_argument" {
		t.Fatalf("invalid host accepted: %+v", f)
	}
	if _, f = call(t, p, "vnc.targets.save", `{"target":{"name":"bad","host":"localhost","port":70000}}`); f == nil {
		t.Fatal("invalid port accepted")
	}
	if _, f = call(t, p, "vnc.targets.list", `{}`); f != nil {
		t.Fatal(f)
	}
	if _, f = call(t, p, "vnc.targets.get", fmt.Sprintf(`{"id":%q}`, saved.ID)); f != nil {
		t.Fatal(f)
	}
	if _, f = call(t, p, "vnc.targets.delete", fmt.Sprintf(`{"id":%q}`, saved.ID)); f != nil {
		t.Fatal(f)
	}
}

func TestSessionUsesSnapshottedTargetRejectsDeleteAndCleansUp(t *testing.T) {
	host := &fakeHost{}
	installHost(t, host)
	p := newPlugin()
	result, f := call(t, p, "vnc.targets.save", `{"target":{"name":"First","host":"127.0.0.1","port":5901,"connectTimeoutSeconds":4}}`)
	if f != nil {
		t.Fatal(f)
	}
	targetID := result.(map[string]any)["target"].(target).ID
	result, f = call(t, p, "vnc.session.open", fmt.Sprintf(`{"targetId":%q}`, targetID))
	if f != nil {
		t.Fatal(f)
	}
	if host.opens != 1 || host.opened["host"] != "127.0.0.1" || host.opened["port"] != 5901 || host.opened["connectTimeoutSeconds"] != 4 {
		t.Fatalf("opened stream params=%#v", host.opened)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(strings.ToLower(string(encoded)), "password") {
		t.Fatalf("session response contains credential field: %s", encoded)
	}
	if _, f = call(t, p, "vnc.targets.delete", fmt.Sprintf(`{"id":%q}`, targetID)); f == nil || f.Code != "failed_precondition" {
		t.Fatalf("active target delete accepted: %+v", f)
	}
	if err := p.event("network.stream.closed", json.RawMessage(`{"id":"owned-stream"}`)); err != nil || len(p.sessions) != 0 {
		t.Fatalf("stream close event cleanup sessions=%v err=%v", p.sessions, err)
	}
	call(t, p, "vnc.session.close", `{"id":"owned-stream"}`)
	if host.closes != 0 {
		t.Fatalf("already-closed stream closed again: %d", host.closes)
	}
	result, f = call(t, p, "vnc.session.open", fmt.Sprintf(`{"targetId":%q}`, targetID))
	if f != nil {
		t.Fatal(f)
	}
	_, f = call(t, p, "vnc.session.close", `{"id":"owned-stream"}`)
	if f != nil || host.closes != 1 {
		t.Fatalf("close cleanup closes=%d failure=%+v", host.closes, f)
	}
	_, f = call(t, p, "vnc.session.status", `{"id":"owned-stream"}`)
	if f == nil || f.Code != "not_found" {
		t.Fatalf("closed status=%+v", f)
	}
}

func TestShutdownClosesStreamsAndSecretsAreNeverStored(t *testing.T) {
	host := &fakeHost{}
	installHost(t, host)
	p := newPlugin()
	result, f := call(t, p, "vnc.targets.save", `{"target":{"name":"Secret check","host":"localhost"}}`)
	if f != nil {
		t.Fatal(f)
	}
	id := result.(map[string]any)["target"].(target).ID
	_, f = call(t, p, "vnc.session.open", fmt.Sprintf(`{"targetId":%q,"password":"secret"}`, id))
	if f == nil {
		t.Fatal("unexpected unknown password field accepted")
	}
	call(t, p, "vnc.session.open", fmt.Sprintf(`{"targetId":%q}`, id))
	p.shutdown()
	if host.closes != 1 || len(p.sessions) != 0 || strings.Contains(string(host.storage), "secret") || strings.Contains(string(host.storage), "password") {
		t.Fatalf("shutdown or storage leaked: closes=%d sessions=%v storage=%s", host.closes, p.sessions, host.storage)
	}
}
