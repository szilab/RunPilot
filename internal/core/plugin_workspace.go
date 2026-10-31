package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

const maxWorkspaceFile = 1 << 20

// maxWorkspaceEntries bounds one directory listing (and each directory level
// of a recursive removal); the listing stays well below the ABI payload limit.
const maxWorkspaceEntries = 16384

func workspaceInvalid(message string) error {
	return &plugins.HostFailure{Code: "invalid_argument", Message: message}
}
func validWorkspacePath(name string, rootOK bool) bool {
	if name == "" {
		return rootOK
	}
	if name == "." {
		return rootOK
	}
	if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || path.IsAbs(name) || len(name) > 1024 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0) {
			return false
		}
		// A colon selects a drive or an alternate data stream on Windows; it is
		// an ordinary file name character elsewhere.
		if runtime.GOOS == "windows" && strings.Contains(part, ":") {
			return false
		}
	}
	return true
}
func (c *Controller) workspaceRoot(owner string) (*os.Root, error) {
	if !safePluginStorageID(owner) {
		return nil, workspaceInvalid("invalid workspace owner")
	}
	base, err := os.OpenRoot(c.dataDir)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	name := "plugins/" + owner + "/data/workspace"
	if err := workspaceMkdir(base, name); err != nil {
		return nil, err
	}
	return base.OpenRoot(name)
}

// openExternalRoot opens a user-configured absolute directory. Only mkdir and
// write create a missing root; every other operation requires it to exist.
func openExternalRoot(rootPath string, create bool) (*os.Root, string, error) {
	if len(rootPath) > 4096 || !filepath.IsAbs(rootPath) || strings.ContainsRune(rootPath, 0) {
		return nil, "", workspaceInvalid("workspace root must be an absolute path")
	}
	absolute := filepath.Clean(rootPath)
	if create {
		if err := os.MkdirAll(absolute, 0700); err != nil {
			return nil, "", err
		}
	}
	root, err := os.OpenRoot(absolute)
	return root, absolute, err
}

// openWorkspaceRoot opens the owner's workspace, or an existing external root.
func (c *Controller) openWorkspaceRoot(owner, rootPath string) (*os.Root, string, error) {
	if rootPath == "" {
		root, err := c.workspaceRoot(owner)
		if err != nil {
			return nil, "", err
		}
		absolute, err := c.workspaceHostPath(owner, "")
		return root, absolute, err
	}
	return openExternalRoot(rootPath, false)
}

// workspaceLinkType reports what a symlink points to inside the root:
// "file", "directory", "other", or "" when it is broken or leaves the root.
func workspaceLinkType(root *os.Root, name string) string {
	info, err := root.Stat(name)
	switch {
	case err != nil:
		return ""
	case info.IsDir():
		return "directory"
	case info.Mode().IsRegular():
		return "file"
	}
	return "other"
}

// workspaceEntry describes one directory entry without following symlinks.
func workspaceEntry(name string, info os.FileInfo) map[string]any {
	kind := "other"
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
	case info.IsDir():
		kind = "directory"
	case info.Mode().IsRegular():
		kind = "file"
	}
	size := int64(0)
	if kind == "file" {
		size = info.Size()
	}
	return map[string]any{
		"name": name, "type": kind, "directory": kind == "directory", "symlink": kind == "symlink",
		"size": size, "modifiedAt": info.ModTime().UTC().Format(time.RFC3339Nano),
		"hidden": strings.HasPrefix(name, ".") || platformHidden(info),
	}
}

