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
		`data-page="software"`,
		`data-page="terminal"`,
		`id="terminalPage"`,
		`id="terminalTabs"`,
		`vendor/xterm/xterm.js`,
		`vendor/xterm/addon-fit.js`,
		`id="softwarePage"`,
		`id="softwareProviderSelect"`,
		`id="softwareProviderCard"`,
		`data-software-tab="buckets"`,
		`id="softwareBucketBar"`,
		`Server automation`,
		`/opt/runpilot/myapp or C:\Tools\myapp.exe`,
		`/srv/data or D:\Data`,
		`value="sh"`,
		`value="bash"`,
		`class="row-list"`,
		`type="button" data-dismiss="processDialog"`,
		`type="button" data-dismiss="jobDialog"`,
		`type="button" data-dismiss="backupDialog"`,
		`id="backupProvider"`,
		`id="resticFields"`,
		`id="rdiffFields"`,
		`for="storageProvider"`,
		`class="storage-provider-label">Provider</label>`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("index.html does not contain %q", want)
		}
	}
	if strings.Contains(page, "refreshBtn") || strings.Contains(page, "card-grid") {
		t.Fatal("index.html still exposes manual refresh or card layout")
	}
	if strings.Contains(page, `id="storageUp"`) {
		t.Fatal("index.html still exposes top-level storage parent navigation")
	}
	if _, err := staticFS.ReadFile("static/runpilot-logo.png"); err != nil {
		t.Fatalf("embedded application logo is missing: %v", err)
	}
	for _, asset := range []string{"static/vendor/xterm/xterm.js", "static/vendor/xterm/addon-fit.js", "static/vendor/xterm/xterm.css", "static/vendor/xterm/LICENSE-xterm.txt"} {
		if _, err := staticFS.ReadFile(asset); err != nil {
			t.Fatalf("embedded terminal asset %s is missing: %v", asset, err)
		}
	}

	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(app)
	for _, want := range []string{"function startAutoRefresh", "[data-dismiss]", "function renderOverview", "function renderSoftware", "function softwarePackageFacts", "function loadSoftwareView", "function changeSoftwareProvider", "function softwareAddBucket", "software-protected-action", "softwareProviderSelect", "softwareLoading", "No software providers available", "api/v1/software/providers", "/buckets", "api/v1/overview", "function updateBackupProvider", "function configurePlatformAwareFields", "case-insensitive platforms", "Scoop root on Windows", `class="row"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("app.js does not contain %q", want)
		}
	}
	if !strings.Contains(script, "function applyTheme") {
		t.Fatal("app.js does not support theme selection")
	}
	for _, want := range []string{"function loadTerminalInfo", "function terminalWebSocketURL", "api/v1/terminal/ticket", "new WebSocket", "FitAddon.FitAddon", `new URL("api/v1/terminal/connect", document.baseURI)`, `wsURL.searchParams.set("ticket", ticket)`} {
		if !strings.Contains(script, want) {
			t.Fatalf("app.js does not contain terminal integration %q", want)
		}
	}
	if strings.Contains(script, "ticket.url") {
		t.Fatal("terminal WebSocket must be derived from document.baseURI, not a server-provided URL")
	}
	if strings.Contains(page+script, "cdn.jsdelivr") || strings.Contains(page+script, "unpkg.com") {
		t.Fatal("terminal assets must not load from a CDN")
	}
	if strings.Contains(script, "Helyi fájlrendszer kezelése.") {
		t.Fatal("app.js still exposes the obsolete storage subtitle")
	}
	if !strings.Contains(script, `title.textContent=".."`) || !strings.Contains(script, `meta.textContent="parent folder"`) {
		t.Fatal("app.js does not render parent navigation as a folder entry")
	}
}
