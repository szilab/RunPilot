package web

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverviewMountsHostAndPluginSurfaces(t *testing.T) {
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	shell, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `id="hostDashboard"`) || !strings.Contains(string(page), `id="overviewWidgets"`) {
		t.Fatal("Overview must provide host metrics and plugin widget containers")
	}
	if !strings.Contains(string(shell), `new RunPilotOverviewWidgets($("overviewWidgets"))`) || !strings.Contains(string(shell), `overviewWidgets.setActive(page === "overview")`) {
		t.Fatal("plugin widgets must follow the active Overview lifecycle")
	}
}

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
