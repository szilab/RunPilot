package web

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostDashboardIsTheOnlyVisibleOverviewSurface(t *testing.T) {
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	shell, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `id="hostDashboard"`) || strings.Contains(string(page), `id="overviewMetrics"`) {
		t.Fatal("Overview still mounts the plugin widget grid")
	}
	if !strings.Contains(string(shell), `new RunPilotOverviewWidgets(document.createElement("div"))`) || strings.Contains(string(shell), "overviewWidgets.setActive(") {
		t.Fatal("plugin widgets are active on the host dashboard")
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
