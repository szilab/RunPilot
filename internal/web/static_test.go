package web

import (
	"strings"
	"testing"
)

func TestPluginSettingsUseSharedSectionsAndNamedNavigationIcons(t *testing.T) {
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`pluginNavigationIcon(entry.icon)`, `icon === "monitor"`, `viewBox="0 0 24 24" aria-hidden="true"`, `section.className = "plugin-settings-section"`, `data-plugin-id="${escapeHtml(id)}"`, `if (!installed.get(entry.extension)?.enabled) continue;`, `const card = cards.get(entry.extension);`, `card.append(section);`} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("shared plugin presentation is missing %q", want)
		}
	}
	for _, removed := range []string{"Latest published", "Latest compatible", "Local/manual"} {
		if strings.Contains(string(app), removed) {
			t.Fatalf("plugin cards retain redundant version details %q", removed)
		}
	}
	styles, err := staticFS.ReadFile("static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`grid-template-columns:repeat(auto-fill,minmax(min(100%,400px),1fr))`, `.plugin-settings-section > form`, `.plugin-settings-section { grid-column:1 / -1;`, `.plugin-settings-section .form-grid { grid-template-columns:minmax(0,1fr); }`, `.plugin-settings-grid .plugin-setting-state { white-space:normal`, `.plugin-settings-grid .plugin-setting-actions { grid-column:2; grid-row:1;`, `.plugin-settings-section .plugin-settings-actions { display:flex; align-items:center; justify-content:flex-end;`, `.plugin-settings-section .plugin-settings-actions button[type="submit"] { order:1; }`, `.plugin-settings-footer > .plugin-settings-actions { grid-column:2; grid-row:1; }`} {
		if !strings.Contains(string(styles), want) {
			t.Fatalf("shared settings layout is missing %q", want)
		}
	}
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index)+string(app), "pluginFeatureSettings") {
		t.Fatal("plugin settings still use a separate area outside the cards")
	}
}

func TestPluginNavigationCanRenderActionsBesideThemeSwitcher(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	theme := strings.Index(page, `id="themeToggle"`)
	actions := strings.Index(page, `id="pluginHeaderActions"`)
	if theme < 0 || actions < 0 || actions < theme {
		t.Fatal("plugin header action slot must follow the theme switcher")
	}
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"typeof entry.headerActions !== \"function\"", "registered?.headerActions", "headerActions.replaceChildren()", "headerActions.classList.toggle(\"hidden\", headerActions.childElementCount === 0)"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("plugin header action host is missing %q", want)
		}
	}
	styles, err := staticFS.ReadFile("static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(styles), ".plugin-header-actions { display: contents; }") {
		t.Fatal("plugin header actions do not share the application header row")
	}
}

func TestSecureWebSocketDefersApplicationTrafficUntilHandshakeCompletes(t *testing.T) {
	source, err := staticFS.ReadFile("static/secure-websocket.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)
	for _, want := range []string{
		"this._ready=false",
		"return this._ready || this.mode === \"disabled\" ? WebSocket.OPEN : WebSocket.CONNECTING",
		"if(this._ready)this._receive(event)",
		"this._ready=true; this.onopen?.()",
		"const sequence=this.sendSequence, header=concat(TAG",
		"this.sendSequence++",
		"}).catch(error=>this._reportError(error));",
		"Secure WebSocket requires HTTPS and Web Crypto",
		"kind===\"text\"?textDecoder.decode(plain):plain",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("secure WebSocket regression protection is missing %q", want)
		}
	}
	for _, removed := range []string{
		"this.readyState=WebSocket.CLOSED",
		"this.sendChain=this.sendChain.catch(()=>{})",
		"kind===\"text\"?textDecoder.decode(plain):plain.buffer",
	} {
		if strings.Contains(script, removed) {
			t.Fatalf("secure WebSocket retains broken behavior %q", removed)
		}
	}
}

