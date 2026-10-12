package web

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoreShellLeavesFeatureNavigationToPlugins(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	for _, id := range []string{"tasks", "storage", "software", "docker"} {
		if strings.Contains(page, `data-page="`+id+`"`) {
			t.Errorf("legacy %s navigation is exposed", id)
		}
	}
	for _, id := range []string{"overview", "settings"} {
		if !strings.Contains(page, `data-page="`+id+`"`) {
			t.Errorf("core %s navigation is missing", id)
		}
	}
	if strings.Contains(page, `id="tasksPage"`) || strings.Contains(page, `id="legacyTasksPage"`) {
		t.Error("legacy Tasks page remains in the shell")
	}
	for _, id := range []string{"storage", "software"} {
		if strings.Contains(page, `id="`+id+`Page"`) || !strings.Contains(page, `id="legacy`+strings.ToUpper(id[:1])+id[1:]+`Page"`) {
			t.Errorf("legacy %s markup reserves the plugin page ID", id)
		}
	}
	if strings.Contains(page, `id="legacyDockerPage"`) || strings.Contains(page, `id="dockerPage"`) {
		t.Error("core still reserves the Docker plugin page")
	}
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(app)
	for _, want := range []string{
		`document.querySelector("nav").append(button)`,
		`pluginNavigation.set(entry.id, { ...entry, button, page, extension })`,
		`if (!Object.hasOwn(pageMeta, page) && !registered) return;`,
		`if (pluginSnapshot && JSON.stringify(pluginSnapshot) !== lastPluginSnapshot)`,
		`if (frontendSnapshot) await loadPluginExtensions(frontendSnapshot);`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("plugin shell behavior missing %q", want)
		}
	}
	if strings.Contains(script, `$("dockerNav")`) {
		t.Error("Docker capability can re-enable legacy navigation")
	}
	refreshStart := strings.Index(script, "async function refresh() {")
	refreshEnd := strings.Index(script, "async function loadPluginExtensions(")
	if refreshStart < 0 || refreshEnd <= refreshStart {
		t.Fatal("shell refresh or plugin loader is missing")
	}
	refresh := script[refreshStart:refreshEnd]
	for _, path := range []string{"api/v1/processes", "api/v1/jobs", "api/v1/runs", "api/v1/storage", "api/v1/overview", "api/v1/docker"} {
		if strings.Contains(refresh, path) {
			t.Errorf("shell refresh still loads legacy %s", path)
		}
	}
	pageSwitchStart := strings.Index(script, "function setPage(page) {")
	pageSwitchEnd := strings.Index(script, `document.querySelectorAll(".nav").forEach`)
	if pageSwitchStart < 0 || pageSwitchEnd <= pageSwitchStart {
		t.Fatal("page switcher is missing")
	}
	pageSwitch := script[pageSwitchStart:pageSwitchEnd]
	for _, old := range []string{`page === "docker"`, `page === "software"`} {
		if strings.Contains(pageSwitch, old) {
			t.Errorf("legacy page switcher still matches %s", old)
		}
	}
	plugin, err := os.ReadFile(filepath.Join("..", "..", "plugins", "tasks", "web", "plugin.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plugin), `id: "tasks", title: "Tasks"`) || strings.Contains(string(plugin), `id: "tasks-plugin"`) {
		t.Error("Tasks plugin does not own the normal Tasks page")
	}
}

