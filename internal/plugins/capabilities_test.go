package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The capability index in PLUGIN_API.md is the public contract; keep it in
// step with the handler table.
func TestPublishedCapabilitiesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "PLUGIN_API.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	start := strings.Index(text, "| Family | Capabilities |")
	if start < 0 {
		t.Fatal("PLUGIN_API.md has no capability index")
	}
	index := text[start:]
	if end := strings.Index(index, "\n\n"); end >= 0 {
		index = index[:end]
	}
	for _, name := range PublishedCapabilities() {
		if !strings.Contains(index, "`"+name+"`") {
			t.Errorf("capability %s is published but missing from the PLUGIN_API.md index", name)
		}
	}
	// Code spans that look like a capability name ("family.method") must be published.
	for _, line := range strings.Split(index, "\n")[2:] {
		spans := strings.Split(line, "`")
		for i := 1; i < len(spans); i += 2 {
			span := spans[i]
			if strings.Contains(span, ".") && !strings.ContainsAny(span, " {}[]") {
				if _, ok := capabilityHandlers[span]; !ok {
					t.Errorf("PLUGIN_API.md lists %s, which is not published", span)
				}
			}
		}
	}
}
