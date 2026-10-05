// Package browserpath validates virtual browser mount paths.
package browserpath

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

func Validate(value string, root bool) error {
	if len(value) > 1024 || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\?#\x00\r\n") {
		return fmt.Errorf("invalid path")
	}

	decoded := value
	for i := 0; i < 4; i++ {
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return fmt.Errorf("invalid escaping")
		}
		if strings.ContainsAny(next, "\\\x00\r\n") {
			return fmt.Errorf("invalid path")
		}
		for _, part := range strings.Split(next, "/") {
			if part == ".." || part == "." {
				return fmt.Errorf("path traversal")
			}
		}
		if next == decoded {
			break
		}
		decoded = next
	}
	if strings.Contains(decoded, "%") {
		return fmt.Errorf("ambiguous escaping")
	}
	if path.Clean(value) != value || (!root && value == "/") {
		return fmt.Errorf("path must be normalized")
	}
	return nil
}

func Mount(value string) error {
	if (&url.URL{Path: value}).EscapedPath() != value || strings.Contains(value, "%") {
		return fmt.Errorf("mount must use literal URL-safe characters")
	}
	if err := Validate(value, false); err != nil {
		return err
	}
	first := strings.Split(strings.TrimPrefix(value, "/"), "/")[0]
	// Reserve the shell's files and framework namespaces (including future static files).
	if strings.Contains(first, ".") {
		return fmt.Errorf("reserved mount")
	}
	switch first {
	case "api", "plugins", "vendor", "healthz", "__runpilot__":
		return fmt.Errorf("reserved mount")
	}
	return nil
}
func Overlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func Join(base, mount string) string { return strings.TrimSuffix(base, "/") + mount }

func Escape(value string) string { return (&url.URL{Path: value}).EscapedPath() }
