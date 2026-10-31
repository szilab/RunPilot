//go:build linux

package core

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameNoReplace uses renameat2(RENAME_NOREPLACE) so an existing target is
// never replaced. It reports false when the filesystem lacks support.
func renameNoReplace(fromDir int, from string, toDir int, to string) (bool, error) {
	err := unix.Renameat2(fromDir, from, toDir, to, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		return false, nil
	}
	return true, err
}
