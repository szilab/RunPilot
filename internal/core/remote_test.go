package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyXpraTargetGetsDisplayDefaultsWithoutConfigRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runpilot.yaml")
	legacy := `version: 1
server: { bind: 127.0.0.1:9070, token: existing-token }
remoteTargets:
  - id: legacy
    name: Legacy Xpra
    provider: xpra
    type: application
    enabled: true
    command: { path: xterm }
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	controller, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	targets := controller.RemoteTargets()
	if len(targets) != 1 || targets[0].Xpra == nil || targets[0].Xpra.Encoding != "webp" || targets[0].Xpra.Video == nil || *targets[0].Xpra.Video || targets[0].Xpra.LaunchAfterConnect == nil || !*targets[0].Xpra.LaunchAfterConnect {
		t.Fatalf("legacy target was not normalized: %#v", targets)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if containsXpraSection(string(contents)) {
		t.Fatalf("legacy target display defaults were persisted before editing: %s", contents)
	}
}

func containsXpraSection(value string) bool {
	for _, line := range strings.Split(value, "\n") {
		if line == "  xpra:" {
			return true
		}
	}
	return false
}
