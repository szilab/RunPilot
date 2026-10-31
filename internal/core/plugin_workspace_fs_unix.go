//go:build !windows

package core

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// renameInRoot renames relative to directory handles opened inside the
// roots, so no path component can redirect the rename outside them.
func renameInRoot(src *os.Root, _ string, from string, dst *os.Root, _ string, to string, overwrite bool) error {
	fromDir, fromBase := splitWorkspacePath(from)
	toDir, toBase := splitWorkspacePath(to)
	sourceDir, err := src.Open(fromDir)
	if err != nil {
		return err
	}
	defer sourceDir.Close()
	targetDir, err := dst.Open(toDir)
	if err != nil {
		return err
	}
	defer targetDir.Close()
	sourceFD, targetFD := int(sourceDir.Fd()), int(targetDir.Fd())
	if !overwrite {
		if supported, err := renameNoReplace(sourceFD, fromBase, targetFD, toBase); supported {
			return renameError(err)
		}
		if _, err := dst.Lstat(to); err == nil {
			return errWorkspaceExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return renameError(unix.Renameat(sourceFD, fromBase, targetFD, toBase))
}

func renameError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EEXIST), errors.Is(err, unix.ENOTEMPTY):
		return errWorkspaceExists
	case errors.Is(err, unix.EXDEV):
		return errWorkspaceCrossDevice
	case errors.Is(err, unix.ENOENT):
		return os.ErrNotExist
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return os.ErrPermission
	}
	return err
}

func platformHidden(os.FileInfo) bool { return false }

// setFileModTime keeps a copied file's modification time.
func setFileModTime(file *os.File, modified time.Time) error {
	value := unix.NsecToTimeval(modified.UnixNano())
	return unix.Futimes(int(file.Fd()), []unix.Timeval{value, value})
}
