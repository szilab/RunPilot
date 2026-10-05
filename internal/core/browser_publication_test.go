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
