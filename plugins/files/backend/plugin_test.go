package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

// fakeHost serves a minimal workspace. Roots are mapped into a temporary
// directory: "/" is base and a Windows drive "C:\" is base/C.
type fakeHost struct {
	t         *testing.T
	os        string
	base      string
	roots     []string
	started   []map[string]any
	transfers []map[string]any
	published []map[string]any
}

func capabilityError(code string) error { return &pluginapi.Error{Code: code, Message: code} }

func assign(result any, value any) error {
	if result == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, result)
}

func (h *fakeHost) local(root, name string) string {
	h.roots = append(h.roots, root)
	base := h.base
	if root != "/" {
		base = filepath.Join(h.base, strings.TrimSuffix(strings.TrimSuffix(root, `\`), ":"))
	}
	return filepath.Join(base, filepath.FromSlash(name))
}

func (h *fakeHost) call(method string, params any, result any) error {
	args, _ := params.(map[string]any)
	resolve := func() string {
		root, _ := args["root"].(string)
		name, _ := args["path"].(string)
		return h.local(root, name)
	}
	info := func(path string) map[string]any {
		stat, _ := os.Lstat(path)
		kind := "file"
		if stat.IsDir() {
			kind = "directory"
		}
		return map[string]any{"name": stat.Name(), "type": kind, "size": stat.Size(), "modifiedAt": stat.ModTime().UTC().Format(time.RFC3339Nano), "hidden": strings.HasPrefix(stat.Name(), ".")}
	}
	switch method {
	case "system.status":
		return assign(result, map[string]any{"os": h.os, "disks": []map[string]any{{"path": `C:\`}, {"path": `d:\`}, {"path": `C:\`}, {"path": "/"}}})
	case "workspace.stat":
		if _, err := os.Lstat(resolve()); err != nil {
			return capabilityError("not_found")
		}
		return assign(result, info(resolve()))
	case "workspace.list":
		items, err := os.ReadDir(resolve())
		if err != nil {
			return capabilityError("not_found")
		}
		entries := []map[string]any{}
		for _, item := range items {
			entries = append(entries, info(filepath.Join(resolve(), item.Name())))
		}
		return assign(result, map[string]any{"entries": entries})
	case "workspace.read":
		data, err := os.ReadFile(resolve())
		if err != nil {
			return capabilityError("not_found")
		}
		return assign(result, map[string]any{"data": base64.StdEncoding.EncodeToString(data)})
	case "workspace.write":
		data, _ := base64.StdEncoding.DecodeString(args["data"].(string))
		return os.WriteFile(resolve(), data, 0o644)
	case "workspace.mkdir":
		return os.MkdirAll(resolve(), 0o755)
	case "workspace.rename":
		return os.Rename(resolve(), h.local(args["root"].(string), args["target"].(string)))
	case "workspace.operation.start":
		h.started = append(h.started, args)
		return assign(result, map[string]any{"id": "op-1", "state": "running"})
	case "workspace.transfer.download", "workspace.transfer.upload":
		args["method"] = method
		h.transfers = append(h.transfers, args)
		return assign(result, map[string]any{"url": "api/v1/transfers/ticket"})
	case "events.publish":
		h.published = append(h.published, args)
		return nil
	}
	h.t.Fatalf("unexpected host call %s", method)
	return errors.New("unexpected")
}

func setup(t *testing.T, hostOS string) (*plugin, *fakeHost) {
	t.Helper()
	host := &fakeHost{t: t, os: hostOS, base: t.TempDir()}
	original := callHost
	callHost = host.call
	t.Cleanup(func() { callHost = original })
	return newPlugin(), host
}

func call(t *testing.T, p *plugin, method string, params any) map[string]any {
	t.Helper()
	result, failure := callRaw(p, method, params)
	if failure != nil {
		t.Fatalf("%s: %+v", method, failure)
	}
	return result
}

func callRaw(p *plugin, method string, params any) (map[string]any, *rpcError) {
	raw, _ := json.Marshal(params)
	result, failure := p.handle(method, raw)
	if failure != nil {
		return nil, failure
	}
	encoded, _ := json.Marshal(result)
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	return decoded, nil
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListingStartsAtTheFilesystemTop(t *testing.T) {
	p, host := setup(t, "linux")
	for _, name := range []string{"b.txt", "A.txt", ".secret"} {
		write(t, filepath.Join(host.base, "home", name), "x")
	}
	if err := os.Mkdir(filepath.Join(host.base, "home", "zeta"), 0o755); err != nil {
		t.Fatal(err)
	}
	top := call(t, p, "files.list", map[string]any{"path": ""})
	if top["top"] != true || len(top["entries"].([]any)) != 1 {
		t.Fatalf("top = %v", top)
	}
	listed := call(t, p, "files.list", map[string]any{"path": "/home/"})
	var names []string
	for _, item := range listed["entries"].([]any) {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "zeta,A.txt,b.txt" || listed["hiddenCount"] != float64(1) || listed["path"] != "home" || listed["top"] != false {
		t.Fatalf("listing = %v", listed)
	}
	if listed = call(t, p, "files.list", map[string]any{"path": "home", "showHidden": true}); len(listed["entries"].([]any)) != 4 {
		t.Fatalf("listing with hidden files = %v", listed)
	}
	for _, path := range []string{"../etc", "home/../../b", `home\b`, "home//x"} {
		if _, failure := callRaw(p, "files.list", map[string]any{"path": path}); failure == nil || failure.Code != "invalid_argument" {
			t.Errorf("path %q = %+v", path, failure)
		}
	}
	for _, root := range host.roots {
		if root != "/" {
			t.Fatalf("Linux paths used root %q", root)
		}
	}
}

func TestWindowsShowsDrivesAtTheTop(t *testing.T) {
	p, host := setup(t, "windows")
	write(t, filepath.Join(host.base, "C", "Users", "notes.txt"), "x")
	top := call(t, p, "files.list", map[string]any{"path": ""})
	var drives []string
	for _, item := range top["entries"].([]any) {
		drives = append(drives, item.(map[string]any)["name"].(string))
	}
	if strings.Join(drives, ",") != "C:,D:" {
		t.Fatalf("drives = %v", top)
	}
	listed := call(t, p, "files.list", map[string]any{"path": "c:/Users"})
	if entries := listed["entries"].([]any); len(entries) != 1 || host.roots[len(host.roots)-1] != `C:\` {
		t.Fatalf("drive listing = %v roots %v", listed, host.roots)
	}
	if _, failure := callRaw(p, "files.list", map[string]any{"path": "Users"}); failure == nil {
		t.Fatal("a Windows path without a drive was accepted")
	}
	if _, failure := callRaw(p, "files.operation.start", map[string]any{"kind": "delete", "from": "C:"}); failure == nil {
		t.Fatal("deleting a drive was accepted")
	}
}

func TestTextEditingDetectsConflictsAndBinaryFiles(t *testing.T) {
	p, host := setup(t, "linux")
	write(t, filepath.Join(host.base, "srv", "compose.yaml"), "services: {}\n")
	opened := call(t, p, "files.text.read", map[string]any{"path": "srv/compose.yaml"})
	if opened["content"] != "services: {}\n" {
		t.Fatalf("opened = %v", opened)
	}
	saved := call(t, p, "files.text.write", map[string]any{"path": "srv/compose.yaml", "content": "services:\n  web: {}\n", "expectedModifiedAt": opened["modifiedAt"]})
	if data, _ := os.ReadFile(filepath.Join(host.base, "srv", "compose.yaml")); string(data) != "services:\n  web: {}\n" {
		t.Fatalf("saved content = %q", data)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(host.base, "srv", "compose.yaml"), later, later); err != nil {
		t.Fatal(err)
	}
	if _, failure := callRaw(p, "files.text.write", map[string]any{"path": "srv/compose.yaml", "content": "lost", "expectedModifiedAt": saved["modifiedAt"]}); failure == nil || failure.Code != "conflict" {
		t.Fatalf("stale save = %+v", failure)
	}
	call(t, p, "files.text.write", map[string]any{"path": "srv/notes.md", "content": "new", "create": true})
	if _, failure := callRaw(p, "files.text.write", map[string]any{"path": "srv/notes.md", "content": "again", "create": true}); failure == nil || failure.Code != "already_exists" {
		t.Fatalf("create over existing = %+v", failure)
	}
	write(t, filepath.Join(host.base, "srv", "image.bin"), "\x89\x00\x01")
	if _, failure := callRaw(p, "files.text.read", map[string]any{"path": "srv/image.bin"}); failure == nil || failure.Code != "binary" {
		t.Fatalf("binary read = %+v", failure)
	}
}

func TestChangesNeverTargetTheTop(t *testing.T) {
	p, host := setup(t, "linux")
	write(t, filepath.Join(host.base, "data", "film.mkv"), "x")
	call(t, p, "files.folder.create", map[string]any{"path": "data", "name": "Film: Extras"})
	if _, err := os.Stat(filepath.Join(host.base, "data", "Film: Extras")); err != nil {
		t.Fatal(err)
	}
	if _, failure := callRaw(p, "files.folder.create", map[string]any{"path": "data", "name": "Film: Extras"}); failure == nil || failure.Code != "already_exists" {
		t.Fatalf("duplicate folder = %+v", failure)
	}
	if _, failure := callRaw(p, "files.folder.create", map[string]any{"path": "missing/parent", "name": "x"}); failure == nil || failure.Code != "not_found" {
		t.Fatalf("folder in a missing parent = %+v", failure)
	}
	for _, name := range []string{"a/b", "..", ""} {
		if _, failure := callRaw(p, "files.folder.create", map[string]any{"path": "data", "name": name}); failure == nil {
			t.Errorf("folder name %q was accepted", name)
		}
	}
	renamed := call(t, p, "files.rename", map[string]any{"path": "data/film.mkv", "name": "movie.mkv"})
	if renamed["path"] != "data/movie.mkv" {
		t.Fatalf("renamed = %v", renamed)
	}
	for _, params := range []map[string]any{{"kind": "delete", "from": ""}, {"kind": "delete", "from": "/"}, {"kind": "copy", "from": "data/movie.mkv", "to": ""}} {
		if _, failure := callRaw(p, "files.operation.start", params); failure == nil {
			t.Errorf("operation %v was accepted", params)
		}
	}
	if _, failure := callRaw(p, "files.rename", map[string]any{"path": "", "name": "x"}); failure == nil {
		t.Fatal("renaming the top was accepted")
	}
	call(t, p, "files.operation.start", map[string]any{"kind": "move", "from": "data/movie.mkv", "to": "srv/movie.mkv"})
	started := host.started[0]
	if started["kind"] != "move" || started["source"].(map[string]any)["root"] != "/" || started["target"].(map[string]any)["path"] != "srv/movie.mkv" {
		t.Fatalf("operation = %v", started)
	}
	call(t, p, "files.upload", map[string]any{"path": "", "name": "top.txt"})
	call(t, p, "files.download", map[string]any{"path": "data/movie.mkv", "inline": true})
	if host.transfers[0]["directory"] != "" || host.transfers[1]["inline"] != true {
		t.Fatalf("transfers = %v", host.transfers)
	}
	if failure := p.event("workspace.operation.done", json.RawMessage(`{"id":"op-1","state":"succeeded"}`)); failure != nil {
		t.Fatal(failure)
	}
	if len(host.published) != 1 || host.published[0]["event"] != "files.operation" {
		t.Fatalf("published = %v", host.published)
	}
}
