package core

import (
	"encoding/json"
	"testing"
	"time"
)

func browserCall(t *testing.T, m *browserPublications, owner, base, method string, params any) map[string]string {
	t.Helper()
	raw, _ := json.Marshal(params)
	result, err := m.call(owner, base, method, raw)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if json.Unmarshal(result, &out) != nil {
		t.Fatal(string(result))
	}
	return out
}
func TestBrowserPublicationTicketOwnershipAndLifecycle(t *testing.T) {
	m := newBrowserPublications(nil)
	defer m.stop("")
	p := browserCall(t, m, "one", "/multi/path", "browser.publication.register", map[string]string{"mountPath": "/app", "bootstrap": "web/bootstrap.html", "worker": "web/sw.js"})
	raw := json.RawMessage(`{"mountPath":"/app/sub","bootstrap":"web/bootstrap.html","worker":"web/sw.js"}`)
	if _, err := m.call("other", "/", "browser.publication.register", raw); err == nil {
		t.Fatal("mount collision accepted")
	}
	g := browserCall(t, m, "one", "/multi/path", "http.gateway.open", map[string]string{"publicationId": p["id"], "upstreamURL": "http://localhost:1234", "upstreamBasePath": "/app"})
	if g["publicPrefix"] != "/multi/path/app" {
		t.Fatal(g)
	}
	if _, _, err := m.attach("other", g["id"]); err == nil {
		t.Fatal("foreign plugin attached")
	}
	ticket := browserCall(t, m, "one", "/", "browser.stream.ticket", map[string]string{"streamId": g["id"]})["ticket"]
	grant, ok := m.consume(ticket)
	if !ok || grant.Owner != "one" || grant.StreamID != g["id"] {
		t.Fatal(grant, ok)
	}
	if _, ok = m.consume(ticket); ok {
		t.Fatal("ticket reused")
	}
	expired := browserCall(t, m, "one", "/", "browser.stream.ticket", map[string]string{"streamId": g["id"]})["ticket"]
	m.mu.Lock()
	v := m.tickets[expired]
	v.expires = time.Now().Add(-time.Second)
	m.tickets[expired] = v
	m.mu.Unlock()
	if _, ok = m.consume(expired); ok {
		t.Fatal("expired ticket accepted")
	}
	conn, release, err := m.attach("one", g["id"])
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, _, err = m.attach("one", g["id"]); err == nil {
		t.Fatal("duplicate attachment")
	}
	m.stop("one")
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("shutdown stream leaked")
	}
	if len(m.gateways) != 0 || len(m.mounts) != 0 || len(m.tickets) != 0 {
		t.Fatal("runtime registrations leaked")
	}
}

func TestBrowserRuntimeExclusiveOwnershipAndRemoval(t *testing.T) {
	m := newBrowserPublications(nil)
	defer m.stop("")
	runtime := browserCall(t, m, "one", "/p", "browser.runtime.register", map[string]string{"bootstrap": "web/bootstrap.js", "worker": "web/sw.js"})
	same := browserCall(t, m, "one", "/p", "browser.runtime.register", map[string]string{"bootstrap": "web/bootstrap.js", "worker": "web/sw.js"})
	if same["id"] != runtime["id"] {
		t.Fatal("runtime registration is not idempotent")
	}
	for _, attempt := range []struct{ owner, method, raw string }{
		{"other", "browser.runtime.register", `{"bootstrap":"web/bootstrap.js","worker":"web/sw.js"}`},
		{"one", "browser.runtime.register", `{"bootstrap":"web/bootstrap.js","worker":"web/different.js"}`},
		{"one", "browser.runtime.register", `{"bootstrap":"web/bootstrap.js","worker":"web/sw.js","scope":"/"}`},
		{"one", "browser.runtime.register", `{"bootstrap":"https://evil/bootstrap.js","worker":"web/sw.js"}`},
		{"other", "browser.runtime.remove", `{"id":"` + runtime["id"] + `"}`},
		{"other", "browser.publication.register", `{"mountPath":"/other","bootstrap":"web/bootstrap.html","runtimeId":"` + runtime["id"] + `"}`},
	} {
		if _, err := m.call(attempt.owner, "/p", attempt.method, json.RawMessage(attempt.raw)); err == nil {
			t.Fatal("invalid runtime operation accepted", attempt.owner, attempt.method)
		}
	}
	publication := browserCall(t, m, "one", "/p", "browser.publication.register", map[string]string{"mountPath": "/app", "bootstrap": "web/bootstrap.html", "runtimeId": runtime["id"]})
	g := browserCall(t, m, "one", "/p", "http.gateway.open", map[string]string{"publicationId": publication["id"], "upstreamURL": "http://localhost:1234", "upstreamBasePath": "/app"})
	if g["runtimeId"] != runtime["id"] || g["baseURL"] != "/p/" {
		t.Fatal(g)
	}
	browserCall(t, m, "one", "/p", "browser.runtime.remove", map[string]string{"id": runtime["id"]})
	if m.runtime != nil || len(m.mounts) != 0 || len(m.gateways) != 0 {
		t.Fatal("runtime removal leaked mounts or streams")
	}
	replacement := browserCall(t, m, "other", "/p", "browser.runtime.register", map[string]string{"bootstrap": "web/bootstrap.js", "worker": "web/sw.js"})
	if replacement["id"] == runtime["id"] {
		t.Fatal("runtime generation reused")
	}
	m.stop("other")
	if m.runtime != nil {
		t.Fatal("owner shutdown leaked runtime")
	}
}
