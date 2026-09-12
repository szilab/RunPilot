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
		`id="softwarePage"`,
		`id="softwareProviderSelect"`,
		`id="softwareProviderCard"`,
		`data-software-tab="buckets"`,
		`id="softwareBucketBar"`,
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

	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(app)
	for _, want := range []string{"function startAutoRefresh", "[data-dismiss]", "function renderOverview", "function renderSoftware", "function softwarePackageFacts", "function loadSoftwareView", "function changeSoftwareProvider", "function softwareAddBucket", "software-protected-action", "softwareProviderSelect", "softwareLoading", "No software providers available", "api/v1/software/providers", "/buckets", "api/v1/overview", "function updateBackupProvider", `class="row"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("app.js does not contain %q", want)
		}
	}
	if !strings.Contains(script, "function applyTheme") {
		t.Fatal("app.js does not support theme selection")
	}
	if strings.Contains(script, "Helyi fájlrendszer kezelése.") {
		t.Fatal("app.js still exposes the obsolete storage subtitle")
	}
	if !strings.Contains(script, `title.textContent=".."`) || !strings.Contains(script, `meta.textContent="parent folder"`) {
		t.Fatal("app.js does not render parent navigation as a folder entry")
	}
}