func TestInteractiveSessionViewOwnsOnlyGenericSurfaceBehavior(t *testing.T) {
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(app)
	start := strings.Index(script, "function createInteractiveSessionView(")
	endOffset := -1
	if start >= 0 {
		endOffset = strings.Index(script[start:], "\nfunction pluginUI()")
	}
	end := -1
	if endOffset >= 0 {
		end = start + endOffset
	}
	if start < 0 || end <= start {
		t.Fatal("public interactive session view is missing")
	}
	view := script[start:end]
	for _, want := range []string{"actions,setStatus", "onResize(listener)", "fitScale(content)", "enterFullscreen", "exitFullscreen", "onFullscreenChange(listener)", "setStatus(state,label)", "setLoading(value", "setError(message", "ResizeObserver", "requestAnimationFrame", "setTimeout(reportSize,60)", "requestFullscreen", "fullscreenchange", "removeEventListener", "dispose()"} {
		if !strings.Contains(view, want) {
			t.Fatalf("interactive session view is missing %q", want)
		}
	}
	for _, forbidden := range []string{"Guacamole", "RFB", "Xpra", "rdp.session", "vnc."} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("interactive session view contains provider-specific behavior %q", forbidden)
		}
	}
}

func TestLegacyTerminalUIIsRemoved(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{`id="terminalPage"`, `id="terminalNav"`, `id="terminalShell"`, `api/v1/terminal/ticket`, `api/v1/terminal/connect`} {
		if strings.Contains(string(index)+string(app), removed) {
			t.Fatalf("legacy terminal UI still contains %q", removed)
		}
	}
}

func TestSidebarWarnsWhenHTTPDisablesWebSocketPayloadEncryption(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `id="connectionWarning"`) || !strings.Contains(string(index), `aria-label="HTTP connection: WebSocket payload encryption is disabled."`) {
		t.Fatal("sidebar HTTP transport warning is missing")
	}
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `$("connectionWarning").classList.toggle("hidden", location.protocol !== "http:")`) {
		t.Fatal("sidebar HTTP transport warning is not controlled by page scheme")
	}
	styles, err := staticFS.ReadFile("static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(styles), `.shell.sidebar-collapsed .side-foot, .shell.sidebar-collapsed .connection-warning { width: 40px; height: 40px;`) || !strings.Contains(string(styles), `position: absolute; inset: 0; display: grid; place-items: center;`) {
		t.Fatal("collapsed sidebar warning treatment is missing")
	}
}

