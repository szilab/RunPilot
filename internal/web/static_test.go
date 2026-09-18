package web

import (
	"strings"
	"testing"
)

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

func TestTerminalSessionIDsDoNotRequireCryptoRandomUUID(t *testing.T) {
	source, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)
	for _, want := range []string{
		"function terminalSessionID()",
		"typeof crypto.randomUUID === \"function\"",
		"crypto.getRandomValues(new Uint8Array(16))",
		"const id = terminalSessionID();",
		"error?.message || \"Terminal connection failed.\"",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("terminal UUID compatibility behavior is missing %q", want)
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
		`id="remoteTargetProvider"`,
		`id="rdpSettingsAction"`,
		`class="docker-resources-grid"`,
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
	for _, removed := range []string{`id="pageSubtitle"`, `Active sessions`, `id="remoteSessions"`, `remote-provider-add`} {
		if strings.Contains(page+script, removed) {
			t.Fatalf("UI still contains obsolete presentation %q", removed)
		}
	}
	for _, want := range []string{"function startAutoRefresh", "[data-dismiss]", "function renderOverview", "function renderTasks", "function renderSoftware", "function softwarePackageFacts", "function loadSoftwareView", "function changeSoftwareProvider", "function softwareAddBucket", "software-protected-action", "softwareProviderSelect", "softwareLoading", "No software providers available", "api/v1/software/providers", "/buckets", "api/v1/overview", "function updateBackupProvider", "function configurePlatformAwareFields", "case-insensitive platforms", "dockerProjectErrors", "Compose operation failed", "const projectColumns = [[], []];", "docker-project-column", "const canUp = ready && hasCompose && !busy;", "const canDelete = ready && !volume.inUse && !busy", "const deleteAction = volume.inUse ? \"\"", "const composeManaged=!!network.composeProject", "docker-resource-action-slot", "docker-resource-row", "function openDockerVolumeStorage", "storagePathCapabilities=listing.capabilities", "setStorageEntryLoading", "function initializeSidebar", "sidebarStorageKey", `rdpSettingsAction").classList.toggle("hidden", page !== "remote")`, "function remoteXpraDefaults", "function populateRemoteProviderSelect", "activeByTarget", "function focusRemoteClient", `class="row"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("app.js does not contain %q", want)
		}
	}
	for _, want := range []string{`remote: ["Remote Access", "Add target"]`, `$("sidebarToggle").setAttribute("aria-label", label)`, `onclick="openRemoteSession('${session.id}')"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("UI polish behavior is missing %q", want)
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
	for _, want := range []string{".docker-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); align-items:start;", ".docker-project-column { display:grid; align-content:start; gap:12px;", ".docker-resources-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr));", ".docker-container { display: grid; grid-template-columns: 10px minmax(0, 1fr) auto minmax(150px, .7fr);", ".docker-resource-row { min-height:42px;", ".nav-label { overflow:hidden; text-overflow:ellipsis; }", ".sidebar-toggle { display:flex; align-items:center; gap:12px; width:100%; height:42px; min-height:42px; overflow:hidden;", ".docker-resource-actions { display:grid; grid-template-columns:108px 78px;", "#remoteTargets, #remoteProviders { grid-template-columns:repeat(3,minmax(0,1fr)); }", ".remote-card-status, .remote-provider-status { display:flex;", ".remote-provider-card { display:grid; grid-template-columns:minmax(0,1fr) auto;", ".remote-target-session { display:flex;"} {
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
	for _, want := range []string{"function guacamoleTunnelError", "function guacamoleClientError", "Could not establish tunnel to guacd", "Guacamole client/RDP error", "client.onerror", "client.onstatechange", "Guacamole WebSocket tunnel error", `519:"Guacamole could not find the upstream RDP service"`, "function remoteTransportParams", "client.connect(transportParams)"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("missing tunnel error handling %q", want)
		}
	}
	if strings.Contains(string(app), "client.connect();") {
		t.Fatal("Guacamole tunnel must receive its query parameters through client.connect")
	}
}

func TestRDPClientSurfaceUsesFrameShellAndScopedCursor(t *testing.T) {
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(app)
	for _, want := range []string{
		"surface.classList.add(\"rdp-active\")",
		"classList.remove(\"rdp-active\")",
		"initialRDPSurfaceSize(frameShell)",
		"getSurfaceSize:()=>currentRDPSurfaceSize(frameShell)",
		"observer.observe(frameShell)",
		"window.addEventListener(\"resize\",surfaceChanged)",
		"layoutTransitionStarted:()=>sizing.layoutTransitionStarted()",
		"sizing.layoutTransitionFinished()",
		"remote RDP resize requested",
		"function instrumentGuacamoleResizeMessages",
		"Guacamole client emitted size",
		"remoteRDPInteraction?.surfaceChanged?.()",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("RDP client sizing is missing %q", want)
		}
	}
	for _, removed := range []string{"function inspectGuacamoleDisplay", "function instrumentGuacamoleDisplay", "browser received blob stream=", "Guacamole image decode promise rejected"} {
		if strings.Contains(script, removed) {
			t.Fatalf("temporary RDP display tracing remains: %q", removed)
		}
	}
	start := strings.Index(script, "function renderRemoteSession()")
	end := strings.Index(script[start:], "function closeRemoteSession()")
	if start < 0 || end < 0 {
		t.Fatal("remote session rendering functions are missing")
	}
	if strings.Contains(script[start:start+end], "replaceChildren()") {
		t.Fatal("periodic remote session rendering must not destroy the active Guacamole display")
	}

	styles, err := staticFS.ReadFile("static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".remote-rdp { display: block; width: 100%; height: 100%;", ".remote-rdp.rdp-active { display: grid; place-items: center; overflow: hidden; cursor: none; }", ".remote-rdp.rdp-active, .remote-rdp.rdp-active * { cursor: none; }", "main.remote-active .remote-frame-shell { flex: 1 1 0; height: 0; min-height: 0;"} {
		if !strings.Contains(string(styles), want) {
			t.Fatalf("active RDP display surface is not sized: %q", want)
		}
	}
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil || !strings.Contains(string(index), `src="rdp-sizing.js"`) {
		t.Fatal("RDP sizing controller is not embedded before the application")
	}
}

func TestRemoteProviderCardsShowAvailabilityAndHints(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `id="remoteProviders" class="docker-grid"`) {
		t.Fatal("remote providers do not use the Docker card grid")
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
	for _, want := range []string{".docker-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr));", ".provider-info::after", "@media (max-width: 1100px)"} {
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
		`id="remoteTargetProvider" aria-label="Remote provider"`,
		`id="remoteXpraSettings" class="remote-xpra-settings span-2 remote-xpra-only"`,
		`<summary>Advanced settings</summary>`,
		`id="remoteRDPUsername" autocomplete="username" placeholder="DOMAIN\username"`,
		`id="remoteRDPLayout"`,
		`hu-hu-qwertz`,
		`id="remoteRDPConnectUsername" autocomplete="username" placeholder="DOMAIN\username"`,
		`id="remoteVNCSettings"`,
		`id="remoteVNCHost"`,
		`id="remoteVNCCredentialsDialog"`,
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
	for _, want := range []string{"function splitRDPUsername", "function formatRDPUsername", "openRemoteTarget()", "isRDP||isVNC ? \"desktop\"", "remoteVNCCredentialsForm"} {
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
	for _, want := range []string{"function openGuacdSettings", `rdpSettingsAction").addEventListener("click", openGuacdSettings)`, "body:JSON.stringify(value)"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("guacd dialog behavior missing %q", want)
		}
	}
	if !strings.Contains(string(page), `id="rdpSettingsAction"`) || !strings.Contains(string(page), ">RDP settings</button>") {
		t.Fatal("RDP settings action is missing")
	}
}

func TestNoVNCAssetAndUIAreEmbedded(t *testing.T) {
	asset, err := staticFS.ReadFile("static/vendor/novnc/core/rfb.js")
	if err != nil || !strings.Contains(string(asset), "noVNC: HTML5 VNC client") {
		t.Fatalf("noVNC asset missing: %v", err)
	}
	pako, err := staticFS.ReadFile("static/vendor/novnc/vendor/pako/lib/zlib/inflate.js")
	if err != nil || !strings.Contains(string(pako), "inflate_fast") {
		t.Fatalf("noVNC pako dependency missing: %v", err)
	}
	cursor, err := staticFS.ReadFile("static/vendor/novnc/core/util/cursor.js")
	if err != nil || !strings.Contains(string(cursor), "scaledHotx") || !strings.Contains(string(cursor), "ctx.drawImage(this._sourceCanvas") {
		t.Fatalf("noVNC cursor scaling support missing: %v", err)
	}
	license, err := staticFS.ReadFile("static/vendor/novnc/LICENSE.txt")
	if err != nil || !strings.Contains(string(license), "MPL 2.0") {
		t.Fatalf("noVNC license missing: %v", err)
	}
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil || !strings.Contains(string(page), `src="novnc.js"`) || !strings.Contains(string(page), `id="remoteVNC"`) {
		t.Fatal("noVNC UI is not embedded")
	}
	app, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"function openVNCRemoteSession", "remoteVNCTransportURL", "credentialsrequired", "sendCredentials"} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("noVNC UI behavior missing %q", want)
		}
	}
	if strings.Contains(string(app), "pending.rfb.sendCredentials(credentials); credentials.password") {
		t.Fatal("noVNC credentials are cleared before its asynchronous authentication handshake")
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
