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
		`id="sidebarToggle"`,
		`class="nav-icon"`,
		`class="nav-label"`,
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
		`class="storage-provider-label">Location</label>`,
		`id="storageNewFile"`,
		`data-page="tasks"`,
		`id="tasksPage"`,
		`data-task-kind="continuous"`,
		`class="row task-choice"`,
		`id="remoteXpraSettings"`,
		`id="remoteXpraProfile"`,
		`id="remoteXpraDPIMode"`,
		`id="remoteXpraLaunchAfterConnect"`,
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
	for _, removed := range []string{`data-page="processes"`, `data-page="jobs"`, `data-page="backups"`, `data-page="history"`} {
		if strings.Contains(page, removed) {
			t.Fatalf("index.html still contains obsolete navigation %s", removed)
		}
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
	for _, want := range []string{"function startAutoRefresh", "[data-dismiss]", "function renderOverview", "function renderTasks", "function renderSoftware", "function softwarePackageFacts", "function loadSoftwareView", "function changeSoftwareProvider", "function softwareAddBucket", "software-protected-action", "softwareProviderSelect", "softwareLoading", "No software providers available", "api/v1/software/providers", "/buckets", "api/v1/overview", "function updateBackupProvider", "function configurePlatformAwareFields", "case-insensitive platforms", "Scoop root on Windows", "dockerProjectErrors", "Compose operation failed", "const canUp = ready && hasCompose && !busy;", "const canDelete = ready && !volume.inUse && !busy", "storagePathCapabilities=listing.capabilities", "setStorageEntryLoading", "function openNewTextFile", `$("textEditorContent").readOnly=!canEdit`, "function initializeSidebar", "sidebarStorageKey", "function remoteXpraDefaults", "function focusRemoteClient", `class="row"`} {
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
	styles, err := staticFS.ReadFile("static/styles.css")
	if err != nil || !strings.Contains(string(styles), ".docker-card { display: grid; align-content: start;") {
		t.Fatal("Docker project cards do not keep empty-project controls top aligned")
	}
	if !strings.Contains(string(styles), `:root[data-theme="dark"] .task-choice { background: #1d2a3e; color: #f8fafc; }`) {
		t.Fatal("task selector does not have a dark-theme color treatment")
	}
	if !strings.Contains(string(styles), ".shell.sidebar-collapsed") || !strings.Contains(string(styles), "main { padding: 17px 21px 24px;") {
		t.Fatal("sidebar collapse or reduced main padding styles are missing")
	}
	if strings.Contains(script, "Helyi fájlrendszer kezelése.") {
		t.Fatal("app.js still exposes the obsolete storage subtitle")
	}
	if !strings.Contains(script, `title.textContent=".."`) || !strings.Contains(script, `meta.textContent="parent folder"`) {
		t.Fatal("app.js does not render parent navigation as a folder entry")
	}
}

func TestRDPPasswordIsOptionalAndTunnelErrorsAreUseful(t *testing.T) {
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "Password (optional)") || strings.Contains(string(page), `id="remoteRDPConnectPassword" type="password" autocomplete="current-password" required`) {
		t.Fatal("RDP password remains required")
	}
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"function guacamoleTunnelError", "Could not establish tunnel to guacd", "(519)", "function remoteTransportParams", "client.connect(transportParams)"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("missing tunnel error handling %q", want)
		}
	}
	if strings.Contains(string(app), "client.connect();") {
		t.Fatal("Guacamole tunnel must receive its query parameters through client.connect")
	}
}

func TestRemoteProviderCardsShowAvailabilityAndHints(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `id="remoteProviders" class="remote-provider-grid"`) {
		t.Fatal("remote providers do not use the compact card grid")
	}
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"function remoteProviderHint", "function remoteProviderCard", "provider.installHint", "class=\"provider-info\""} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("remote provider UI is missing %q", want)
		}
	}
	styles, err := staticFS.ReadFile("static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".remote-provider-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr));", ".provider-info::after", "@media (max-width: 1100px)"} {
		if !strings.Contains(string(styles), want) {
			t.Fatalf("remote provider card styles are missing %q", want)
		}
	}
}

func TestRemoteTargetDialogIsProviderSpecific(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	for _, want := range []string{
		`id="remoteTargetProvider" value="xpra"`,
		`id="remoteXpraSettings" class="remote-xpra-advanced span-2 remote-xpra-only"`,
		`<summary>Advanced settings</summary>`,
		`id="remoteRDPUsername" autocomplete="username" placeholder="DOMAIN\username"`,
		`id="remoteRDPLayout"`,
		`hu-hu-qwertz`,
		`id="remoteRDPConnectUsername" autocomplete="username" placeholder="DOMAIN\username"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("provider-specific remote target UI is missing %q", want)
		}
	}
	for _, removed := range []string{`id="remoteTargetEnabled"`, `id="remoteRDPDomain"`, `id="remoteRDPConnectDomain"`, "Xpra display", "Optimized for browser-based administration and desktop applications.", "Clipboard temporarily unavailable"} {
		if strings.Contains(page, removed) {
			t.Fatalf("obsolete remote target UI remains: %q", removed)
		}
	}

	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(app)
	for _, want := range []string{"function splitRDPUsername", "function formatRDPUsername", "openRemoteTarget(null,'", "type:isRDP ? \"desktop\""} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote target script is missing %q", want)
		}
	}
	if strings.Contains(script, "remoteTargetEnabled") || strings.Contains(script, "remoteRDPDomain") {
		t.Fatal("remote target script still uses removed controls")
	}
}

func TestGuacamoleAssetAndRDPUIAreEmbedded(t *testing.T) {
	asset, err := staticFS.ReadFile("static/vendor/guacamole/guacamole-common-js-1.6.0.min.js")
	if err != nil || !strings.Contains(string(asset), "Guacamole") {
		t.Fatalf("Guacamole asset missing: %v", err)
	}
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"guacamole-common-js-1.6.0.min.js", `type="password"`, `id="guacdSettingsDialog"`, `id="guacdHost"`, `id="guacdPort"`, `id="guacdTLS"`, `id="guacdTimeout"`, `id="guacdTest"`, `data-dismiss="guacdSettingsDialog"`, `id="remoteRDPLayout"`, `id="remoteRDPResize"`} {
		if !strings.Contains(string(page), want) {
			t.Fatalf("RDP UI missing %q", want)
		}
	}
	if strings.Contains(string(page), "<section class=\"overview-section\"><div class=\"section-head\"><h2>RDP / Guacamole settings</h2>") {
		t.Fatal("guacd settings remain inline")
	}
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"function openGuacdSettings", `openGuacdSettings()">Settings`, "body:JSON.stringify(value)"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("guacd dialog behavior missing %q", want)
		}
	}
}

func TestErrorToastIsProminentAndPersistent(t *testing.T) {
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"function toastError", `level === "error" ? "Error" : "Notice"`, "level === \"error\" ? 10000 : 4000", "toastError(`Remote session unavailable:"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("error toast behavior is missing %q", want)
		}
	}
	styles, err := staticFS.ReadFile("static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".toast-error", ".toast-close", "z-index:50"} {
		if !strings.Contains(string(styles), want) {
			t.Fatalf("error toast styling is missing %q", want)
		}
	}
}