func TestStaticUIUsesRowsAndAutomaticRefresh(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	for _, want := range []string{
		`data-page="overview"`,
		`id="sidebarToggle"`,
		`<rect x="3" y="4" width="18" height="16" rx="2"/>`,
		`class="nav-icon"`,
		`class="nav-label"`,
		`id="overviewPage"`,
		`runpilot-logo.png`,
		`id="themeToggle"`,
		`data-page="software"`,
		`vendor/xterm/xterm.js`,
		`vendor/xterm/addon-fit.js`,
		`id="softwarePage"`,
		`id="softwareProviderSelect"`,
		`id="softwareProviderCard"`,
		`data-software-tab="buckets"`,
		`id="softwareBucketBar"`,
		`Server automation`,
		`/opt/runpilot/myapp, C:\Tools\myapp.exe, or ls -la /`,
		`/srv/data or D:\Data`,
		`value="sh"`,
		`value="sh-inline"`,
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
		`class="docker-resources-grid"`,
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
	for _, removed := range []string{`id="pageSubtitle"`, `Active sessions`, `id="remoteSessions"`, `remote-provider-add`} {
		if strings.Contains(page+script, removed) {
			t.Fatalf("UI still contains obsolete presentation %q", removed)
		}
	}
	for _, want := range []string{"function startAutoRefresh", "[data-dismiss]", "function renderOverview", "function renderTasks", "function renderSoftware", "function softwarePackageFacts", "function loadSoftwareView", "function changeSoftwareProvider", "function softwareAddBucket", "software-protected-action", "softwareProviderSelect", "softwareLoading", "No software providers available", "api/v1/software/providers", "/buckets", "api/v1/overview", "function updateBackupProvider", "function configurePlatformAwareFields", "case-insensitive platforms", "dockerProjectErrors", "Compose operation failed", "const projectColumns = [[], []];", "docker-project-column", "const canUp = ready && hasCompose && !busy;", "const canDelete = ready && !volume.inUse && !busy", "const deleteAction = volume.inUse ? \"\"", "const composeManaged=!!network.composeProject", "docker-resource-action-slot", "docker-resource-row", "function openDockerVolumeStorage", "storagePathCapabilities=listing.capabilities", "setStorageEntryLoading", "function initializeSidebar", "sidebarStorageKey", `class="row"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("app.js does not contain %q", want)
		}
	}
	for _, want := range []string{`$("sidebarToggle").setAttribute("aria-label", label)`} {
		if !strings.Contains(script, want) {
			t.Fatalf("UI polish behavior is missing %q", want)
		}
	}
	if !strings.Contains(script, "function applyTheme") {
		t.Fatal("app.js does not support theme selection")
	}
	for _, want := range []string{"function loadSystemInfo", "function dockerAttachWebSocketURL", "api/v1/docker/containers/", "new RunPilotSecureWebSocket", "FitAddon.FitAddon", `new URL("api/v1/docker/attach", document.baseURI)`, `wsURL.searchParams.set("ticket", ticket)`, "theme: Object.freeze({", "subscribe: listener =>"} {
		if !strings.Contains(script, want) {
			t.Fatalf("app.js does not contain shared UI or Docker attach integration %q", want)
		}
	}
	if strings.Contains(script, "ticket.url") {
		t.Fatal("Docker attach WebSocket must be derived from document.baseURI, not a server-provided URL")
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
	for _, want := range []string{".docker-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); align-items:start;", ".docker-project-column { display:grid; align-content:start; gap:12px;", ".docker-resources-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr));", ".docker-container { display: grid; grid-template-columns: 10px minmax(0, 1fr) auto minmax(150px, .7fr);", ".docker-resource-row { min-height:42px;", ".nav-label { overflow:hidden; text-overflow:ellipsis; }", ".sidebar-toggle { display:flex; align-items:center; gap:12px; width:100%; height:42px; min-height:42px; overflow:hidden;", ".docker-resource-actions { display:grid; grid-template-columns:108px 78px;"} {
		if !strings.Contains(string(styles), want) {
			t.Fatalf("compact responsive UI treatment is missing %q", want)
		}
	}
	if strings.Contains(script, "Helyi fájlrendszer kezelése.") {
		t.Fatal("app.js still exposes the obsolete storage subtitle")
	}
	if !strings.Contains(script, `title.textContent=".."`) || !strings.Contains(script, `meta.textContent="parent folder"`) {
		t.Fatal("app.js does not render parent navigation as a folder entry")
	}
}

func TestLegacyRemoteNavigationAndFeatureAssetsAreRemoved(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{`data-page="remote"`, `id="remotePage"`, "Remote Access", "api/v1/remote/targets", "RunPilotNoVNC", "Guacamole", "rdp.session", "vnc.session"} {
		if strings.Contains(string(index)+string(app), removed) {
			t.Fatalf("legacy combined Remote UI remains: %q", removed)
		}
	}
	for _, asset := range []string{"static/novnc.js", "static/vendor/novnc/core/rfb.js", "static/vendor/guacamole/guacamole-common-js-1.6.0.min.js"} {
		if _, err := staticFS.ReadFile(asset); err == nil {
			t.Fatalf("legacy core remote asset remains: %s", asset)
		}
	}
}

func TestErrorToastIsProminentAndPersistent(t *testing.T) {
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"function toastError", `level === "error" ? "Error" : "Notice"`, "level === \"error\" ? 10000 : 4000"} {
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
