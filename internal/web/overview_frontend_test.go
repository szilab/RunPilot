package web

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOverviewFrontendContracts(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	cmd := exec.Command("node", "internal/web/static/overview.test.cjs")
	cmd.Dir = filepath.Join("..", "..")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Overview frontend: %v\n%s", err, output)
	}
	plugins := exec.Command("node", "--test", "plugins/tasks/web/plugin.test.mjs", "plugins/docker/web/plugin.test.mjs")
	plugins.Dir = filepath.Join("..", "..")
	if output, err := plugins.CombinedOutput(); err != nil {
		t.Fatalf("Overview plugin frontend: %v\n%s", err, output)
	}
}
