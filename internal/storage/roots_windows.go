//go:build windows

package storage

import "syscall"

func filesystemRoots() []string {
	mask, _, callErr := syscall.NewLazyDLL("kernel32.dll").NewProc("GetLogicalDrives").Call()
	if mask == 0 || callErr != syscall.Errno(0) {
		return nil
	}
	roots := make([]string, 0, 8)
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) != 0 {
			roots = append(roots, string(rune('A'+i))+":\\")
		}
	}
	return roots
}
