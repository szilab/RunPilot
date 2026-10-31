//go:build windows

package core

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// renameInRoot rejects symlinked parents and renames by path. Windows has no
// handle-relative rename that Go exposes for os.Root.
func renameInRoot(src *os.Root, srcPath, from string, dst *os.Root, dstPath, to string, overwrite bool) error {
	fromDir, _ := splitWorkspacePath(from)
	toDir, _ := splitWorkspacePath(to)
	if err := rejectWorkspaceSymlinks(src, fromDir); err != nil {
		return err
	}
	if err := rejectWorkspaceSymlinks(dst, toDir); err != nil {
		return err
	}
	if _, err := src.Lstat(from); err != nil {
		return err
	}
	source, err := windows.UTF16PtrFromString(filepath.Join(srcPath, filepath.FromSlash(from)))
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(filepath.Join(dstPath, filepath.FromSlash(to)))
	if err != nil {
		return err
	}
	var flags uint32
	if overwrite {
		flags = windows.MOVEFILE_REPLACE_EXISTING
	}
	err = windows.MoveFileEx(source, target, flags)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, windows.ERROR_ALREADY_EXISTS), errors.Is(err, windows.ERROR_FILE_EXISTS):
		return errWorkspaceExists
	case errors.Is(err, windows.ERROR_NOT_SAME_DEVICE):
		return errWorkspaceCrossDevice
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return os.ErrNotExist
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return os.ErrPermission
	}
	return err
}

func platformHidden(info os.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&windows.FILE_ATTRIBUTE_HIDDEN != 0
}

func setFileModTime(file *os.File, modified time.Time) error {
	value := windows.NsecToFiletime(modified.UnixNano())
	return windows.SetFileTime(windows.Handle(file.Fd()), nil, nil, &value)
}
