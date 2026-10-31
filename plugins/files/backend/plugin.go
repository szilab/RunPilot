package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

var callHost = pluginapi.CallHost

const (
	maxTextBytes = 1 << 20
	maxPathBytes = 4096
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
	return fail("failed", "file operation failed")
}

func decode(raw json.RawMessage, value any) *rpcError {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return fail("invalid_argument", "invalid request")
	}
	return nil
}

type entry struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	LinkType   string `json:"linkType,omitempty"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
	Hidden     bool   `json:"hidden"`
}

// The file manager always shows the whole host filesystem. Browser paths
// are slash-separated and relative to its top: "srv/data" on Linux and
// "C:/Users" on Windows, where the top level lists the drives. The RunPilot
// service account's permissions decide what can be read or changed.
type plugin struct {
	os string // host operating system, read once from system.status
}

func newPlugin() *plugin { return &plugin{} }

type hostStatus struct {
	OS    string `json:"os"`
	Disks []struct {
		Path string `json:"path"`
	} `json:"disks"`
}

func (p *plugin) hostOS() string {
	if p.os == "" {
		var status hostStatus
		if callHost("system.status", map[string]any{}, &status) == nil && status.OS != "" {
			p.os = status.OS
		} else {
			return "linux"
		}
	}
	return p.os
}

func (p *plugin) windows() bool { return p.hostOS() == "windows" }

// location is a browser path resolved to a workspace root and a path
// relative to it.
type location struct {
	public string // the slash-separated browser path
	root   string // absolute host directory used as workspace root
	path   string // path inside root; "" is the root itself
}

func drive(part string) bool {
	if len(part) != 2 || part[1] != ':' {
		return false
	}
	letter := part[0] | 0x20
	return letter >= 'a' && letter <= 'z'
}

// resolve validates a browser path. The top of the filesystem (and on
// Windows a drive root) can be listed but never renamed, moved or deleted.
func (p *plugin) resolve(value string, rootOK bool) (location, *rpcError) {
	value = strings.Trim(value, "/")
	if len(value) > maxPathBytes || strings.Contains(value, `\`) || strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return location{}, fail("invalid_argument", "invalid path")
	}
	parts := []string{}
	if value != "" {
		parts = strings.Split(value, "/")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return location{}, fail("invalid_argument", "invalid path")
		}
	}
	if !p.windows() {
		if value == "" && !rootOK {
			return location{}, fail("invalid_argument", "the top folder cannot be changed")
		}
		return location{public: value, root: "/", path: value}, nil
	}
	if value == "" {
		return location{}, fail("invalid_argument", "choose a drive")
	}
	if !drive(parts[0]) {
		return location{}, fail("invalid_argument", "a Windows path starts with a drive such as C:")
	}
	rest := strings.Join(parts[1:], "/")
	if rest == "" && !rootOK {
		return location{}, fail("invalid_argument", "a drive cannot be changed")
	}
	return location{public: value, root: strings.ToUpper(parts[0]) + `\`, path: rest}, nil
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && utf8.ValidString(name) && !strings.ContainsAny(name, "/\\\x00")
}

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func parentPath(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[:index]
	}
	return ""
}

func workspace(operation string, params map[string]any, out any) *rpcError {
	if err := callHost("workspace."+operation, params, out); err != nil {
		return hostError(err)
	}
	return nil
}

func (l location) params(extra map[string]any) map[string]any {
	params := map[string]any{"root": l.root, "path": l.path}
	for key, value := range extra {
		params[key] = value
	}
	return params
}

// drives lists the Windows drive roots as the top-level folders.
func (p *plugin) drives() []entry {
	var status hostStatus
	_ = callHost("system.status", map[string]any{}, &status)
	seen := map[string]bool{}
	out := []entry{}
	for _, disk := range status.Disks {
		if len(disk.Path) < 2 || !drive(disk.Path[:2]) {
			continue
		}
		name := strings.ToUpper(disk.Path[:2])
		if !seen[name] {
			seen[name] = true
			out = append(out, entry{Name: name, Type: "directory"})
		}
	}
	return out
}

func (p *plugin) list(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Path       string `json:"path"`
		ShowHidden bool   `json:"showHidden"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	if p.windows() && strings.Trim(request.Path, "/") == "" {
		return map[string]any{"path": "", "entries": p.drives(), "hiddenCount": 0, "top": true}, nil
	}
	target, failure := p.resolve(request.Path, true)
	if failure != nil {
		return nil, failure
	}
	var listing struct {
		Entries []entry `json:"entries"`
	}
	if failure := workspace("list", target.params(nil), &listing); failure != nil {
		return nil, failure
	}
	entries := make([]entry, 0, len(listing.Entries))
	hidden := 0
	for _, item := range listing.Entries {
		if item.Hidden && !request.ShowHidden {
			hidden++
			continue
		}
		entries = append(entries, item)
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i].Type == "directory", entries[j].Type == "directory"
		if left != right {
			return left
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return map[string]any{"path": target.public, "entries": entries, "hiddenCount": hidden, "top": target.path == "" && !p.windows()}, nil
}