func (c *Controller) workspaceHostPath(owner, relative string, roots ...string) (string, error) {
	externalRoot := ""
	if len(roots) > 0 {
		externalRoot = roots[0]
	}
	if externalRoot != "" {
		return filepath.Join(externalRoot, filepath.FromSlash(relative)), nil
	}
	base, err := filepath.Abs(filepath.Join(c.dataDir, "plugins", owner, "data", "workspace"))
	if err != nil {
		return "", err
	}
	return filepath.Join(base, filepath.FromSlash(relative)), nil
}
func workspaceParent(root *os.Root, name string) (*os.Root, string, error) {
	parent, base := path.Split(name)
	if parent == "" {
		return root, base, nil
	}
	r, err := root.OpenRoot(strings.TrimSuffix(parent, "/"))
	return r, base, err
}
func (c *Controller) pluginWorkspace(owner, op string, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		Path      string `json:"path"`
		Root      string `json:"root"`
		Data      string `json:"data"`
		Recursive bool   `json:"recursive"`
	}
	switch op {
	case "rename":
		return c.workspaceRename(owner, raw)
	case "operation.start", "operation.status", "operation.cancel":
		return c.workspaceOperations.handle(owner, strings.TrimPrefix(op, "operation."), raw)
	case "transfer.download", "transfer.upload":
		return c.workspaceTransfers.issue(owner, strings.TrimPrefix(op, "transfer."), raw)
	}
	if len(raw) > 2*maxWorkspaceFile || json.Unmarshal(raw, &in) != nil || !validWorkspacePath(in.Path, op == "list" || op == "stat" || op == "mkdir") {
		return nil, workspaceInvalid("invalid workspace request or path")
	}
	var root *os.Root
	var err error
	externalRoot := ""
	if in.Root == "" {
		root, err = c.workspaceRoot(owner)
	} else {
		root, externalRoot, err = openExternalRoot(in.Root, op == "mkdir" || op == "write")
	}
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, workspaceInvalid("workspace root is unavailable")
	}
	defer root.Close()
	name := in.Path
	if name == "" {
		name = "."
	}
	switch op {
	case "stat":
		fi, err := root.Lstat(name)
		if err != nil {
			return nil, err
		}
		entry := workspaceEntry(path.Base(name), fi)
		if entry["type"] == "symlink" {
			entry["linkType"] = workspaceLinkType(root, name)
		}
		entry["path"] = in.Path
		entry["size"] = fi.Size()
		return json.Marshal(entry)
	case "list":
		f, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		entries, err := f.ReadDir(maxWorkspaceEntries + 1)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if len(entries) > maxWorkspaceEntries {
			return nil, workspaceInvalid("workspace directory has too many entries")
		}
		out := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			// DirEntry.Info resolves names outside os.Root for nested
			// directories; Lstat through the root stays confined.
			info, err := root.Lstat(path.Join(name, e.Name()))
			if err != nil {
				continue // removed while listing
			}
			item := workspaceEntry(e.Name(), info)
			if item["type"] == "symlink" {
				item["linkType"] = workspaceLinkType(root, path.Join(name, e.Name()))
			}
			out = append(out, item)
		}
		return json.Marshal(map[string]any{"entries": out})
	case "read":
		f, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() || fi.Size() > maxWorkspaceFile {
			return nil, workspaceInvalid("workspace file is not a regular file of at most 1 MiB")
		}
		b, err := io.ReadAll(io.LimitReader(f, maxWorkspaceFile+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxWorkspaceFile {
			return nil, workspaceInvalid("workspace file exceeds 1 MiB")
		}
		return json.Marshal(map[string]any{"data": base64.StdEncoding.EncodeToString(b)})
	case "mkdir":
		if err := workspaceMkdir(root, name); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	case "write":
		b, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil || len(b) > maxWorkspaceFile {
			return nil, workspaceInvalid("workspace write exceeds 1 MiB or contains invalid base64")
		}
		parent, base, err := workspaceParent(root, name)
		if err != nil {
			return nil, err
		}
		if parent != root {
			defer parent.Close()
		}
		if fi, err := parent.Lstat(base); err == nil && !fi.Mode().IsRegular() {
			return nil, workspaceInvalid("workspace target is not a regular file")
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		// A temporary file in the same directory makes replacement atomic.
		dir, err := c.workspaceHostPath(owner, path.Dir(name), externalRoot)
		if err != nil {
			return nil, err
		}
		tmp, err := os.CreateTemp(dir, ".runpilot-")
		if err != nil {
			return nil, err
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		if _, err = tmp.Write(b); err != nil {
			tmp.Close()
			return nil, err
		}
		if err = tmp.Sync(); err != nil {
			tmp.Close()
			return nil, err
		}
		if err = tmp.Close(); err != nil {
			return nil, err
		}
		if err = os.Rename(tmpName, filepath.Join(dir, base)); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	case "remove":
		if name == "." {
			return nil, workspaceInvalid("workspace root cannot be removed")
		}
		if err := workspaceRemove(root, name, in.Recursive); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	}
	return nil, workspaceInvalid("unknown workspace operation")
}
func workspaceMkdir(root *os.Root, name string) error {
	if name == "." {
		return nil
	}
	parts := strings.Split(name, "/")
	cur := ""
	for _, p := range parts {
		if cur != "" {
			cur += "/"
		}
		cur += p
		err := root.Mkdir(cur, 0700)
		if err != nil && !os.IsExist(err) {
			return err
		}
		fi, err := root.Lstat(cur)
		if err != nil {
			return err
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return workspaceInvalid("workspace path includes a symlink or non-directory")
		}
	}
	return nil
}
func workspaceRemove(root *os.Root, name string, recursive bool) error {
	fi, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if fi.IsDir() && recursive {
		dir, err := root.Open(name)
		if err != nil {
			return err
		}
		entries, err := dir.ReadDir(maxWorkspaceEntries + 1)
		dir.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(entries) > maxWorkspaceEntries {
			return workspaceInvalid("workspace directory has too many entries")
		}
		for _, e := range entries {
			child := path.Join(name, e.Name())
			if err := workspaceRemove(root, child, true); err != nil {
				return fmt.Errorf("remove %s: %w", child, err)
			}
		}
	}
	return root.Remove(name)
}
func (h controllerPluginHost) Workspace(_ context.Context, owner, operation string, raw json.RawMessage) (json.RawMessage, error) {
	value, err := h.controller.pluginWorkspace(owner, operation, raw)
	if os.IsNotExist(err) {
		return nil, &plugins.HostFailure{Code: "not_found", Message: "workspace path does not exist"}
	}
	return value, err
}
