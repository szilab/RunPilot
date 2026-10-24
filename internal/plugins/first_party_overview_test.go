package plugins

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// First-party plugins keep Overview to the built-in host dashboard. The
// runpilot.overview contract stays available to other plugins.
func TestFirstPartyPluginsDoNotRegisterOverviewWidgets(t *testing.T) {
	root := filepath.Join("..", "..", "plugins")
	usage := regexp.MustCompile(`\boverview\s*\.\s*register\b|\{\s*[^}]*\boverview\b[^}]*\}\s*=\s*runpilot\b`)
	checked := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "vendor" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".js") && !strings.HasSuffix(name, ".mjs") || strings.Contains(name, ".test.") || !strings.Contains(filepath.ToSlash(path), "/web/") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		if usage.Match(source) {
			t.Errorf("%s registers an Overview widget; first-party plugins must not", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no first-party plugin frontend was checked")
	}
}
