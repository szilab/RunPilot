package web

import (
	"strings"
	"testing"
)

func TestStaticUIUsesRowsAndAutomaticRefresh(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	for _, want := range []string{
		`data-page="overview"`,
		`id="overviewPage"`,
		`runpilot-logo.png`,
		`id="themeToggle"`,
		`class="row-list"`,
		`type="button" data-dismiss="processDialog"`,
		`type="button" data-dismiss="jobDialog"`,
		`type="button" data-dismiss="backupDialog"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("index.html does not contain %q", want)
		}
	}
	if strings.Contains(page, "refreshBtn") || strings.Contains(page, "card-grid") {
		t.Fatal("index.html still exposes manual refresh or card layout")
	}
	if _, err := staticFS.ReadFile("static/runpilot-logo.png"); err != nil {
		t.Fatalf("embedded application logo is missing: %v", err)
	}

	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(app)
	for _, want := range []string{"function startAutoRefresh", "[data-dismiss]", "function renderOverview", "api/v1/overview", `class="row"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("app.js does not contain %q", want)
		}
	}
	if !strings.Contains(script, "function applyTheme") {
		t.Fatal("app.js does not support theme selection")
	}
}
