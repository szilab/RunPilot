//go:build windows

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const fileAttributeHidden = 0x2

func hiddenEntry(directory string, entry os.DirEntry) bool {
	if strings.HasPrefix(entry.Name(), ".") {
		return true
	}
	p, err := syscall.UTF16PtrFromString(filepath.Join(directory, entry.Name()))
	if err != nil {
		return false
	}
	attributes, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetFileAttributesW").Call(uintptr(unsafe.Pointer(p)))
	return attributes != 0xffffffff && attributes&fileAttributeHidden != 0
}
