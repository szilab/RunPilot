//go:build !windows

package storage

import (
	"os"
	"strings"
)

func hiddenEntry(_ string, entry os.DirEntry) bool {
	return strings.HasPrefix(entry.Name(), ".")
}
