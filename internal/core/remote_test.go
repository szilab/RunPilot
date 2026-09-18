package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestGuacdConfigurationDefaultsAndPersistence(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := c.GuacdConfig(); got.Host != "127.0.0.1" || got.Port != 4822 || got.ConnectTimeoutSeconds != 5 {
		t.Fatalf("defaults=%#v", got)
	}
	value, err := c.UpdateGuacdConfig(model.GuacdConfig{Host: "guacd", Port: 4823, ConnectTimeoutSeconds: 7})
	if err != nil || value.Host != "guacd" || c.Snapshot().Remote.Guacd.Port != 4823 {
		t.Fatalf("persist=%#v err=%v", value, err)
	}
}

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