func TestFirstPartyPluginNavigationImages(t *testing.T) {
	for _, name := range []string{"remote", "tasks", "terminal", "web-apps", "docker"} {
		pluginDir := filepath.Join("..", "..", "plugins", name, "web")
		source, err := os.ReadFile(filepath.Join(pluginDir, "plugin.js"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(source), `icon: { src: new URL("./icon.svg", import.meta.url).href }`) && !strings.Contains(string(source), `icon:{src:new URL("./icon.svg",import.meta.url).href}`) {
			t.Errorf("%s navigation does not reference its packaged icon", name)
		}
		asset, err := os.ReadFile(filepath.Join(pluginDir, "icon.svg"))
		if err != nil {
			t.Fatal(err)
		}
		var icon struct {
			XMLName xml.Name `xml:"svg"`
			ViewBox string   `xml:"viewBox,attr"`
		}
		if err := xml.Unmarshal(asset, &icon); err != nil || icon.XMLName.Local != "svg" || icon.ViewBox != "0 0 24 24" {
			t.Errorf("%s has an invalid sidebar SVG: %v", name, err)
		}
	}
}

func TestPluginSettingsUseSharedDialogsAndNamedNavigationIcons(t *testing.T) {
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`pluginNavigationIcon(entry.icon)`, `icon === "monitor"`, `viewBox="0 0 24 24" aria-hidden="true"`, `section.className = "plugin-settings-section"`, `data-plugin-id="${escapeHtml(id)}"`, `function openPluginSettings(id)`, `dialog.className = "dialog plugin-settings-dialog"`, `const settingsEntries=[...pluginSettings.values()].filter(item=>item.extension===id);`} {
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
	for _, want := range []string{`.rp-masonry { columns:var(--rp-masonry-columns,2);`, `.plugin-settings-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr));`, `.plugin-settings-section > form`, `.plugin-settings-section { grid-column:1 / -1;`, `.plugin-settings-section .form-grid { grid-template-columns:minmax(0,1fr); }`, `.plugin-settings-grid .plugin-setting-version { white-space:normal`, `.plugin-settings-grid .plugin-setting-actions { grid-column:1; grid-row:2;`, `.plugin-settings-section .plugin-settings-actions { display:flex; align-items:center; justify-content:flex-end;`, `.plugin-settings-section .plugin-settings-actions button[type="submit"] { order:1; }`, `.plugin-settings-footer > .plugin-settings-actions { grid-column:1; grid-row:2; }`} {
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
		"this._fail(error);throw error",
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

func TestSidebarUsesConnectionIndicatorForPayloadEncryption(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{`id="connectionWarning"`, `id="sidebarHostName"`} {
		if strings.Contains(string(index), removed) {
			t.Fatalf("sidebar still contains %s", removed)
		}
	}
	for _, required := range []string{`id="connectionDot"`, `id="connectionText"`} {
		if !strings.Contains(string(index), required) {
			t.Fatalf("sidebar connection status is missing %s", required)
		}
	}
	styles, err := staticFS.ReadFile("static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(styles), `.dot.unencrypted {`) {
		t.Fatal("unencrypted connection indicator style is missing")
	}
	for _, want := range []string{
		`.shell.sidebar-collapsed .system-version { display: none; }`,
		`.shell.sidebar-collapsed .side-foot-label { position: absolute;`,
		`.shell.sidebar-collapsed .side-foot { justify-content: center; height: 40px; padding: 0; border: 0; background: transparent; }`,
		`.shell.sidebar-collapsed .side-foot .dot { flex: 0 0 8px; }`,
	} {
		if !strings.Contains(string(styles), want) {
			t.Fatalf("collapsed sidebar connection indicator is missing %q", want)
		}
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
		`id="legacySoftwarePage"`,
		`id="softwareProviderSelect"`,
		`id="softwareProviderCard"`,
		`data-software-tab="buckets"`,
		`id="softwareBucketBar"`,
		`class="row-list"`,
		`for="storageProvider"`,
		`class="storage-provider-label">Location</label>`,
		`id="storageNewFile"`,
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
	for _, removed := range []string{"legacyTasksPage", "function renderTasks", "api/v1/processes", "api/v1/jobs", "processDialog", "backupDialog", "runHistoryDialog"} {
		if strings.Contains(page+script, removed) {
			t.Errorf("legacy Tasks frontend remains: %s", removed)
		}
	}
	for _, removed := range []string{`id="pageSubtitle"`, `Active sessions`, `id="remoteSessions"`, `remote-provider-add`} {
		if strings.Contains(page+script, removed) {
			t.Fatalf("UI still contains obsolete presentation %q", removed)
		}
	}
	for _, want := range []string{"function startAutoRefresh", "[data-dismiss]", "hostDashboard.setActive", "function renderSoftware", "function softwarePackageFacts", "function loadSoftwareView", "function changeSoftwareProvider", "function softwareAddBucket", "software-protected-action", "softwareProviderSelect", "softwareLoading", "No software providers available", "/buckets", "storagePathCapabilities=listing.capabilities", "setStorageEntryLoading", "function initializeSidebar", "sidebarStorageKey"} {
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
	for _, want := range []string{"function loadSystemInfo", "new RunPilotSecureWebSocket", "theme: Object.freeze({", "subscribe: listener =>"} {
		if !strings.Contains(script, want) {
			t.Fatalf("app.js does not contain shared UI or Docker attach integration %q", want)
		}
	}
	for _, legacy := range []string{"legacyDockerPage", "function renderDocker", "api/v1/docker/attach", "function dockerAttachWebSocketURL"} {
		if strings.Contains(page+script, legacy) {
			t.Fatalf("legacy Docker UI remains: %s", legacy)
		}
	}
	if strings.Contains(page+script, "cdn.jsdelivr") || strings.Contains(page+script, "unpkg.com") {
		t.Fatal("terminal assets must not load from a CDN")
	}
	styles, err := staticFS.ReadFile("static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(styles), ".shell.sidebar-collapsed") || !strings.Contains(string(styles), "main { min-width: 0; padding: 28px clamp(20px, 3vw, 48px) 32px;") {
		t.Fatal("sidebar collapse or responsive main padding styles are missing")
	}
	for _, want := range []string{".nav-label { overflow:hidden; text-overflow:ellipsis; }", ".sidebar-toggle { display:flex; align-items:center; gap:12px; width:100%; height:42px; min-height:42px; overflow:hidden;"} {
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
