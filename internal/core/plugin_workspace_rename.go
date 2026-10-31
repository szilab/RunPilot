package core

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"

	"github.com/szilab/RunPilot/internal/plugins"
)

var (
	errWorkspaceExists      = errors.New("workspace target already exists")
	errWorkspaceCrossDevice = errors.New("workspace source and target are on different filesystems")
)

// workspaceFileError maps filesystem errors to stable capability codes.
func workspaceFileError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return &plugins.HostFailure{Code: "not_found", Message: "workspace path does not exist"}
	case errors.Is(err, errWorkspaceExists), errors.Is(err, fs.ErrExist):
		return &plugins.HostFailure{Code: "already_exists", Message: "workspace target already exists"}
	case errors.Is(err, errWorkspaceCrossDevice):
		return &plugins.HostFailure{Code: "cross_device", Message: errWorkspaceCrossDevice.Error()}
	case errors.Is(err, fs.ErrPermission):
		return &plugins.HostFailure{Code: "permission_denied", Message: "the RunPilot service account cannot access this path"}
	}
	var failure *plugins.HostFailure
	if errors.As(err, &failure) {
		return err
	}
	return &plugins.HostFailure{Code: "failed", Message: "workspace operation failed"}
}

// splitWorkspacePath returns the parent directory ("." for top level) and base.
func splitWorkspacePath(name string) (string, string) {
	dir, base := path.Split(name)
	if dir == "" {
		return ".", base
	}
	return dir[:len(dir)-1], base
}

func (c *Controller) workspaceRename(owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		Root      string `json:"root"`
		Path      string `json:"path"`
		Target    string `json:"target"`
		Overwrite bool   `json:"overwrite"`
	}
	if len(raw) > 64<<10 || decodeNetworkParams(raw, &in) != nil || !validWorkspacePath(in.Path, false) || !validWorkspacePath(in.Target, false) {
		return nil, workspaceInvalid("rename requires valid path and target")
	}
	if in.Path == in.Target {
		return json.RawMessage(`{}`), nil
	}
	root, rootPath, err := c.openWorkspaceRoot(owner, in.Root)
	if err != nil {
		return nil, workspaceFileError(err)
	}
	defer root.Close()
	if err := renameInRoot(root, rootPath, in.Path, root, rootPath, in.Target, in.Overwrite); err != nil {
		return nil, workspaceFileError(err)
	}
	return json.RawMessage(`{}`), nil
}
