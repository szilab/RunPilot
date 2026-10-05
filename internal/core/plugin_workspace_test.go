package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/plugins"
)

func wsCall(t *testing.T, c *Controller, owner, op, path string, extra map[string]any) (json.RawMessage, error) {
	t.Helper()
	in := map[string]any{"path": path}
	for k, v := range extra {
		in[k] = v
	}
	raw, _ := json.Marshal(in)
	return (controllerPluginHost{c}).Workspace(context.Background(), owner, op, raw)
}
func TestPluginWorkspaceIsolationAndBounds(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	if _, err := wsCall(t, c, "alpha", "mkdir", "projects/a", nil); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString([]byte("hello"))
	if _, err := wsCall(t, c, "alpha", "write", "projects/a/compose.yaml", map[string]any{"data": data}); err != nil {
		t.Fatal(err)
	}
	raw, err := wsCall(t, c, "alpha", "read", "projects/a/compose.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), data) {
		t.Fatalf("read: %s", raw)
	}
	if _, err := wsCall(t, c, "beta", "read", "projects/a/compose.yaml", nil); err == nil {
		t.Fatal("cross-owner read succeeded")
	}
	for _, path := range []string{"../beta", "/tmp/outside", "projects/../beta", "projects\\a", "C:/outside"} {
		if _, err := wsCall(t, c, "alpha", "write", path, map[string]any{"data": data}); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(c.dataDir, "plugins", "alpha", "data", "workspace", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := wsCall(t, c, "alpha", "write", "escape/file", map[string]any{"data": data}); err == nil {
		t.Fatal("symlink escape succeeded")
	}
	if _, err := wsCall(t, c, "alpha", "remove", "escape", map[string]any{"recursive": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("recursive deletion escaped root", err)
	}
	if _, err := wsCall(t, c, "alpha", "write", "projects/a/large", map[string]any{"data": base64.StdEncoding.EncodeToString(make([]byte, maxWorkspaceFile+1))}); err == nil {
		t.Fatal("oversized write succeeded")
	}
	if _, err := wsCall(t, c, "alpha", "remove", "projects", map[string]any{"recursive": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := wsCall(t, c, "alpha", "stat", "projects", nil); err == nil {
		t.Fatal("recursive remove failed")
	}
}
func TestLegacyDockerProjectMigration(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "compose", "jellyfin")
	if err := os.MkdirAll(filepath.Join(source, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "compose.yaml"), []byte("services: {one: {}}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "config", "settings.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyDockerProjects(dir); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "plugins", "docker", "data", "workspace", "projects", "jellyfin")
	data, err := os.ReadFile(filepath.Join(dest, "compose.yaml"))
	if err != nil || string(data) != "services: {one: {}}" {
		t.Fatalf("migration: %s %v", data, err)
	}
	origin, err := os.ReadFile(filepath.Join(dest, ".runpilot-legacy-origin"))
	if err != nil || string(origin) != source {
		t.Fatalf("migration origin: %s %v", origin, err)
	}
	if err := os.WriteFile(filepath.Join(dest, "compose.yaml"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyDockerProjects(dir); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dest, "compose.yaml"))
	if err != nil || string(data) != "new" {
		t.Fatal("migration overwrote plugin file")
	}
	data, err = os.ReadFile(filepath.Join(source, "compose.yaml"))
	if err != nil || string(data) != "services: {one: {}}" {
		t.Fatal("migration changed source")
	}
}
func TestWorkspaceHostFailureCodes(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	_, err := wsCall(t, c, "alpha", "read", "missing", nil)
	var failure *plugins.HostFailure
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = failure
}