type statResult struct {
	Type       string `json:"type"`
	LinkType   string `json:"linkType"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
}

func (p *plugin) readText(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Path string `json:"path"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	target, failure := p.resolve(request.Path, false)
	if failure != nil {
		return nil, failure
	}
	var info statResult
	if failure := workspace("stat", target.params(nil), &info); failure != nil {
		return nil, failure
	}
	if info.Type != "file" && !(info.Type == "symlink" && info.LinkType == "file") {
		return nil, fail("invalid_argument", "only regular files can be opened as text")
	}
	var read struct {
		Data string `json:"data"`
	}
	if failure := workspace("read", target.params(nil), &read); failure != nil {
		if failure.Code == "invalid_argument" {
			return nil, fail("too_large", "files larger than 1 MiB cannot be edited here; download them instead")
		}
		return nil, failure
	}
	data, err := base64.StdEncoding.DecodeString(read.Data)
	if err != nil {
		return nil, fail("failed", "invalid file data")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fail("binary", "this file is not UTF-8 text; download it instead")
	}
	return map[string]any{"content": string(data), "modifiedAt": info.ModifiedAt, "size": len(data)}, nil
}

// writeText saves text. A set expectedModifiedAt rejects the save when the
// file changed since it was opened; create rejects an existing file.
func (p *plugin) writeText(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Path               string `json:"path"`
		Content            string `json:"content"`
		ExpectedModifiedAt string `json:"expectedModifiedAt"`
		Create             bool   `json:"create"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	target, failure := p.resolve(request.Path, false)
	if failure != nil {
		return nil, failure
	}
	if !validName(target.path[strings.LastIndex(target.path, "/")+1:]) {
		return nil, fail("invalid_argument", "invalid file name")
	}
	if len(request.Content) > maxTextBytes {
		return nil, fail("too_large", "text is larger than 1 MiB")
	}
	var info statResult
	statFailure := workspace("stat", target.params(nil), &info)
	switch {
	case statFailure != nil && statFailure.Code != "not_found":
		return nil, statFailure
	case statFailure == nil && request.Create:
		return nil, fail("already_exists", "a file with this name already exists")
	case statFailure == nil && info.Type != "file":
		return nil, fail("invalid_argument", "the target is not a regular file")
	case statFailure == nil && request.ExpectedModifiedAt != "" && info.ModifiedAt != request.ExpectedModifiedAt:
		return nil, fail("conflict", "the file changed since it was opened")
	case statFailure != nil && !request.Create:
		return nil, fail("not_found", "the file no longer exists")
	}
	data := base64.StdEncoding.EncodeToString([]byte(request.Content))
	if failure := workspace("write", target.params(map[string]any{"data": data}), nil); failure != nil {
		return nil, failure
	}
	if failure := workspace("stat", target.params(nil), &info); failure != nil {
		return nil, failure
	}
	return map[string]any{"modifiedAt": info.ModifiedAt, "size": info.Size}, nil
}

func (p *plugin) createFolder(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	if !validName(request.Name) {
		return nil, fail("invalid_argument", "invalid folder name")
	}
	target, failure := p.resolve(joinPath(strings.Trim(request.Path, "/"), request.Name), false)
	if failure != nil {
		return nil, failure
	}
	if workspace("stat", target.params(nil), nil) == nil {
		return nil, fail("already_exists", "an item with this name already exists")
	}
	// The parent must exist; mkdir would otherwise create missing parents.
	parent, _ := p.resolve(parentPath(target.public), true)
	var info statResult
	if failure := workspace("stat", parent.params(nil), &info); failure != nil {
		return nil, failure
	}
	if failure := workspace("mkdir", target.params(nil), nil); failure != nil {
		return nil, failure
	}
	return map[string]any{"path": target.public}, nil
}

func (p *plugin) rename(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	target, failure := p.resolve(request.Path, false)
	if failure != nil {
		return nil, failure
	}
	if !validName(request.Name) {
		return nil, fail("invalid_argument", "invalid name")
	}
	renamed := joinPath(parentPath(target.path), request.Name)
	if failure := workspace("rename", map[string]any{"root": target.root, "path": target.path, "target": renamed}, nil); failure != nil {
		return nil, failure
	}
	return map[string]any{"path": joinPath(parentPath(target.public), request.Name)}, nil
}

// startOperation copies, moves or deletes one item in the background.
func (p *plugin) startOperation(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Kind string  `json:"kind"`
		From string  `json:"from"`
		To   *string `json:"to"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	from, failure := p.resolve(request.From, false)
	if failure != nil {
		return nil, failure
	}
	params := map[string]any{"kind": request.Kind, "source": map[string]any{"root": from.root, "path": from.path}}
	switch request.Kind {
	case "delete":
		if request.To != nil {
			return nil, fail("invalid_argument", "delete takes no target")
		}
	case "copy", "move":
		if request.To == nil {
			return nil, fail("invalid_argument", "copy and move need a target")
		}
		to, failure := p.resolve(*request.To, false)
		if failure != nil {
			return nil, failure
		}
		if !validName(to.path[strings.LastIndex(to.path, "/")+1:]) {
			return nil, fail("invalid_argument", "invalid target name")
		}
		params["target"] = map[string]any{"root": to.root, "path": to.path}
	default:
		return nil, fail("invalid_argument", "operation must be copy, move or delete")
	}
	var status json.RawMessage
	if failure := workspace("operation.start", params, &status); failure != nil {
		return nil, failure
	}
	return map[string]any{"operation": status}, nil
}

