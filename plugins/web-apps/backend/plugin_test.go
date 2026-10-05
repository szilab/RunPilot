package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type fakeHost struct {
	failWrite    bool
	storage      json.RawMessage
	publications map[string]string
	opened       map[string]any
	closes       int
	next         int
}

func (h *fakeHost) call(method string, params any, out any) error {
	args, _ := params.(map[string]any)
	respond := func(value any) error {
		if out == nil {
			return nil
		}
		raw, _ := json.Marshal(value)
		return json.Unmarshal(raw, out)
	}
	switch method {
	case "browser.runtime.register":
		return respond(map[string]string{"id": "runtime"})
	case "browser.runtime.remove":
		return nil
	case "storage.get":
		if h.storage != nil && out != nil {
			return json.Unmarshal(h.storage, out)
		}
		return nil
	case "storage.set":
		if h.failWrite {
			return fmt.Errorf("disk full")
		}
		h.storage, _ = json.Marshal(args["value"])
		return nil
	case "browser.publication.register":
		h.next++
		if h.publications == nil {
			h.publications = map[string]string{}
		}
		id := fmt.Sprintf("pub-%d", h.next)
		h.publications[id] = args["mountPath"].(string)
		return respond(map[string]string{"id": id})
	case "browser.publication.remove":
		delete(h.publications, args["id"].(string))
		return nil
	case "http.gateway.open":
		h.opened = args
		h.next++
		return respond(map[string]string{"id": fmt.Sprintf("stream-%d", h.next), "publicPrefix": "/p" + h.publications[args["publicationId"].(string)], "publicationId": args["publicationId"].(string), "runtimeId": "runtime", "baseURL": "/p/"})
	case "http.gateway.close":
		h.closes++
		return nil
	case "browser.stream.ticket":
		return respond(map[string]string{"ticket": "one-time-ticket"})
	}
	return fmt.Errorf("unexpected capability %s", method)
}
func installHost(t *testing.T, h *fakeHost) {
	old := callHost
	callHost = h.call
	t.Cleanup(func() { callHost = old })
}
func TestTargetCRUDAndSessionSnapshot(t *testing.T) {
	h := &fakeHost{}
	installHost(t, h)
	p := newPlugin()
	result, f := p.handle("apps.targets.save", json.RawMessage(`{"target":{"name":" Jellyfin ","mountPath":"/app/","upstreamURL":"http://localhost:8096/","upstreamBasePath":"/p/app/","basePathHeader":"X-Script-Name","forwardPublicHost":true,"forwardPublicScheme":true,"customHeaders":{"X-Compat":"yes"}}}`))
	if f != nil {
		t.Fatal(f)
	}
	saved := result.(map[string]any)["target"].(target)
	if saved.ID != "app-1" || saved.MountPath != "/app" || saved.UpstreamBasePath != "/p/app" {
		t.Fatal(saved)
	}
	second := newPlugin()
	if f = second.initialize(); f != nil {
		t.Fatal(f)
	}
	list, f := second.handle("apps.targets.list", nil)
	if f != nil || len(list.(map[string]any)["targets"].([]target)) != 1 {
		t.Fatal(list, f)
	}
	opened, f := p.handle("apps.session.open", json.RawMessage(`{"targetId":"app-1","publicHost":"runpilot.example","publicScheme":"https"}`))
	if f != nil {
		t.Fatal(f)
	}
	session := opened.(map[string]any)["session"].(session)
	if h.opened["upstreamURL"] != "http://localhost:8096" || h.opened["upstreamBasePath"] != "/p/app" || session.PublicPrefix != "/p/app" {
		t.Fatal(h.opened, session)
	}
	if h.opened["basePathHeader"] != "X-Script-Name" || h.opened["forwardPublicHost"] != true || h.opened["forwardPublicScheme"] != true || h.opened["publicHost"] != "runpilot.example" || h.opened["publicScheme"] != "https" || h.opened["customHeaders"].(map[string]string)["X-Compat"] != "yes" {
		t.Fatal("proxy compatibility settings were not snapshotted into the gateway", h.opened)
	}
	for _, method := range []string{"apps.targets.delete", "apps.targets.save"} {
		request := `{"id":"app-1"}`
		if method == "apps.targets.save" {
			request = `{"target":{"id":"app-1","name":"edit","mountPath":"/other","upstreamURL":"https://host","upstreamBasePath":"/"}}`
		}
		if _, f = p.handle(method, json.RawMessage(request)); f == nil || f.Code != "failed_precondition" {
			t.Fatal("active target mutation", method, f)
		}
	}
	if _, f = p.handle("apps.session.open", json.RawMessage(`{"targetId":"app-1","upstreamURL":"http://attacker"}`)); f == nil {
		t.Fatal("browser overrode upstream")
	}
	if strings.Contains(string(h.storage), "ticket") || strings.Contains(string(h.storage), `"streamId"`) {
		t.Fatal("persisted runtime secret")
	}
	if f = p.event("http.gateway.closed", json.RawMessage(`{"id":"`+session.ID+`"}`)); f != nil || len(p.sessions) != 0 {
		t.Fatal(f)
	}
	if _, f = p.handle("apps.targets.delete", json.RawMessage(`{"id":"app-1"}`)); f != nil {
		t.Fatal(f)
	}
	if len(p.publications) != 0 {
		t.Fatal("registration leaked")
	}
	second.shutdown()
	if len(h.publications) != 0 {
		t.Fatal("shutdown registration leaked")
	}
}
func TestTargetValidationAndCollisions(t *testing.T) {
	h := &fakeHost{}
	installHost(t, h)
	p := newPlugin()
	for _, mount := range []string{"/", "relative", "/api", "/plugins/foo", "/app.js", "/healthz", "/a/../b", "/a/%2e%2e/b", "/a/%252e%252e/b", "/a//b", "/a\\b"} {
		t.Run(mount, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"target": target{Name: "name", MountPath: mount, UpstreamURL: "http://host", UpstreamBasePath: "/"}})
			if _, f := p.handle("apps.targets.save", raw); f == nil {
				t.Fatal("invalid mount accepted")
			}
		})
	}
	for _, url := range []string{"ftp://host", "http://user:password@host", "http://host/path", "https://host?key=secret", "http://host:70000", "http://host/#secret"} {
		raw, _ := json.Marshal(map[string]any{"target": target{Name: "name", MountPath: "/app", UpstreamURL: url, UpstreamBasePath: "/"}})
		if _, f := p.handle("apps.targets.save", raw); f == nil {
			t.Fatal("invalid upstream", url)
		}
	}
	for _, id := range []string{"app-0", "app-01", "app-x", "foreign-1"} {
		if _, f := normalizeTarget(target{ID: id, Name: "name", MountPath: "/app", UpstreamURL: "http://host"}); f == nil {
			t.Fatal(id)
		}
	}
	for _, name := range []string{"", strings.Repeat("n", 101), "line\nname"} {
		if _, f := normalizeTarget(target{Name: name, MountPath: "/app", UpstreamURL: "http://host"}); f == nil {
			t.Fatal("invalid name")
		}
	}
	for _, headers := range []map[string]string{
		{"Authorization": "Bearer secret"}, {"Cookie": "session=secret"}, {"Proxy-Authorization": "secret"},
		{"X-Forwarded-Host": "evil.example"}, {"Connection": "close"}, {"Accept-Encoding": "br"}, {"Range": "bytes=1-2"}, {"Bad Header": "x"}, {"X-Okay": "bad\r\nvalue"},
	} {
		if _, f := normalizeTarget(target{Name: "headers", MountPath: "/headers", UpstreamURL: "http://host", CustomHeaders: headers}); f == nil {
			t.Fatalf("accepted unsafe custom headers: %#v", headers)
		}
	}
	if _, f := normalizeTarget(target{Name: "headers", MountPath: "/headers", UpstreamURL: "http://host", BasePathHeader: "X-Script-Name", ForwardHost: true, ForwardScheme: true, CustomHeaders: map[string]string{"X-Compat": "yes"}}); f != nil {
		t.Fatal("valid generic proxy configuration rejected", f)
	}
	if _, f := p.handle("apps.targets.save", json.RawMessage(`{"target":{"name":"first","mountPath":"/app","upstreamURL":"http://host"}}`)); f != nil {
		t.Fatal(f)
	}
	for _, mount := range []string{"/app", "/app/nested"} {
		raw, _ := json.Marshal(map[string]any{"target": target{Name: "second", MountPath: mount, UpstreamURL: "http://host"}})
		if _, f := p.handle("apps.targets.save", raw); f == nil {
			t.Fatal("mount collision")
		}
	}
	if _, f := p.handle("apps.targets.save", json.RawMessage(`{"target":{"id":"app-1","name":"moved","mountPath":"/other","upstreamURL":"https://host"}}`)); f != nil {
		t.Fatal(f)
	}
	if len(h.publications) != 1 {
		t.Fatal("stale publication", h.publications)
	}
	if _, f := p.handle("apps.session.open", json.RawMessage(`{"targetId":"app-1"}`)); f != nil {
		t.Fatal(f)
	}
	p.shutdown()
	if h.closes != 1 || len(h.publications) != 0 {
		t.Fatal("shutdown failed")
	}
}

func TestMountChangeRollsBackPublicationOnStorageFailure(t *testing.T) {
	h := &fakeHost{}
	installHost(t, h)
	p := newPlugin()
	if _, f := p.handle("apps.targets.save", json.RawMessage(`{"target":{"name":"first","mountPath":"/app","upstreamURL":"http://host"}}`)); f != nil {
		t.Fatal(f)
	}
	original := string(h.storage)
	h.failWrite = true
	if _, f := p.handle("apps.targets.save", json.RawMessage(`{"target":{"id":"app-1","name":"moved","mountPath":"/app/nested","upstreamURL":"https://host"}}`)); f == nil {
		t.Fatal("storage failure ignored")
	}
	if string(h.storage) != original || len(h.publications) != 1 || h.publications[p.publications["app-1"]] != "/app" {
		t.Fatal("publication did not roll back", h.publications, p.publications)
	}
	h.failWrite = false
	if _, f := p.handle("apps.targets.save", json.RawMessage(`{"target":{"id":"app-1","name":"moved","mountPath":"/app/nested","upstreamURL":"https://host"}}`)); f != nil {
		t.Fatal(f)
	}
	if h.publications[p.publications["app-1"]] != "/app/nested" {
		t.Fatal("nested mount move failed")
	}
}
