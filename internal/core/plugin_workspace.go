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
	"strings"

	"github.com/szilab/RunPilot/internal/plugins"
)

const maxWorkspaceFile = 1 << 20
const maxWorkspaceEntries = 4096

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
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0) || strings.Contains(part, ":") {
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
func (c *Controller) workspaceHostPath(owner, relative string) (string, error) {
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
		Data      string `json:"data"`
		Recursive bool   `json:"recursive"`
	}
	if len(raw) > 2*maxWorkspaceFile || json.Unmarshal(raw, &in) != nil || !validWorkspacePath(in.Path, op == "list" || op == "stat") {
		return nil, workspaceInvalid("invalid workspace request or path")
	}
	root, err := c.workspaceRoot(owner)
	if err != nil {
		return nil, err
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
		return json.Marshal(map[string]any{"path": in.Path, "directory": fi.IsDir(), "size": fi.Size(), "symlink": fi.Mode()&os.ModeSymlink != 0})
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
			out = append(out, map[string]any{"name": e.Name(), "directory": e.IsDir(), "symlink": e.Type()&os.ModeSymlink != 0})
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
		dir, err := c.workspaceHostPath(owner, path.Dir(name))
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
