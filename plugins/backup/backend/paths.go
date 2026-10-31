package main

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Jobs store native host paths: "/srv/data" on Linux and "C:\Users" on
// Windows. The folder picker and file checks use the workspace capability
// with an external root, so a native path is split into a root and a
// slash-separated path inside it.

func driveLetter(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	letter := value[0] | 0x20
	return letter >= 'a' && letter <= 'z'
}

func absolutePath(value, hostOS string) bool {
	if hostOS == "windows" {
		return driveLetter(value) && len(value) >= 3 && (value[2] == '\\' || value[2] == '/')
	}
	return strings.HasPrefix(value, "/")
}

func uncPath(value, hostOS string) bool {
	return hostOS == "windows" && (strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//"))
}

func cleanNative(value, hostOS string) string {
	value = strings.ReplaceAll(value, `\`, "/")
	for strings.Contains(value, "//") {
		value = strings.ReplaceAll(value, "//", "/")
	}
	value = strings.TrimSuffix(value, "/")
	if hostOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

// sameFolder reports whether one path equals or contains the other, which
// would make a mirror copy into itself.
func sameFolder(a, b, hostOS string) bool {
	a, b = cleanNative(a, hostOS), cleanNative(b, hostOS)
	return a == b || strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/")
}

type location struct{ root, path string }

// splitNative turns an absolute native path into a workspace location.
func splitNative(value, hostOS string) (location, bool) {
	if !absolutePath(value, hostOS) || strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return location{}, false
	}
	root, rest := "/", value
	if hostOS == "windows" {
		root, rest = strings.ToUpper(value[:2])+`\`, strings.ReplaceAll(value[2:], `\`, "/")
	}
	parts := []string{}
	for _, part := range strings.Split(rest, "/") {
		switch part {
		case "", ".":
		case "..":
			return location{}, false
		default:
			parts = append(parts, part)
		}
	}
	return location{root: root, path: strings.Join(parts, "/")}, true
}

// browsePath is the picker's slash-separated path: "srv/data" on Linux and
// "C:/Users" on Windows, where "" lists the drives.
func browseLocation(value, hostOS string) (location, bool) {
	value = strings.Trim(value, "/")
	if strings.Contains(value, `\`) {
		return location{}, false
	}
	if hostOS != "windows" {
		return splitNative("/"+value, hostOS)
	}
	if value == "" {
		return location{}, true
	}
	if len(value) == 2 {
		value += "/"
	}
	return splitNative(value, hostOS)
}

type workspaceEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	LinkType string `json:"linkType"`
	Hidden   bool   `json:"hidden"`
}

// browse lists the folders of a picker path. Hidden folders are included
// because backups often live there (for example ~/.config).
func (p *plugin) browse(path string) (any, *rpcError) {
	hostOS := p.hostOS()
	loc, ok := browseLocation(path, hostOS)
	if !ok {
		return nil, rpcFail("invalid_argument", "invalid path")
	}
	path = strings.Trim(path, "/")
	if hostOS == "windows" && loc.root == "" {
		drives := []string{}
		seen := map[string]bool{}
		for _, disk := range p.status().Disks {
			if driveLetter(disk.Path) {
				name := strings.ToUpper(disk.Path[:2])
				if !seen[name] {
					seen[name] = true
					drives = append(drives, name)
				}
			}
		}
		sort.Strings(drives)
		return map[string]any{"path": "", "folders": drives, "top": true}, nil
	}
	var listed struct {
		Entries []workspaceEntry `json:"entries"`
	}
	if err := callHost("workspace.list", map[string]any{"root": loc.root, "path": loc.path}, &listed); err != nil {
		return nil, hostFailure(err)
	}
	folders := []string{}
	for _, item := range listed.Entries {
		if item.Type == "directory" || (item.Type == "symlink" && item.LinkType == "directory") {
			folders = append(folders, item.Name)
		}
	}
	sort.Slice(folders, func(i, j int) bool {
		a, b := strings.ToLower(folders[i]), strings.ToLower(folders[j])
		if a != b {
			return a < b
		}
		return folders[i] < folders[j]
	})
	return map[string]any{"path": path, "folders": folders, "top": hostOS != "windows" && path == ""}, nil
}

// emptyOrMissing checks a restore target: restic never overwrites anything
// when it restores into a new or empty folder.
func (p *plugin) emptyOrMissing(target string) *rpcError {
	loc, ok := splitNative(target, p.hostOS())
	if !ok || loc.path == "" {
		return rpcFail("invalid_argument", "choose a new or empty folder to restore into")
	}
	var stat struct {
		Type string `json:"type"`
	}
	if err := callHost("workspace.stat", map[string]any{"root": loc.root, "path": loc.path}, &stat); err != nil {
		if isNotFound(err) {
			return nil
		}
		return hostFailure(err)
	}
	if stat.Type != "directory" {
		return rpcFail("invalid_argument", "the restore target exists and is not a folder")
	}
	var listed struct {
		Entries []workspaceEntry `json:"entries"`
	}
	if err := callHost("workspace.list", map[string]any{"root": loc.root, "path": loc.path}, &listed); err != nil {
		return hostFailure(err)
	}
	if len(listed.Entries) > 0 {
		return rpcFail("invalid_argument", "the restore target is not empty; choose a new or empty folder")
	}
	return nil
}

// readEnvironmentFile reads KEY=VALUE lines. Values are passed only to the
// backup process and never stored, logged or returned to the browser.
func (p *plugin) readEnvironmentFile(file string) (map[string]string, error) {
	loc, ok := splitNative(file, p.hostOS())
	if !ok || loc.path == "" {
		return nil, fail("environment file: invalid path")
	}
	var read struct {
		Data string `json:"data"`
	}
	if err := callHost("workspace.read", map[string]any{"root": loc.root, "path": loc.path}, &read); err != nil {
		return nil, fail("environment file " + file + ": " + errText(err))
	}
	data, err := decodeBase64(read.Data)
	if err != nil {
		return nil, fail("environment file " + file + " could not be read")
	}
	return parseEnvironment(string(data))
}

func parseEnvironment(text string) (map[string]string, error) {
	out := map[string]string{}
	for number, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, found := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !found || !validEnvironmentName(name) {
			return nil, fail("environment file: line " + itoa(number+1) + " is not NAME=value")
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		out[name] = value
	}
	if len(out) > 256 {
		return nil, fail("environment file: too many variables")
	}
	return out, nil
}

func validEnvironmentName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for i, ch := range name {
		if !(ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || i > 0 && ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}