func (p *plugin) cancelOperation(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		ID string `json:"id"`
	}
	if failure := decode(raw, &request); failure != nil || request.ID == "" {
		return nil, fail("invalid_argument", "operation id is required")
	}
	var status json.RawMessage
	if failure := workspace("operation.cancel", map[string]any{"id": request.ID}, &status); failure != nil {
		return nil, failure
	}
	return map[string]any{"operation": status}, nil
}

func (p *plugin) download(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Path   string `json:"path"`
		Inline bool   `json:"inline"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	target, failure := p.resolve(request.Path, false)
	if failure != nil {
		return nil, failure
	}
	var ticket json.RawMessage
	if failure := workspace("transfer.download", target.params(map[string]any{"inline": request.Inline}), &ticket); failure != nil {
		return nil, failure
	}
	return ticket, nil
}

func (p *plugin) upload(raw json.RawMessage) (any, *rpcError) {
	var request struct {
		Path      string `json:"path"`
		Name      string `json:"name"`
		Overwrite bool   `json:"overwrite"`
	}
	if failure := decode(raw, &request); failure != nil {
		return nil, failure
	}
	dir, failure := p.resolve(request.Path, true)
	if failure != nil {
		return nil, failure
	}
	if !validName(request.Name) {
		return nil, fail("invalid_argument", "invalid file name")
	}
	var ticket json.RawMessage
	if failure := workspace("transfer.upload", map[string]any{"root": dir.root, "directory": dir.path, "name": request.Name, "overwrite": request.Overwrite}, &ticket); failure != nil {
		return nil, failure
	}
	return ticket, nil
}

func (p *plugin) handle(method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "files.list":
		return p.list(raw)
	case "files.text.read":
		return p.readText(raw)
	case "files.text.write":
		return p.writeText(raw)
	case "files.folder.create":
		return p.createFolder(raw)
	case "files.rename":
		return p.rename(raw)
	case "files.operation.start":
		return p.startOperation(raw)
	case "files.operation.cancel":
		return p.cancelOperation(raw)
	case "files.download":
		return p.download(raw)
	case "files.upload":
		return p.upload(raw)
	default:
		return nil, fail("unknown_method", "unknown Files method")
	}
}

// event relays background operation progress to the browser.
func (p *plugin) event(name string, raw json.RawMessage) *rpcError {
	if name != "workspace.operation.progress" && name != "workspace.operation.done" {
		return nil
	}
	var status json.RawMessage
	if json.Unmarshal(raw, &status) != nil {
		return fail("invalid_argument", "invalid operation event")
	}
	_ = callHost("events.publish", map[string]any{"event": "files.operation", "data": status}, nil)
	return nil
}

func (p *plugin) shutdown() {}
