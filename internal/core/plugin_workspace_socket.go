package core

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/szilab/RunPilot/internal/plugins"
)

// maxUnixSocketPath stays below the smallest common sun_path limit (104 on
// BSD-derived systems, 108 on Linux) including the terminating byte.
const maxUnixSocketPath = 100

// workspaceSocketHostPath resolves an existing Unix domain socket inside the
// owner's workspace. Symlink components are rejected so the dial cannot leave
// the owner workspace.
func (c *Controller) workspaceSocketHostPath(owner, name string) (string, error) {
	if !validWorkspacePath(name, false) {
		return "", workspaceInvalid("invalid workspace socket path")
	}
	root, err := c.workspaceRoot(owner)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := rejectWorkspaceSymlinks(root, name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", &plugins.HostFailure{Code: "not_found", Message: "workspace socket does not exist"}
		}
		return "", err
	}
	fi, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return "", workspaceInvalid("workspace path is not a socket")
	}
	return c.workspaceHostPath(owner, name)
}

// dialUnixSocket connects to a Unix domain socket. Paths longer than the
// sun_path limit are reached on Linux through the parent directory's
// /proc/self/fd entry, so deep data directories still work.
func dialUnixSocket(ctx context.Context, dialer *net.Dialer, socketPath string) (net.Conn, error) {
	if len(socketPath) < maxUnixSocketPath {
		return dialer.DialContext(ctx, "unix", socketPath)
	}
	if runtime.GOOS != "linux" {
		return nil, &plugins.HostFailure{Code: "failed", Message: "workspace socket path is too long"}
	}
	dir, err := os.Open(filepath.Dir(socketPath))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	short := "/proc/self/fd/" + strconv.Itoa(int(dir.Fd())) + "/" + filepath.Base(socketPath)
	if len(short) >= maxUnixSocketPath {
		return nil, &plugins.HostFailure{Code: "failed", Message: "workspace socket name is too long"}
	}
	return dialer.DialContext(ctx, "unix", short)
}
