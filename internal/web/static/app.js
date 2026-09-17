const $ = (id) => document.getElementById(id);
let token = localStorage.getItem("runpilot.token") || "";
let currentPage = "overview";
let processes = [];
let jobs = [];
let taskFilter = "all";
let storage = [], storageLocation = null, storagePath = "";
let softwareProviders = [], softwareProviderID = "", softwarePackages = [], softwareUpdates = [], softwareSearchResults = [], softwareBuckets = [], softwareTab = "installed", softwareBusy = false, softwareBusyLabel = "", softwareLoading = false, softwareLoadingKey = "", softwareLoadedKey = "", softwareLoadSequence = 0, softwareRootDrafts = {};
let storageClipboard = null, editingTextPath = null, storageShowHidden = false, storagePathCapabilities = null;
let overview = null;
let systemInfo = null, terminalInfo = null, terminalTabs = [], activeTerminalID = "";
let dockerRuntime = null, dockerProjects = [], dockerVolumes = [], dockerNetworks = [], dockerBusy = new Set(), dockerPendingContainerStates = new Map(), dockerProjectErrors = new Map(), dockerEditing = null, dockerAttachTerminal = null;
let remoteProviders = [], remoteTargets = [], remoteSessions = [], remoteSessionID = "", remoteStartingTargets = new Set(), remoteRDPInteraction = null, remotePendingTarget = null;
let logTimer = null, toastTimer = null;
let logSource = null;
let refreshTimer = null;
let refreshing = false;
let connectionTimer = null, connectionOnline = false, connectionChecked = false, connectionWasLost = false, reloadingAfterReconnect = false;
const themeStorageKey = "runpilot.theme";
const sidebarStorageKey = "runpilot.sidebar-collapsed";

const pageMeta = {
  overview: ["Overview", "RunPilot service and resource health at a glance.", null],
  storage: ["Storage", "Browse the local filesystem and automatically discovered Docker volumes.", null],
  tasks: ["Tasks", "Continuous commands, scheduled commands, and backups managed in one place.", "Add task"],
  software: ["Software", "Install and maintain portable applications in a RunPilot-managed Scoop root on Windows.", null],
  terminal: ["Terminal", "Interactive shells run with the same OS authority as the RunPilot service.", null],
  docker: ["Docker", "Docker Compose projects, volumes, and networks managed with the RunPilot service identity.", "Create project"],
  remote: ["Remote Access", "Launch configured graphical applications and desktops through the same RunPilot origin.", null],
};

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Authorization", `Bearer ${token}`);
  if (options.body && !(options.body instanceof FormData) && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const res = await fetch(path, {...options, headers});
  if (res.status === 401) {
    const error = new Error("Unauthorized"); error.serverReachable = true;
    throw error;
  }
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try { message = (await res.json()).error || message; } catch {}
    const error = new Error(message); error.serverReachable = true;
    throw error;
  }
  if (res.status === 204) return null;
  const ct = res.headers.get("content-type") || "";
  return ct.includes("application/json") ? res.json() : res.text();
}

function setConnected(ok) {
  if (!ok && (connectionOnline || connectionChecked)) connectionWasLost = true;
  connectionChecked = true;
  const restored = ok && connectionWasLost;
  connectionOnline = ok;
  $("connectionDot").classList.toggle("ok", ok);
  $("connectionText").textContent = ok ? "Connected" : "Offline";
  if (restored && !reloadingAfterReconnect) {
    reloadingAfterReconnect = true;
    window.location.reload();
  }
}

function applyTheme(theme) {
  const isDark = theme === "dark";
  document.documentElement.dataset.theme = isDark ? "dark" : "light";
  localStorage.setItem(themeStorageKey, isDark ? "dark" : "light");
  const toggle = $("themeToggle");
  toggle.setAttribute("aria-pressed", String(isDark));
  toggle.title = isDark ? "Switch to light theme" : "Switch to dark theme";
  toggle.innerHTML = isDark ? "☀ <span>Light theme</span>" : "☾ <span>Dark theme</span>";
}

function initializeTheme() {
  const saved = localStorage.getItem(themeStorageKey);
  applyTheme(saved || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"));
  $("themeToggle").addEventListener("click", () => {
    applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark");
  });
}

function setSidebarCollapsed(collapsed) {
  const shell=document.querySelector(".shell"), changing=shell.classList.contains("sidebar-collapsed")!==collapsed;
  if (changing) remoteRDPInteraction?.layoutTransitionStarted?.();
  shell.classList.toggle("sidebar-collapsed", collapsed);
  $("sidebarToggle").setAttribute("aria-expanded", String(!collapsed));
  $("sidebarToggle").title = collapsed ? "Expand sidebar" : "Collapse sidebar";
  localStorage.setItem(sidebarStorageKey, String(collapsed));
}

function initializeSidebar() {
  setSidebarCollapsed(localStorage.getItem(sidebarStorageKey) === "true");
  $("sidebarToggle").addEventListener("click", () => setSidebarCollapsed(!document.querySelector(".shell").classList.contains("sidebar-collapsed")));
}

function toast(message, level = "info") {
  const el = $("toast");
  clearTimeout(toastTimer);
  el.replaceChildren();
  const content=document.createElement("div"), title=document.createElement("strong"), detail=document.createElement("span"), close=document.createElement("button");
  title.textContent=level === "error" ? "Error" : "Notice"; detail.textContent=message;
  content.append(title,detail);
  close.type="button"; close.className="toast-close"; close.textContent="×"; close.setAttribute("aria-label","Dismiss notification"); close.addEventListener("click",()=>el.classList.add("hidden"));
  el.append(content,close); el.classList.toggle("toast-error",level === "error"); el.setAttribute("role",level === "error" ? "alert" : "status"); el.classList.remove("hidden");
  toastTimer=setTimeout(()=>el.classList.add("hidden"),level === "error" ? 10000 : 4000);
}
function toastError(message) { toast(message,"error"); }

function splitArgs(text) {
  const out = [];
  let cur = "", quote = null, escape = false;
  for (const ch of text.trim()) {
    if (escape) { cur += ch; escape = false; continue; }
    if (ch === "\\") { escape = true; continue; }
    if (quote) {
      if (ch === quote) quote = null; else cur += ch;
    } else if (ch === '"' || ch === "'") quote = ch;
    else if (/\s/.test(ch)) { if (cur) { out.push(cur); cur = ""; } }
    else cur += ch;
  }
  if (cur) out.push(cur);
  return out;
}

function formatArgs(args = []) {
  return args.map(a => /\s/.test(a) ? `"${a.replaceAll('"', '\\"')}"` : a).join(" ");
}

function setCommandError(prefix, message = "") {
  const error = $(`${prefix}CommandError`);
  error.textContent = message;
  error.classList.toggle("hidden", !message);
}

function updateEnvironmentEmpty(prefix) {
  const hasRows = $(`${prefix}EnvRows`).children.length > 0;
  $(`${prefix}EnvEmpty`).classList.toggle("hidden", hasRows);
}

function addEnvironmentRow(prefix, name = "", value = "", focusName = false) {
  const row = document.createElement("div");
  row.className = "environment-row";
  row.innerHTML = `<input class="environment-name" aria-label="Variable name" placeholder="NAME">
    <input class="environment-value" aria-label="Variable value" placeholder="Value">
    <button class="button secondary small environment-remove" type="button" title="Remove variable" aria-label="Remove variable">Remove</button>`;
  row.querySelector(".environment-name").value = name;
  row.querySelector(".environment-value").value = value;
  row.querySelector(".environment-remove").addEventListener("click", () => {
    row.remove();
    updateEnvironmentEmpty(prefix);
  });
  $(`${prefix}EnvRows`).append(row);
  updateEnvironmentEmpty(prefix);
  if (focusName) row.querySelector(".environment-name").focus();
}

function populateCommandEditor(prefix, command = {}) {
  $(`${prefix}Path`).value = command.path || "";
  $(`${prefix}Args`).value = formatArgs(command.args || []);
  $(`${prefix}Cwd`).value = command.workingDirectory || "";
  $(`${prefix}Interpreter`).value = command.interpreter || "auto";
  $(`${prefix}EnvRows`).replaceChildren();
  Object.keys(command.environment || {}).sort((a, b) => a.localeCompare(b)).forEach(name => {
    addEnvironmentRow(prefix, name, command.environment[name]);
  });
  updateEnvironmentEmpty(prefix);
  setCommandError(prefix);
	if (systemInfo?.capabilities) configurePlatformAwareFields(systemInfo.capabilities);
}

function commandFromEditor(prefix) {
  const environment = {};
  const names = new Map();
  for (const row of $(`${prefix}EnvRows`).querySelectorAll(".environment-row")) {
    const name = row.querySelector(".environment-name").value.trim();
    const value = row.querySelector(".environment-value").value;
    if (!name && !value) continue;
    if (!name) {
      setCommandError(prefix, "Environment variable names are required.");
      return null;
    }
    if (name.includes("=")) {
      setCommandError(prefix, `Environment variable "${name}" must not contain =.`);
      return null;
    }
    const canonicalName = name.toUpperCase();
    if (names.has(canonicalName)) {
      setCommandError(prefix, `Environment variables "${names.get(canonicalName)}" and "${name}" conflict on case-insensitive platforms.`);
      return null;
    }
    names.set(canonicalName, name);
    environment[name] = value;
  }
  setCommandError(prefix);
  return {
    path: $(`${prefix}Path`).value.trim(),
    args: splitArgs($(`${prefix}Args`).value),
    workingDirectory: $(`${prefix}Cwd`).value.trim(),
    interpreter: $(`${prefix}Interpreter`).value,
    environment,
  };
}

document.querySelectorAll(".environment-editor").forEach(editor => {
  const prefix = editor.dataset.commandPrefix;
  editor.querySelector(".env-add").addEventListener("click", () => addEnvironmentRow(prefix, "", "", true));
});

function fmtDate(value) {
  if (!value) return "—";
  return new Date(value).toLocaleString();
}

function fmtBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes, unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

function fmtPercent(value) {
  return Number.isFinite(value) ? `${value.toFixed(1)}%` : "—";
}

function percent(value, total = 100) {
  if (!Number.isFinite(value) || !Number.isFinite(total) || total <= 0) return 0;
  return Math.max(0, Math.min(100, (value / total) * 100));
}

function meter(value, tone = "blue") {
  return `<div class="meter ${tone}" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${value.toFixed(1)}"><span style="width:${value}%"></span></div>`;
}

function utilizationMetric(label, current, average, tone, available = true) {
  if (!available) return `<div class="metric utilization-metric"><span>${label}</span><strong>Not available</strong>${meter(0, tone)}</div>`;
  return `<div class="metric utilization-metric">
    <span>${label}</span>
    <div class="utilization-reading"><span>Current</span><strong>${fmtPercent(current)}</strong></div>
    ${meter(percent(current), tone)}
    <div class="utilization-reading"><span>1 min AVG</span><strong>${fmtPercent(average)}</strong></div>
    ${meter(percent(average), tone)}
  </div>`;
}

function scheduleText(s) {
  if (!s) return "—";
  if (s.type === "interval") {
    const min = Math.round((s.intervalSeconds || 0) / 60);
    return `Every ${min} min`;
  }
  if (s.type === "daily") return `Daily ${s.timeOfDay}`;
  return `Cron ${s.cron}`;
}

function statusBadge(status) {
  const cls = status || "idle";
  return `<span class="status ${escapeHtml(cls)}">${escapeHtml(cls)}</span>`;
}

function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, ch => ({
    "&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"
  }[ch]));
}

async function refresh() {
  if (refreshing || !token) return;
  refreshing = true;
  try {
    const [p, j, o, st, sw, docker, remote] = await Promise.all([
      api("api/v1/processes"),
      api("api/v1/jobs"),
      api("api/v1/overview"), api("api/v1/storage"),
      currentPage === "software" ? api("api/v1/software/providers") : Promise.resolve(null),
      currentPage === "docker" ? Promise.all([api("api/v1/docker/projects"), api("api/v1/docker/volumes"), api("api/v1/docker/networks")]) : Promise.resolve(null),
      currentPage === "remote" ? Promise.all([api("api/v1/remote/providers"), api("api/v1/remote/targets"), api("api/v1/remote/sessions")]) : Promise.resolve(null)
    ]);
    processes = p;
    jobs = j;
    overview = o;
    storage = st;
    if (sw) {
      softwareProviders = sw;
      if (!sw.some(p => p.id === softwareProviderID)) softwareProviderID = sw[0]?.id || "";
      const selected = sw.find(p => p.id === softwareProviderID);
      if (selected?.state === "ready") {
        await loadSoftwareView();
      } else {
        softwarePackages = []; softwareUpdates = []; softwareSearchResults = []; softwareBuckets = []; softwareLoading = false; softwareLoadingKey = ""; softwareLoadedKey = "";
      }
    }
    renderOverview();
    renderTasks();
    renderStorage();
    if (currentPage === "software") renderSoftware();
    if (docker) applyDockerSnapshot(docker);
    if (remote) { [remoteProviders, remoteTargets, remoteSessions] = remote; renderRemote(); }
    setConnected(true);
  } catch (e) {
    softwareLoading = false; softwareLoadingKey = "";
    setConnected(!!e.serverReachable);
    if (e.message === "Unauthorized") $("loginDialog").showModal();
    else toast(e.message);
    if (currentPage === "software") renderSoftware();
  } finally {
    refreshing = false;
  }
}

function remoteStatus(state) { return state === "available" ? "running" : state === "not-installed" || state === "unsupported" ? "stopped" : "failure"; }
function guacdValue() { return {host:$("guacdHost").value.trim(),port:Number($("guacdPort").value),tls:$("guacdTLS").checked,connectTimeoutSeconds:Number($("guacdTimeout").value)}; }
function setGuacdError(message="") { $("guacdSettingsError").textContent=message; $("guacdSettingsError").classList.toggle("hidden",!message); }
function setGuacdStatus(message="", error=false) { const node=$("guacdSettingsStatus"); node.textContent=message; node.classList.toggle("hidden",!message); node.classList.toggle("form-error",error); }
function validateGuacdForm(value) { if(!value.host) return "guacd host is required"; if(!Number.isInteger(value.port)||value.port<1||value.port>65535) return "guacd port must be between 1 and 65535"; if(!Number.isInteger(value.connectTimeoutSeconds)||value.connectTimeoutSeconds<1||value.connectTimeoutSeconds>60) return "guacd connect timeout must be between 1 and 60 seconds"; return ""; }
async function openGuacdSettings() { setGuacdError(); setGuacdStatus(); try { const value=await api("api/v1/remote/guacd"); $("guacdHost").value=value.host||"127.0.0.1"; $("guacdPort").value=value.port||4822; $("guacdTLS").checked=!!value.tls; $("guacdTimeout").value=value.connectTimeoutSeconds||5; $("guacdSettingsDialog").showModal(); } catch(e) { toastError(e.message); } }
function remoteProviderHint(provider) { return provider.state === "available" ? "" : provider.installHint || provider.message || "This provider is not currently available."; }
function remoteProviderCard(provider) { const hint=remoteProviderHint(provider), metadata=[provider.platform,provider.version].filter(Boolean).join(" · "), rdp=provider.id==="rdp", available=provider.state === "available", summary=rdp&&provider.message ? `<div class="meta">${escapeHtml(provider.message)}</div>` : (metadata ? `<div class="meta">${escapeHtml(metadata)}</div>` : ""), action=rdp ? `<button class="button secondary small remote-provider-add" onclick="openGuacdSettings()">Settings</button>` : `<button class="button secondary small remote-provider-add" ${available ? "" : "disabled"} onclick="openRemoteTarget(null,'${escapeHtml(provider.id)}')">Add ${escapeHtml(provider.name)} target</button>`; return `<article class="remote-provider-card"><div class="remote-provider-heading"><div><h3>${escapeHtml(provider.name)}</h3>${summary}</div><div class="remote-provider-status"><span class="status ${remoteStatus(provider.state)}">${escapeHtml(provider.state)}</span>${hint&&!rdp ? `<span class="provider-info" tabindex="0" role="img" aria-label="Provider installation hint" data-tooltip="${escapeHtml(hint)}">ⓘ</span>` : ""}</div></div>${action}</article>`; }
function renderRemote() {
  if (!$("remoteProviders")) return;
  $("remoteProviders").innerHTML = remoteProviders.map(remoteProviderCard).join("") || `<div class="empty compact"><h2>No providers registered</h2></div>`;
  $("remoteTargets").innerHTML = remoteTargets.map(target => { const starting=remoteStartingTargets.has(target.id), detail=target.provider === "rdp" ? `${target.rdp?.host || ""}${target.rdp?.port ? `:${target.rdp.port}` : ""}` : `${target.type} · ${target.command?.path || ""}`; return `<article class="row"><div class="row-head"><div><h3>${escapeHtml(target.name)}</h3><div class="meta">${escapeHtml(detail)}</div></div></div><div class="row-actions"><button class="button primary small remote-open-button" ${!starting ? "" : "disabled"} aria-busy="${starting}" onclick="startRemoteSession('${target.id}')">${starting ? '<span class="spinner remote-button-spinner" aria-hidden="true"></span>Starting…' : "Open"}</button><button class="button secondary small" onclick="editRemoteTarget('${target.id}')">Edit</button><button class="button danger small" onclick="deleteRemoteTarget('${target.id}')">Delete</button></div></article>`; }).join("");
  $("remoteTargetsEmpty").classList.toggle("hidden", remoteTargets.length > 0);
  const active = remoteSessions.filter(session => ["starting", "running", "stopping"].includes(session.state));
  $("remoteSessions").innerHTML = active.map(session => `<article class="row"><div class="row-head"><div><h3>${escapeHtml(session.targetName)}</h3><div class="meta">${escapeHtml(session.type)} · ${fmtDate(session.startedAt || session.createdAt)}</div></div>${statusBadge(session.state)}</div>${session.message ? `<div class="notice remote-session-notice"><strong>${escapeHtml(session.message)}</strong>${session.windowCount !== undefined ? `<span>${session.windowCount} visible application window${session.windowCount === 1 ? "" : "s"}.</span>` : ""}</div>` : ""}${session.failure ? `<div class="form-error">${escapeHtml(session.failure)}</div>` : ""}<div class="row-actions"><button class="button primary small" ${session.state === "running" ? "" : "disabled"} onclick="openRemoteSession('${session.id}')">Open</button><button class="button secondary small" onclick="openRemoteDiagnostics('${session.id}')">Diagnostics</button><button class="button danger small" onclick="stopRemoteSession('${session.id}')">Stop</button></div></article>`).join("");
  $("remoteSessionsEmpty").classList.toggle("hidden", active.length > 0);
  if (remoteSessionID && !remoteSessions.some(session => session.id === remoteSessionID)) {
    closeRemoteSession();
    toast("The remote session is no longer available.");
    return;
  }
  if (remoteSessionID) renderRemoteSession();
}
function remoteXpraDefaults() { return remoteProviders.find(provider => provider.id === "xpra")?.xpraDefaults || {}; }
function setRemoteXpraSettings(options = remoteXpraDefaults()) { $("remoteXpraProfile").value=options.profile || "recommended"; $("remoteXpraEncoding").value=options.encoding || "webp"; $("remoteXpraVideo").checked=!!options.video; $("remoteXpraDPIMode").value=options.dpiMode || "auto"; $("remoteXpraDPI").value=options.dpi || 96; $("remoteXpraLaunchAfterConnect").checked=options.launchAfterConnect !== false; $("remoteXpraClipboard").checked=options.clipboard !== false; $("remoteXpraDynamicResize").checked=options.dynamicResize !== false; $("remoteXpraMenu").value=options.menu || "autohide"; $("remoteXpraToolbarPosition").value=options.toolbarPosition || "top-left"; updateRemoteXpraSettings(); }
function updateRemoteXpraSettings() { const xpra=$("remoteTargetProvider").value === "xpra", customDPI=$("remoteXpraDPIMode").value === "custom"; $("remoteXpraSettings").classList.toggle("hidden",!xpra); $("remoteRDPSettings").classList.toggle("hidden",xpra); document.querySelectorAll(".remote-xpra-only").forEach(node=>node.classList.toggle("hidden",!xpra)); $("remoteXpraDPIWrap").classList.toggle("hidden",!xpra || !customDPI); $("remoteXpraDPI").required=xpra && customDPI; $("remotePath").required=xpra; if (!xpra) $("remoteTargetType").value="desktop"; }
function applyRemoteXpraProfile() { const profile=$("remoteXpraProfile").value; const values={recommended:["webp",false],automatic:["auto",true],compatibility:["rgb",false]}; if (values[profile]) { $("remoteXpraEncoding").value=values[profile][0]; $("remoteXpraVideo").checked=values[profile][1]; } }
function splitRDPUsername(value) { const text=(value||"").trim(), separator=text.indexOf("\\"); return separator > 0 ? {domain:text.slice(0,separator),username:text.slice(separator+1)} : {domain:"",username:text}; }
function formatRDPUsername(username, domain) { return domain ? `${domain}\\${username}` : username||""; }
function openRemoteTarget(existing = null, providerID = "xpra") { const provider=existing?.provider||providerID; $("remoteTargetForm").reset(); $("remoteTargetId").value=existing?.id||""; $("remoteTargetName").value=existing?.name||""; $("remoteTargetType").value=existing?.type||"application"; $("remoteTargetProvider").value=provider; $("remoteDBusMode").value=existing?.dbusMode || (existing?.forwardDbus ? "host-session" : "isolated"); setRemoteXpraSettings(existing?.xpra || remoteXpraDefaults()); const rdp=existing?.rdp||{}; $("remoteRDPHost").value=rdp.host||""; $("remoteRDPPort").value=rdp.port||3389; $("remoteRDPUsername").value=formatRDPUsername(rdp.username,rdp.domain); $("remoteRDPSecurity").value=rdp.securityMode||"automatic"; $("remoteRDPLayout").value=rdp.serverLayout||""; $("remoteRDPResize").value=rdp.resizeMethod||"display-update"; $("remoteRDPCertificate").value=rdp.certificatePolicy||"validate"; $("remoteRDPTimeout").value=rdp.timeoutSeconds||10; $("remoteRDPClipboard").value=rdp.clipboardNormalization||"preserve"; $("remoteRDPPerformance").value=rdp.performanceProfile||"balanced"; populateCommandEditor("remote",existing?.command||{interpreter:"direct"}); updateRemoteXpraSettings(); const rdpTarget=provider === "rdp"; $("remoteTargetDialogTitle").textContent=existing ? `Edit ${rdpTarget ? "RDP" : "Xpra"} target` : `Add ${rdpTarget ? "RDP" : "Xpra"} target`; $("remoteTargetDialogDescription").textContent=rdpTarget ? "Save a desktop endpoint; credentials are requested each time you connect." : "Launch an application or desktop in an isolated Xpra session."; $("remoteTargetDialog").showModal(); }
function editRemoteTarget(id) { openRemoteTarget(remoteTargets.find(target => target.id === id)); }
async function deleteRemoteTarget(id) { if (!confirm("Delete this remote target?")) return; try { await api(`api/v1/remote/targets/${id}`,{method:"DELETE"}); await refresh(); } catch (e) { toast(e.message); } }
function promptRDPConnection(target) { remotePendingTarget=target; $("remoteRDPConnectTitle").textContent=`Connect to ${target.name||"RDP target"}`; $("remoteRDPConnectUsername").value=formatRDPUsername(target.rdp?.username,target.rdp?.domain); $("remoteRDPConnectPassword").value=""; $("remoteRDPConnectDialog").showModal(); }
async function startRemoteSession(id) { const target=remoteTargets.find(item=>item.id===id); if (target?.provider === "rdp") { promptRDPConnection(target); return; } await beginRemoteSession(id); }
async function beginRemoteSession(id, credentials=null) { if (remoteStartingTargets.has(id)) return; remoteStartingTargets.add(id); renderRemote(); try { const session=await api(`api/v1/remote/targets/${id}/sessions`,{method:"POST",body:credentials ? JSON.stringify(credentials) : undefined}); if (credentials) credentials.password=""; await refresh(); await openRemoteSession(session.id,credentials); } catch (e) { toastError(e.message); await refresh(); } finally { remoteStartingTargets.delete(id); if (currentPage === "remote") renderRemote(); } }
async function stopRemoteSession(id) {
  // Close the local client first so its session-bound tunnel is released before
  // returning to the Remote overview.
  const closingCurrentSession = remoteSessionID === id;
  if (closingCurrentSession) closeRemoteSession();
  try {
    await api(`api/v1/remote/sessions/${id}`, {method: "DELETE"});
    await refresh();
  } catch (e) {
    toastError(e.message);
  }
}
async function openRemoteDiagnostics(id) { const session=remoteSessions.find(item=>item.id===id); openLog(`${session?.targetName || "Remote session"} diagnostics`, async () => { const details=await api(`api/v1/remote/sessions/${id}/diagnostics`); return details.log || details.session.message || "No Xpra diagnostics were captured."; }); }
function focusRemoteClient() {
  if (!remoteSessionID) return;
  const client = remoteRDPInteraction ? $("remoteRDP") : $("remoteFrame");
  client.focus({preventScroll: true});
}
async function openRemoteSession(id, credentials=null) { const session=remoteSessions.find(item=>item.id===id); if (session?.provider === "rdp" && !credentials) { promptRDPConnection({name:session.targetName,rdp:session.rdp,sessionID:id}); return; } remoteSessionID=id; setPage("remote",false); $("remoteSessionError").classList.add("hidden"); $("remoteLoading").classList.remove("hidden"); renderRemoteSession(); try { if (session?.provider === "rdp") await openRDPRemoteSession(session,credentials); else { const ticket=await api(`api/v1/remote/sessions/${id}/client-ticket`,{method:"POST"}); const frame=$("remoteFrame"); $("remoteRDP").classList.add("hidden"); frame.classList.remove("hidden"); frame.src=`api/v1/remote/sessions/${encodeURIComponent(id)}/client/?ticket=${encodeURIComponent(ticket.ticket)}`; } history.replaceState(null,"",`?remoteSession=${encodeURIComponent(id)}`); } catch(e) { closeRemoteSession(); toastError(`Remote session unavailable: ${e.message}`); } }
function renderRemoteSession() { const session=remoteSessions.find(item=>item.id===remoteSessionID); document.querySelector("main").classList.add("remote-active"); $("remoteOverview").classList.add("hidden"); $("remoteSessionView").classList.remove("hidden"); $("remoteSessionName").textContent=session?.targetName||"Remote session"; $("remoteSessionMeta").textContent=session ? `${session.state} · ${fmtDate(session.startedAt || session.createdAt)}` : "Loading session…"; $("remoteStop").disabled=!session || !["starting","running","stopping"].includes(session.state); $("remoteSessionNotice").textContent=session?.message || ""; $("remoteSessionNotice").classList.toggle("hidden", !session?.message); }
function closeRemoteSession() {
  const interaction = remoteRDPInteraction;
  remoteSessionID = "";
  remoteRDPInteraction = null;
  // Restore navigation before asking the embedded client to tear down. Its
  // session may already be gone when the remote side or Stop button ended it.
  document.querySelector("main").classList.remove("remote-active");
  $("remoteFrame").src = "about:blank";
  $("remoteFrame").classList.remove("hidden");
  $("remoteRDP").classList.remove("rdp-active");
  $("remoteRDP").classList.add("hidden");
  $("remoteLoading").classList.add("hidden");
  $("remoteSessionNotice").classList.add("hidden");
  $("remoteOverview").classList.remove("hidden");
  $("remoteSessionView").classList.add("hidden");
  history.replaceState(null, "", location.pathname);
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  try {
    interaction?.shutdown();
  } catch (error) {
    console.warn("RDP client shutdown completed after a transport error.", error);
  }
}

// WebSocketTunnel appends the argument passed to client.connect() after "?".
// Keep its endpoint query-free and supply the one-time ticket there; otherwise
// Guacamole creates a malformed URL ending in "?undefined".
function remoteTransportURL(id) { const url=new URL(`api/v1/remote/sessions/${encodeURIComponent(id)}/transport`,document.baseURI); url.protocol=url.protocol === "https:" ? "wss:" : "ws:"; return url.toString(); }
function remoteTransportParams(ticket, options) { return RunPilotRDPSize.transportParams(ticket,options); }
function nextAnimationFrame() { return new Promise(resolve=>requestAnimationFrame(resolve)); }
function currentRDPSurfaceSize(container) { const width=Math.round(container.clientWidth), height=Math.round(container.clientHeight); return width>0&&height>0 ? {width,height} : null; }
async function initialRDPSurfaceSize(container) { const measured=await RunPilotRDPSize.measureAfterLayout(nextAnimationFrame,()=>currentRDPSurfaceSize(container)); const resolved=RunPilotRDPSize.resolveInitialSize(measured); if(resolved.fallback) console.warn("initial RDP surface unavailable; using fallback 640x480"); else console.debug(`initial RDP surface size: ${resolved.width}x${resolved.height}`); return resolved; }
function guacamoleTunnelError(status) { const code=Number(status?.code), message=String(status?.message||""); if(message.includes("Could not establish tunnel to guacd")) { const detail=message.replace(/^512\s+/,"").trim(); return `${detail}. Check Remote session diagnostics.${Number.isFinite(code) ? ` (${code})` : ""}`; } const descriptions={512:"Guacamole server error",513:"Guacamole server is busy",514:"The upstream RDP connection timed out",515:"The upstream RDP connection failed",516:"The requested RDP resource was not found",517:"The RDP resource is in conflict",518:"The RDP resource was closed",519:"Guacamole could not find the upstream RDP service",520:"The upstream RDP service is unavailable",521:"The Guacamole session is in conflict",522:"The Guacamole session timed out",523:"The Guacamole session was closed",768:"Invalid Guacamole tunnel request",769:"Guacamole tunnel authorization failed",771:"Guacamole tunnel access was denied",776:"The Guacamole client timed out",781:"The Guacamole client was too slow",783:"Unsupported Guacamole client message",797:"Too many Guacamole clients"}; if(Number.isFinite(code) && descriptions[code]) return `${descriptions[code]}. Check Remote session diagnostics. (${code})`; return `RDP session ended: ${message||"connection closed"}${Number.isFinite(code) ? ` (${code})` : ""}`; }
function guacamoleClientError(status) { const code=Number(status?.code), message=String(status?.message||"").trim(); return `Guacamole client/RDP error: ${message||"remote desktop connection failed"}${Number.isFinite(code) ? ` (${code})` : ""}. Check Remote session diagnostics.`; }
function guacamoleStateName(states,state) { return Object.keys(states||{}).find(name=>states[name]===state)||`unknown (${state})`; }
function instrumentGuacamoleResizeMessages(tunnel) { const previous=tunnel.sendMessage; tunnel.sendMessage=function(opcode,...args) { if(opcode==="size") console.debug(`Guacamole client emitted size ${args[0]}x${args[1]}`); return previous.call(tunnel,opcode,...args); }; return ()=>{tunnel.sendMessage=previous}; }
async function openRDPRemoteSession(session, credentials) {
  const ticket=await api(`api/v1/remote/sessions/${session.id}/transport-ticket`,{method:"POST"}), surface=$("remoteRDP"), frameShell=$("remoteFrameShell");
  $("remoteFrame").src="about:blank"; $("remoteFrame").classList.add("hidden"); surface.classList.remove("hidden"); surface.classList.add("rdp-active"); surface.replaceChildren();
  const initialSize=await initialRDPSurfaceSize(frameShell), transportParams=remoteTransportParams(ticket.ticket,{width:initialSize.width,height:initialSize.height,dpi:window.devicePixelRatio > 1 ? 120 : 96,timezone:Intl.DateTimeFormat().resolvedOptions().timeZone||""});
  const tunnel=new Guacamole.WebSocketTunnel(remoteTransportURL(session.id)), stopResizeInstrumentation=instrumentGuacamoleResizeMessages(tunnel); const client=new Guacamole.Client(tunnel), display=client.getDisplay(); surface.append(display.getElement());
  const sizing=RunPilotRDPSize.createController({resizeMethod:session.rdp?.resizeMethod||"display-update",initialSize,getSurfaceSize:()=>currentRDPSurfaceSize(frameShell),sendRemoteSize:size=>{ console.debug(`remote RDP resize requested ${size.width}x${size.height}`); client.sendSize(size.width,size.height); },fitLocal:(available,remote)=>{ const scale=RunPilotRDPSize.fitScale(available,remote); if(scale!==null) display.scale(scale); }}), previousDisplayResize=display.onresize;
  display.onresize=(width,height)=>{ previousDisplayResize?.(width,height); sizing.displayResized({width,height}); };
  const mouse=new Guacamole.Mouse(display.getElement()); mouse.onEach(["mousedown","mousemove","mouseup"],event=>client.sendMouseState(event.state));
  const keyboard=new Guacamole.Keyboard(surface); keyboard.onkeydown=keysym=>client.sendKeyEvent(1,keysym); keyboard.onkeyup=keysym=>client.sendKeyEvent(0,keysym);
  const shell=document.querySelector(".shell"), surfaceChanged=()=>sizing.surfaceChanged(), layoutTransitionFinished=event=>{ if(event.target===shell&&event.propertyName==="grid-template-columns") sizing.layoutTransitionFinished(); };
  remoteRDPInteraction={shutdown:()=>{ sizing.dispose(); stopResizeInstrumentation(); keyboard.reset(); keyboard.onkeydown=null; keyboard.onkeyup=null; mouse.onEach(["mousedown","mousemove","mouseup"],null); client.disconnect(); }, focus:()=>surface.focus({preventScroll:true}), surfaceChanged, layoutTransitionStarted:()=>sizing.layoutTransitionStarted()};
  surface.addEventListener("pointerdown",()=>surface.focus({preventScroll:true})); const observer=new ResizeObserver(surfaceChanged); observer.observe(frameShell); window.addEventListener("resize",surfaceChanged); shell.addEventListener("transitionend",layoutTransitionFinished); shell.addEventListener("transitioncancel",layoutTransitionFinished); const prior=remoteRDPInteraction.shutdown; remoteRDPInteraction.shutdown=()=>{observer.disconnect();window.removeEventListener("resize",surfaceChanged);shell.removeEventListener("transitionend",layoutTransitionFinished);shell.removeEventListener("transitioncancel",layoutTransitionFinished);prior()};
  tunnel.onstatechange=state=>{ console.debug("Guacamole tunnel state",guacamoleStateName(Guacamole.Tunnel.State,state)); if(state===Guacamole.Tunnel.State.OPEN){ $("remoteLoading").classList.add("hidden"); surface.focus({preventScroll:true}); toast(`Connected to ${session.targetName||"RDP session"}.`); } }; tunnel.onerror=status=>{ console.warn("Guacamole WebSocket tunnel error",{code:status?.code,message:status?.message}); if(remoteSessionID===session.id) toastError(guacamoleTunnelError(status)); }; client.onstatechange=state=>{ console.debug("Guacamole client state",guacamoleStateName(Guacamole.Client.State,state)); sizing.setConnected(state===Guacamole.Client.State.CONNECTED); }; client.onerror=status=>{ console.warn("Guacamole client/RDP error",{code:status?.code,message:status?.message}); if(remoteSessionID===session.id) toastError(guacamoleClientError(status)); };
  client.connect(transportParams);
}

function renderOverview() {
  if (!overview) return;
  const host = overview.host || {};
  const memoryUsed = Math.max(0, (host.memoryTotalBytes || 0) - (host.memoryFreeBytes || 0));
  $("overviewMetrics").innerHTML = [
    utilizationMetric("CPU", host.cpuPercent, host.cpuAveragePercent, "blue"),
    `<div class="metric"><span>Memory</span><strong>${escapeHtml(`${fmtBytes(memoryUsed)} / ${fmtBytes(host.memoryTotalBytes)}`)}</strong>${meter(percent(memoryUsed, host.memoryTotalBytes), "violet")}</div>`,
    utilizationMetric("GPU", host.gpuPercent, host.gpuAveragePercent, "pink", host.gpuAvailable),
    ["Tasks", `${overview.runningTasks || 0} running / ${overview.taskCount || 0}`, percent(overview.runningTasks || 0, overview.taskCount || 0), "green"],
  ].map(metric => Array.isArray(metric) ? `<div class="metric"><span>${metric[0]}</span><strong>${escapeHtml(metric[1])}</strong>${meter(metric[2], metric[3])}</div>` : metric).join("");

  const disks = host.disks || [];
  $("diskDetails").innerHTML = disks.length ? disks.map(disk => {
    const used = Math.max(0, (disk.totalBytes || 0) - (disk.freeBytes || 0));
    const usedPercent = percent(used, disk.totalBytes);
    return `<article class="disk-card">
      <h3>${escapeHtml(disk.path)}</h3>
      <div class="disk-summary"><span>${fmtBytes(disk.freeBytes)} free from ${fmtBytes(disk.totalBytes)}</span><strong>${fmtPercent(usedPercent)} used</strong></div>
      ${meter(usedPercent, usedPercent >= 90 ? "red" : "blue")}
    </article>`;
  }).join("") : `<div class="empty compact"><h2>No disk metrics</h2><p>Disk information is not available.</p></div>`;
  $("diskDetails").classList.toggle("disk-grid", disks.length > 0);

  const issues = overview.issues || [];
  $("issueCount").textContent = issues.length ? `${issues.length} issue${issues.length === 1 ? "" : "s"}` : "No issues";
  $("issueDetails").innerHTML = issues.length ? issues.map(issue => `<article class="row issue-row">
    <div class="row-head"><div><h3>${escapeHtml(issue.name)}</h3><div class="meta">${escapeHtml(issue.source)}</div></div>${statusBadge("failure")}</div>
    <div class="row-details"><div class="kv"><span>Details</span><span>${escapeHtml(issue.message)}</span></div></div>
  </article>`).join("") : `<div class="empty compact"><h2>All clear</h2><p>No current task or host errors were reported.</p></div>`;
}

function startAutoRefresh() {
  if (refreshTimer) return;
  refreshTimer = setInterval(() => {
    if (!document.hidden) refresh();
  }, 5000);
}

async function probeConnection() {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 3500);
  try {
    await fetch("api/v1/system", {headers:{Authorization:`Bearer ${token}`}, signal:controller.signal});
    setConnected(true);
  } catch {
    setConnected(false);
  } finally {
    clearTimeout(timeout);
  }
}

function startConnectionMonitor() {
  if (connectionTimer) return;
  connectionTimer = setInterval(() => { if (!document.hidden) probeConnection(); }, 5000);
  probeConnection();
}

function renderContinuousTask(v) {
  const d = v.definition, s = v.status;
  const running = ["running","starting","stopping"].includes(s.state);
  return `<article class="row">
      <div class="row-head">
        <div><h3>${escapeHtml(d.name)}</h3><div class="meta">Continuous · ${escapeHtml(d.command.path)}</div></div>
        ${statusBadge(s.state)}
      </div>
      <div class="row-details">
        <div class="kv"><span>Started</span><span>${fmtDate(s.startedAt)}</span></div>
        <div class="kv"><span>Restart</span><span>${escapeHtml(d.restart?.mode || "on-failure")}</span></div>
        <div class="kv"><span>Autostart</span><span>${d.autostart ? "Yes" : "No"}</span></div>
      </div>
      <div class="row-actions">
        ${running ? `<button class="button secondary small" onclick="processAction('${d.id}','stop')">Stop</button><button class="button secondary small" onclick="processAction('${d.id}','restart')">Restart</button>` : `<button class="button primary small" onclick="processAction('${d.id}','start')">Start</button>`}
        <button class="button secondary small" onclick="openProcessLog('${d.id}','${escapeHtml(d.name)}')">Log</button><button class="button secondary small" onclick="editProcess('${d.id}')">Edit</button><button class="button danger small" onclick="deleteProcess('${d.id}')">Delete</button>
      </div></article>`;
}

function renderTasks() {
  const items = [
    ...processes.map(v => ({kind:"continuous", name:v.definition.name, markup:renderContinuousTask(v)})),
    ...jobs.filter(v => v.definition.type === "command").map(v => ({kind:"scheduled", name:v.definition.name, markup:renderJobRow(v)})),
    ...jobs.filter(v => v.definition.type === "backup").map(v => ({kind:"backup", name:v.definition.name, markup:renderBackupRow(v)})),
  ].filter(item => taskFilter === "all" || item.kind === taskFilter).sort((a,b) => a.name.localeCompare(b.name));
  $("taskGrid").innerHTML = items.map(item => item.markup).join("");
  $("taskEmpty").classList.toggle("hidden", items.length > 0);
}

function renderJobRow(v) {
  const d = v.definition, s = v.status;
  const state = s.running ? "running" : (s.lastSuccess === false ? "failure" : "idle");
  return `<article class="row">
    <div class="row-head"><div><h3>${escapeHtml(d.name)}</h3><div class="meta">Scheduled · ${escapeHtml(scheduleText(d.schedule))}</div></div>${statusBadge(state)}</div>
    <div class="row-details">
      <div class="kv"><span>Schedule</span><span>${escapeHtml(scheduleText(d.schedule))}</span></div>
      <div class="kv"><span>Enabled</span><span>${d.enabled ? "Yes" : "No"}</span></div>
      <div class="kv"><span>Last run</span><span>${fmtDate(s.lastRunAt)}</span></div>
      <div class="kv"><span>Last result</span><span>${s.lastSuccess == null ? "—" : (s.lastSuccess ? "Success" : "Failed")}</span></div>
    </div>
    <div class="row-actions">
      <button class="button primary small" onclick="runJob('${d.id}')">Run now</button>
      ${s.currentRunId ? `<button class="button secondary small" onclick="openRunLog('${s.currentRunId}','${escapeHtml(d.name)}')">Live log</button>` : ""}
      <button class="button secondary small" onclick="editJob('${d.id}')">Edit</button>
      <button class="button danger small" onclick="deleteJob('${d.id}')">Delete</button>
    </div>
  </article>`;
}

function renderBackupRow(v) {
  const d = v.definition, s = v.status, b = d.backup || {};
  const provider = b.engine || "robocopy", cfg = b[provider === "rdiff-backup" ? "rdiffBackup" : provider] || b;
  const source = provider === "restic" ? (cfg.sources || []).join(", ") : cfg.source;
  const target = provider === "restic" ? cfg.repository : cfg.destination;
  const state = s.running ? "running" : (s.lastSuccess === false ? "failure" : "idle");
  return `<article class="row">
    <div class="row-head"><div><h3>${escapeHtml(d.name)}</h3><div class="meta">Backup · ${escapeHtml(provider)} · ${escapeHtml(scheduleText(d.schedule))}</div></div>${statusBadge(state)}</div>
    <div class="row-details">
      <div class="kv"><span>Provider</span><span>${escapeHtml(provider)}</span></div>
      <div class="kv"><span>Schedule</span><span>${escapeHtml(scheduleText(d.schedule))}</span></div>
      <div class="kv"><span>Last run</span><span>${fmtDate(s.lastRunAt)}</span></div>
      <div class="kv"><span>Last result</span><span>${s.lastSuccess == null ? "—" : (s.lastSuccess ? "Success" : "Failed")}</span></div>
    </div>
    <div class="row-actions">
      <button class="button primary small" onclick="runJob('${d.id}')">Run backup</button>
      ${s.currentRunId ? `<button class="button secondary small" onclick="openRunLog('${s.currentRunId}','${escapeHtml(d.name)}')">Live log</button>` : ""}
      <button class="button secondary small" onclick="editBackup('${d.id}')">Edit</button>
      <button class="button danger small" onclick="deleteJob('${d.id}')">Delete</button>
    </div>
  </article>`;
}

async function downloadStorage(id,p){try{const t=await api(`api/v1/storage/${id}/download-ticket`,{method:"POST",body:JSON.stringify({path:p})});window.location.assign(t.url)}catch(e){toast(e.message)}}
async function deleteStorageObject(id,p,type){if(!confirm(`Delete ${type === "directory" ? "folder and all contents" : "file"}? This cannot be undone through RunPilot.`))return;try{await api(`api/v1/storage/${id}/delete`,{method:"POST",body:JSON.stringify({path:p})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}
async function renameStorage(id,p){const n=prompt("New name:");if(!n)return;try{await api(`api/v1/storage/${id}/rename`,{method:"POST",body:JSON.stringify({path:p,newName:n})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}
async function moveStorage(id,p){const d=prompt("Destination folder path (provider-relative):",storagePath);if(d===null)return;try{await api(`api/v1/storage/${id}/move`,{method:"POST",body:JSON.stringify({sourcePath:p,destinationDirectory:d})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}
async function copyStorage(id,p){const d=prompt("Célmappa útvonala:",storagePath);if(d===null)return;try{await api(`api/v1/storage/${id}/copy`,{method:"POST",body:JSON.stringify({sourcePath:p,destinationDirectory:d})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}

function renderStorage() {
  const select = $("storageProvider"), previous = storageLocation || select.value;
  select.replaceChildren();
  storage.forEach(d => { const option = document.createElement("option"); option.value = d.id; option.textContent = d.name; select.append(option); });
  const provider = storage[0];
  $("storageEmpty").classList.toggle("hidden", !!provider);
  $("storageBrowser").classList.toggle("hidden", !provider);
  if (provider) { select.value = storage.some(d => d.id === previous) ? previous : provider.id; if (!storageLocation) browseStorage(select.value, ""); }
}
function isEditableText(name) { return !/\.[^./\\]+$/.test(name) || /\.(txt|log|yaml|yml|json|ini|cfg|conf|toml|xml|csv|md|bat|cmd|ps1|go|js|html|css|ts|tsx|jsx|py|rb|java|c|h|cpp|cs|rs|sh|sql)$/i.test(name); }
function button(label, title, action, disabled = false) { const b=document.createElement("button"); b.className="row-icon"+(title==="Letöltés"?" download-icon":""); b.type="button"; b.textContent=label; b.title=title; b.disabled=disabled; b.addEventListener("click", event=>{event.stopPropagation();action()}); return b; }
function actionSlot(control = null) { if (control) return control; const slot=document.createElement("span");slot.className="row-icon-slot";slot.setAttribute("aria-hidden","true");return slot; }
function entryIcon(entry) { if (entry.type === "directory" || entry.type === "filesystem-root") return "folder"; const ext=(entry.name.split(".").pop()||"").toLowerCase(); if (["txt","log","yaml","yml","json","ini","cfg","conf","toml","xml","csv","md","go","js","ts","py","sh","sql"].includes(ext)||!/\.[^./\\]+$/.test(entry.name)) return "text"; if(["jpg","jpeg","png","gif","webp","svg"].includes(ext)) return "image"; if(["mp3","wav","flac","ogg"].includes(ext)) return "audio"; if(["mp4","mkv","avi","mov","webm"].includes(ext)) return "video"; return "file"; }
function renderBreadcrumbs(id, value) { const root=$("storageBreadcrumbs"); root.replaceChildren(); const home=document.createElement("button");home.className="breadcrumb-link";home.textContent="Ez a gép";home.addEventListener("click",()=>browseStorage(id,""));root.append(home); let built=""; for(const segment of value ? value.split("/") : []) { const sep=document.createElement("span");sep.textContent="/";root.append(sep);built=built?`${built}/${segment}`:segment;const link=document.createElement("button");link.className="breadcrumb-link";link.textContent=segment;const target=built;link.addEventListener("click",()=>browseStorage(id,target));root.append(link); } }
function setStorageEntryLoading(path, loading) { const row=[...document.querySelectorAll(".storage-entry")].find(item=>item.dataset.storagePath===path); if (!row) return; row.setAttribute("aria-busy",String(loading)); row.querySelector(".entry-icon")?.classList.toggle("loading",loading); }
async function browseStorage(id, p = "", loadingEntry = "") {
  if (id === "docker-volumes" && loadingEntry) setStorageEntryLoading(loadingEntry, true);
  try {
    const provider=storage.find(x=>x.id===id); if (!provider || !["ready","read-only"].includes(provider.state?.status)) { toast(provider?.state?.reason || "Storage provider is unavailable"); return; }
    const stateNotice=$("storageStateNotice");
    const listing=await api(`api/v1/storage/${id}/entries?`+new URLSearchParams({path:p,showHidden:storageShowHidden})); storageLocation=id; storagePath=listing.path;
    const pathState=listing.state||provider.state||{}, readOnly=pathState.status === "read-only";
    stateNotice.classList.toggle("hidden",!readOnly); stateNotice.innerHTML=readOnly?`<strong>Storage is read-only</strong><span>${escapeHtml(pathState.reason || "This provider is currently read-only.")}</span>`:"";
    storagePathCapabilities=listing.capabilities||provider.capabilities||{};
    const toolbarCaps=storagePathCapabilities; $("storageUpload").parentElement.classList.toggle("hidden",!toolbarCaps.upload); $("storageNewFile").classList.toggle("hidden",!toolbarCaps.textEdit); $("storageCreate").classList.toggle("hidden",!toolbarCaps.createDirectory);
    $("storageProvider").value=id; renderBreadcrumbs(id,listing.path);
    const root=$("storageEntries");root.replaceChildren(); const header=document.createElement("div");header.className="storage-list-head";header.innerHTML="<span>Name</span><span>Size</span><span>Modified</span><span>Actions</span>";root.append(header);
    if (listing.parentPath != null) {
      const parent=document.createElement("article");parent.className="row storage-entry storage-row openable";
      const head=document.createElement("div");head.className="storage-name";const icon=document.createElement("span");icon.className="entry-icon folder";icon.setAttribute("aria-hidden","true");const title=document.createElement("h3");title.textContent="..";const meta=document.createElement("div");meta.className="meta";meta.textContent="parent folder";head.append(icon,title,meta);
      const size=document.createElement("div");size.className="storage-cell";size.textContent="—";const modified=document.createElement("div");modified.className="storage-cell";modified.textContent="—";const actions=document.createElement("div");actions.className="row-actions";parent.append(head,size,modified,actions);parent.addEventListener("click",()=>browseStorage(id,listing.parentPath));root.append(parent);
    }
    listing.entries.forEach(entry=>{
      const row=document.createElement("article");row.className="row storage-entry storage-row";row.dataset.storagePath=entry.path;
      const head=document.createElement("div");head.className="storage-name";const icon=document.createElement("span");icon.className=`entry-icon ${entryIcon(entry)}`;icon.setAttribute("aria-hidden","true");const title=document.createElement("h3");title.textContent=entry.name;head.append(icon);
      head.append(title);const meta=document.createElement("div");meta.className="meta";meta.textContent=entry.type;head.append(meta);
      const details=document.createElement("div");details.className="row-details";details.innerHTML=`<div class="kv"><span>Méret</span><span>${fmtBytes(entry.size)}</span></div><div class="kv"><span>Módosítva</span><span>${fmtDate(entry.modifiedAt)}</span></div>`;
      const actions=document.createElement("div");actions.className="row-actions";
      const caps=storagePathCapabilities||storage.find(x=>x.id===id)?.capabilities||{}, mutable=entry.type!=="filesystem-root", editable=entry.type==="file" && isEditableText(entry.name) && caps.textEdit;
      actions.append(actionSlot(entry.type==="file" && caps.download ? button("↓","Letöltés",()=>downloadStorage(id,entry.path)) : null));
      actions.append(actionSlot(editable ? button("✎","Szerkesztés",()=>openTextEditor(id,entry.path,entry.name)) : null));
      actions.append(actionSlot(mutable && caps.rename ? button("↺","Átnevezés",()=>renameStorage(id,entry.path)) : null));
      actions.append(actionSlot(mutable && caps.copy ? button("⧉","Másolás",()=>setStorageClipboard("copy",id,entry)) : null));
      actions.append(actionSlot(mutable && caps.move ? button("✂","Kivágás",()=>setStorageClipboard("move",id,entry)) : null));
      actions.append(caps[storageClipboard?.operation] && storageClipboard?.providerId===id ? button("📌","Beillesztés",()=>pasteStorage(id)) : actionSlot());
      actions.append(actionSlot(mutable && caps.delete ? button("🗑","Törlés",()=>deleteStorageObject(id,entry.path,entry.type)) : null));
      const size=document.createElement("div");size.className="storage-cell";size.textContent=entry.type==="file"?fmtBytes(entry.size):"—";const modified=document.createElement("div");modified.className="storage-cell";modified.textContent=fmtDate(entry.modifiedAt);row.append(head,size,modified,actions);
      if(entry.type!=="file") { row.classList.add("openable");row.addEventListener("click",()=>browseStorage(id,entry.path,entry.path)); }
      else { row.classList.add("openable");row.addEventListener("click",()=>isEditableText(entry.name)?openTextEditor(id,entry.path,entry.name):downloadStorage(id,entry.path)); }
      root.append(row);
    });
  } catch(e) { toast(e.message); }
  finally { if (id === "docker-volumes" && loadingEntry) setStorageEntryLoading(loadingEntry, false); }
}
function setStorageClipboard(operation, providerId, entry) { storageClipboard={providerId,operation,path:entry.path,name:entry.name}; toast(operation==="copy" ? `Másolásra kijelölve: ${entry.name}` : `Áthelyezésre kijelölve: ${entry.name}`); browseStorage(storageLocation,storagePath); }
async function pasteStorage(id) { if(!storageClipboard||storageClipboard.providerId!==id)return; try { const endpoint=storageClipboard.operation==="copy"?"copy":"move"; await api(`api/v1/storage/${id}/${endpoint}`,{method:"POST",body:JSON.stringify({sourcePath:storageClipboard.path,destinationDirectory:storagePath})}); const message=storageClipboard.operation==="copy"?"Másolva":"Áthelyezve"; if(storageClipboard.operation==="move")storageClipboard=null;toast(message);browseStorage(id,storagePath); }catch(e){toast(e.message)} }
function syntaxSpan(kind, value) { return `<span class="syntax-${kind}">${escapeHtml(value)}</span>`; }
function highlightText(content, name) {
  const ext=(name.split(".").pop()||"").toLowerCase(), yaml=["yaml","yml"].includes(ext), env=ext==="env"; const structured=["json","yaml","yml","toml","ini","xml","html","css"].includes(ext);
  return content.split("\n").map(line=>{
    if (/^\s*(#|\/\/)/.test(line)) return syntaxSpan("comment",line);
    const chunks=line.split(/("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')/g);
    return chunks.map((part,index)=>{
      if (index%2) return syntaxSpan("string",part);
      let safe=escapeHtml(part);
      if (structured) safe=safe.replace(/\b(true|false|null|yes|no)\b/gi,'<span class="syntax-keyword">$1</span>').replace(/\b(-?\d+(?:\.\d+)?)\b/g,'<span class="syntax-number">$1</span>');
      else safe=safe.replace(/\b(func|function|return|if|else|for|while|package|import|const|var|let|class|public|private|def|true|false|null|nil)\b/g,'<span class="syntax-keyword">$1</span>');
      if (yaml) safe=safe.replace(/^(\s*(?:-\s+)?)([A-Za-z0-9_.-]+)(\s*:)/,'$1<span class="syntax-key">$2</span>$3');
      if (env) safe=safe.replace(/^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)(=)/,'$1<span class="syntax-key">$2</span>$3');
      return safe;
    }).join("");
  }).join("\n")+"\n";
}
function updateTextHighlight() { const code=$("textHighlight").querySelector("code"); code.innerHTML=highlightText($("textEditorContent").value,editingTextPath?.path||""); }
function updateDockerFileHighlight() { const code=$("dockerFileHighlight").querySelector("code"); code.innerHTML=highlightText($("dockerFileContent").value,dockerEditing?.kind==="env"?".env":"compose.yaml"); }
async function openTextEditor(id,path,name) { try { const data=await api(`api/v1/storage/${id}/text?`+new URLSearchParams({path})); editingTextPath={id,path};const canEdit=!!(storagePathCapabilities||storage.find(x=>x.id===id)?.capabilities||{}).textEdit;$("textEditorTitle").textContent=`${canEdit ? "Edit" : "View"}: ${name}`;$("textEditorPath").textContent=path;$("textEditorContent").value=data.content;$("textEditorContent").readOnly=!canEdit;$("saveTextEditor").disabled=!canEdit;updateTextHighlight();$("textEditorDialog").showModal(); }catch(e){toast(e.message)} }
function openNewTextFile() { if (!storageLocation || !(storagePathCapabilities||{}).textEdit) return; const name=prompt("File name:"); if (!name) return; if (name === "." || name === ".." || /[\\/]/.test(name)) { toast("File name must be a single name."); return; } const path=storagePath ? `${storagePath}/${name}` : name; editingTextPath={id:storageLocation,path};$("textEditorTitle").textContent=`New file: ${name}`;$("textEditorPath").textContent=path;$("textEditorContent").value="";$("textEditorContent").readOnly=false;$("saveTextEditor").disabled=false;updateTextHighlight();$("textEditorDialog").showModal(); }
async function saveTextEditor() { if(!editingTextPath)return;try{await api(`api/v1/storage/${editingTextPath.id}/text`,{method:"PUT",body:JSON.stringify({path:editingTextPath.path,content:$("textEditorContent").value})});$("textEditorDialog").close();toast("Fájl mentve");browseStorage(storageLocation,storagePath)}catch(e){toast(e.message)} }

async function processAction(id, action) {
  try { await api(`api/v1/processes/${id}/${action}`, {method:"POST"}); toast(`Continuous task ${action} requested`); setTimeout(refresh, 250); }
  catch (e) { toast(e.message); }
}

async function runJob(id) {
  try { await api(`api/v1/jobs/${id}/run`, {method:"POST"}); toast("Task started"); setTimeout(refresh, 250); }
  catch (e) { toast(e.message); }
}

async function deleteProcess(id) {
  if (!confirm("Delete this continuous task?")) return;
  try { await api(`api/v1/processes/${id}`, {method:"DELETE"}); await refresh(); }
  catch (e) { toast(e.message); }
}

async function deleteJob(id) {
  if (!confirm("Delete this job?")) return;
  try { await api(`api/v1/jobs/${id}`, {method:"DELETE"}); await refresh(); }
  catch (e) { toast(e.message); }
}

function activeSoftwareProvider() { return softwareProviders.find(provider => provider.id === softwareProviderID) || softwareProviders[0]; }
function softwarePackageFacts(pkg) {
  const facts = [{label: pkg.installed ? "Installed" : "Version", value: pkg.version || "—"}];
  if (pkg.availableVersion) facts.push({label: "Latest", value: pkg.availableVersion});
  if (pkg.bucket) facts.push({label: "Source", value: pkg.bucket});
  return facts.map(fact => `<div><span>${escapeHtml(fact.label)}</span><strong>${escapeHtml(fact.value)}</strong></div>`).join("");
}
function renderSoftware() {
  const provider = activeSoftwareProvider();
  const card = $("softwareProviderCard"), packages = $("softwarePackages"), picker = $("softwareProviderSelect");
  picker.replaceChildren();
  softwareProviders.forEach(item => { const option = document.createElement("option"); option.value = item.id; option.textContent = `${item.name} (${item.type})`; picker.append(option); });
  if (!provider) {
    picker.parentElement.classList.add("hidden");
    $("softwareTabs").classList.add("hidden");
    $("softwareSearchBar").classList.add("hidden");
    $("softwareBucketBar").classList.add("hidden");
    card.innerHTML = `<div class="empty compact"><h2>No software providers available</h2><p>No supported software providers are configured for this operating system.</p></div>`;
    packages.replaceChildren();
    return;
  }
  picker.parentElement.classList.remove("hidden");
  picker.value = provider.id;
  const state = provider.state || "unavailable";
  const message = provider.message || "Preparing the managed Scoop runtime.";
  const hasRootDraft = Object.prototype.hasOwnProperty.call(softwareRootDrafts, provider.id);
  const rootValue = hasRootDraft ? softwareRootDrafts[provider.id] : provider.usingDefaultRoot ? "" : provider.root || "";
  card.innerHTML = `<article class="software-provider-summary">
    <div class="software-provider-identity"><div class="software-provider-title"><h2>${escapeHtml(provider.name)}</h2>${statusBadge(state)}</div></div>
    <label class="software-root-editor"><span>Root</span><input id="softwareRoot" value="${escapeHtml(rootValue)}" placeholder="${escapeHtml(provider.root || "Managed Scoop root")}" ${softwareBusy ? "disabled" : ""}><small>Leave empty to use the RunPilot data-directory default. Changing roots does not move or delete applications.</small></label>
    <div class="row-actions software-provider-actions"><button class="button secondary small" onclick="saveSoftwareRoot()" ${softwareBusy ? "disabled" : ""}>Save root</button><button class="button secondary small" onclick="softwareRefresh()" ${softwareBusy ? "disabled" : ""}>${state === "ready" ? "Refresh" : "Retry"}</button></div>
  </article>`;
  $("softwareTabs").classList.toggle("hidden", state !== "ready");
  $("softwareUpgradeAll").classList.toggle("hidden", softwareTab !== "updates" || !softwareUpdates.length);
  $("softwareUpgradeAll").disabled = softwareBusy;
  $("softwareSearchBar").classList.toggle("hidden", state !== "ready" || softwareTab !== "search");
  $("softwareBucketBar").classList.toggle("hidden", state !== "ready" || softwareTab !== "buckets");
  $("softwareAddBucket").disabled = softwareBusy || state !== "ready";
  $("softwareAddBucket").textContent = softwareBusy && softwareTab === "buckets" ? "Working…" : "Add bucket";
  document.querySelectorAll("[data-software-tab]").forEach(b => b.classList.toggle("active", b.dataset.softwareTab === softwareTab));
  if (state !== "ready") { packages.innerHTML = `<div class="empty compact"><h2>${state === "initializing" ? "Preparing RunPilot Software Management…" : "Software unavailable"}</h2><p>${escapeHtml(message)}</p></div>`; return; }
  if (softwareLoading) { packages.innerHTML = `<div class="software-loading" role="status"><span class="spinner" aria-hidden="true"></span><strong>Loading ${softwareTab === "buckets" ? "buckets" : "applications"}…</strong><span>Querying the selected provider.</span></div>`; return; }
  if (softwareTab === "buckets" && softwareBusy) { packages.innerHTML = `<div class="software-loading" role="status"><span class="spinner" aria-hidden="true"></span><strong>${escapeHtml(softwareBusyLabel || "Updating buckets")}…</strong><span>Waiting for Scoop to finish the bucket operation.</span></div>`; return; }
  if (softwareTab === "buckets") {
    packages.innerHTML = softwareBuckets.length ? softwareBuckets.map(bucket => `<article class="software-bucket-row"><div><h3>${escapeHtml(bucket.name)}</h3><div class="meta">${escapeHtml(bucket.source || "Scoop default source")}</div></div><div class="row-actions">${bucket.protected ? `<span class="software-protected" title="This bucket is required by Scoop">Required by Scoop</span>` : `<button class="button danger small" onclick="softwareRemoveBucket('${escapeHtml(bucket.name)}')" ${softwareBusy ? "disabled" : ""}>Remove</button>`}</div></article>`).join("") : `<div class="empty compact"><h2>No additional buckets</h2><p>Add a trusted Scoop bucket to make its applications available for search.</p></div>`;
    return;
  }
  const items = softwareTab === "updates" ? softwareUpdates : softwareTab === "search" ? softwareSearchResults : softwarePackages;
  packages.innerHTML = items.length ? items.map(p => `<article class="software-package-row"><div class="software-package-name"><h3>${escapeHtml(p.name)}</h3><div class="meta">${escapeHtml(p.description || p.bucket || "Scoop main bucket")}</div></div>${p.updateAvailable ? statusBadge("update") : p.installed ? statusBadge("installed") : ""}<div class="software-package-meta">${softwarePackageFacts(p)}</div><div class="row-actions">${p.protected ? `${p.installed && p.updateAvailable ? `<button class="button software-protected-action small" disabled>Upgrade</button>` : ""}<button class="button software-protected-action small" disabled>${p.installed ? "Uninstall" : "Install"}</button>` : p.installed ? `${p.updateAvailable ? `<button class="button primary small" onclick="softwareUpgrade('${escapeHtml(p.id)}')" ${softwareBusy ? "disabled" : ""}>Upgrade</button>` : ""}<button class="button danger small" onclick="softwareUninstall('${escapeHtml(p.id)}')" ${softwareBusy ? "disabled" : ""}>Uninstall</button>` : `<button class="button primary small" onclick="softwareInstall('${escapeHtml(p.id)}')" ${softwareBusy ? "disabled" : ""}>Install</button>`}</div></article>`).join("") : `<div class="empty compact"><h2>${softwareTab === "search" ? "Search the managed buckets" : softwareTab === "updates" ? "No updates found" : "No RunPilot-managed applications"}</h2><p>${softwareTab === "installed" ? "Applications installed in another Scoop root are deliberately not shown here." : ""}</p></div>`;
}
function changeSoftwareProvider(id) { if (id === softwareProviderID) return; softwareProviderID = id; softwarePackages = []; softwareUpdates = []; softwareSearchResults = []; softwareBuckets = []; softwareLoadedKey = ""; softwareLoadSequence++; softwareLoading = true; renderSoftware(); refresh(); }
async function softwareAction(path, options = {}, busyLabel = "") { softwareBusy = true; softwareBusyLabel = busyLabel; renderSoftware(); let completed = false; try { await api(path, options); completed = true; softwareLoadedKey = ""; toast("Software operation completed"); await refresh(); } catch (e) { toast(e.message); await refresh(); } finally { softwareBusy = false; softwareBusyLabel = ""; renderSoftware(); } return completed; }
function softwareInstall(id) { const p=activeSoftwareProvider(); if(p) softwareAction(`api/v1/software/providers/${p.id}/install`, {method:"POST", body:JSON.stringify({package:id})}); }
function softwareUpgrade(id) { const p=activeSoftwareProvider(); if(p) softwareAction(`api/v1/software/providers/${p.id}/packages/${encodeURIComponent(id)}/upgrade`, {method:"POST"}); }
function softwareUninstall(id) { const p=activeSoftwareProvider(); if(p && confirm(`Uninstall ${id} from the RunPilot-managed Scoop root?`)) softwareAction(`api/v1/software/providers/${p.id}/packages/${encodeURIComponent(id)}/uninstall`, {method:"POST"}); }
function softwareRefresh() { const p=activeSoftwareProvider(); if(p) softwareAction(`api/v1/software/providers/${p.id}/refresh`, {method:"POST"}); }
function softwareUpgradeAll() { const p=activeSoftwareProvider(); if(p && confirm("Upgrade all RunPilot-managed Scoop applications?")) softwareAction(`api/v1/software/providers/${p.id}/upgrade-all`, {method:"POST"}); }
function saveSoftwareRoot() { const p=activeSoftwareProvider(); if(!p)return; const root = $("softwareRoot").value.trim(); softwareRootDrafts[p.id] = root; softwareAction(`api/v1/software/providers/${p.id}`, {method:"PUT",body:JSON.stringify({scoop:{root}})}); }
function softwareAddBucket() { const p = activeSoftwareProvider(), name = $("softwareBucketName").value.trim(), source = $("softwareBucketSource").value.trim(); if (!p || !name) { toast("A bucket name is required."); return; } softwareAction(`api/v1/software/providers/${p.id}/buckets`, {method:"POST", body:JSON.stringify({name, source})}, "Adding bucket").then(completed => { if (completed) { $("softwareBucketName").value = ""; $("softwareBucketSource").value = ""; } }); }
function softwareRemoveBucket(name) { const p = activeSoftwareProvider(); if (p && confirm(`Remove Scoop bucket ${name}?`)) softwareAction(`api/v1/software/providers/${p.id}/buckets/${encodeURIComponent(name)}`, {method:"DELETE"}, "Removing bucket"); }
function softwareViewKey() { const p = activeSoftwareProvider(); if (!p || p.state !== "ready") return ""; const query = softwareTab === "search" ? $("softwareSearchInput").value.trim() : ""; return `${p.id}|${softwareTab}|${query}`; }
async function loadSoftwareView(force = false) {
  const p = activeSoftwareProvider(), query = $("softwareSearchInput").value.trim();
  if (!p || p.state !== "ready") return;
  if (softwareTab === "search" && !query) { softwareSearchResults = []; softwareLoading = false; renderSoftware(); return; }
  const key = softwareViewKey();
  if (!force && key === softwareLoadedKey) return;
  if (!force && softwareLoading && key === softwareLoadingKey) return;
  const sequence = ++softwareLoadSequence;
  softwareLoading = true; softwareLoadingKey = key; renderSoftware();
  try {
    const result = softwareTab === "installed" ? await api(`api/v1/software/providers/${p.id}/installed`) : softwareTab === "updates" ? await api(`api/v1/software/providers/${p.id}/updates`) : softwareTab === "buckets" ? await api(`api/v1/software/providers/${p.id}/buckets`) : await api(`api/v1/software/providers/${p.id}/search?` + new URLSearchParams({q:query}));
    if (sequence !== softwareLoadSequence) return;
    const items = Array.isArray(result) ? result : [];
    if (softwareTab === "installed") softwarePackages = items;
    else if (softwareTab === "updates") softwareUpdates = items;
    else if (softwareTab === "buckets") softwareBuckets = items;
    else softwareSearchResults = items;
    softwareLoadedKey = key;
  } catch (e) { if (sequence === softwareLoadSequence) toast(e.message); }
  finally { if (sequence === softwareLoadSequence) { softwareLoading = false; softwareLoadingKey = ""; renderSoftware(); } }
}
function softwareSearch() { loadSoftwareView(true); }

function renderDocker() {
  const notice = $("dockerNotice"), ready = dockerRuntime?.available;
  notice.classList.toggle("hidden", !!ready);
  notice.innerHTML = ready ? "" : `<strong>Docker unavailable</strong><span>${escapeHtml(dockerRuntime?.message || "Docker status has not been checked.")}${dockerRuntime?.identity ? ` Running as ${escapeHtml(dockerRuntime.identity)}.` : ""}</span>`;
  $("dockerProjectGrid").innerHTML = dockerProjects.map(project => dockerProjectCard(project, ready)).join("");
  $("dockerVolumeList").innerHTML = dockerVolumes.map(volume => dockerVolumeRow(volume, ready)).join("");
  $("dockerNetworkList").innerHTML = dockerNetworks.map(network => dockerNetworkRow(network, ready)).join("");
  $("dockerProjectEmpty").classList.toggle("hidden", dockerProjects.length > 0 || !ready);
  $("dockerVolumeEmpty").classList.toggle("hidden", dockerVolumes.length > 0 || !ready);
  $("dockerNetworkEmpty").classList.toggle("hidden", dockerNetworks.length > 0 || !ready);
}
function applyDockerSnapshot(snapshot) {
  dockerRuntime = snapshot[0].runtime; dockerProjects = snapshot[0].projects || []; dockerVolumes = snapshot[1].volumes || []; dockerNetworks = snapshot[2].networks || [];
  for (const [key, pending] of dockerPendingContainerStates) {
    const container = dockerProjects.flatMap(project => project.containers || []).find(item => item.id === pending.id);
    const reachedExpectedState = pending.running == null ? !container : !!container && (container.state === "running") === pending.running;
    if (reachedExpectedState) { dockerPendingContainerStates.delete(key); dockerBusy.delete(key); }
  }
  renderDocker();
}
async function refreshDockerSnapshot() {
  try {
    const snapshot = await Promise.all([api("api/v1/docker/projects"), api("api/v1/docker/volumes"), api("api/v1/docker/networks")]);
    applyDockerSnapshot(snapshot);
    setConnected(true);
    return true;
  } catch (error) {
    setConnected(!!error.serverReachable);
    toast(error.message);
    return false;
  }
}
function dockerNetworkRow(network, ready) {
  const busy=dockerBusy.has(`network:${network.name}`), usage=!network.inUse ? "Unused" : network.runningUse ? "In use by running container" : "Used by stopped container";
  const protectedNetwork=["bridge","host","none"].includes(network.name);
  const detail=network.composeProject ? `Compose: ${network.composeProject}${network.composeNetwork ? ` / ${network.composeNetwork}` : ""}` : `${network.driver || "unknown driver"} · ${network.scope || "local"}`;
  return `<article class="docker-volume-row" title="${escapeHtml(detail)}"><strong>${escapeHtml(network.name)}</strong><span class="docker-volume-users" title="${escapeHtml(usage)}${(network.usedBy||[]).length ? ` · ${escapeHtml(network.usedBy.join(", "))}` : ""}"><span class="docker-dot ${network.inUse ? (network.runningUse ? "green" : "yellow") : "gray"}"></span>${escapeHtml(usage)}</span>${protectedNetwork ? "" : `<button class="button danger small" onclick="deleteDockerNetwork('${escapeHtml(network.name)}')" ${!ready || network.inUse || busy ? "disabled" : ""}>Delete</button>`}</article>`;
}
function dockerVolumeRow(volume, ready) {
  const busy = dockerBusy.has(`volume:${volume.name}`), usage = !volume.inUse ? "Unused" : volume.runningUse ? "In use by running container" : "Used by stopped container";
  const canDelete = ready && !volume.inUse && !busy;
  const deleteTitle = volume.inUse ? "Delete is unavailable while a container references this volume." : !ready ? "Docker is unavailable." : busy ? "Volume operation in progress." : "Delete volume";
  const detail = volume.composeProject ? `Compose: ${volume.composeProject}${volume.composeVolume ? ` / ${volume.composeVolume}` : ""}` : `${volume.driver || "unknown driver"} · ${volume.scope || "local"}`;
  const users = `<span class="docker-volume-users" title="${escapeHtml(usage)}${(volume.usedBy || []).length ? ` · ${escapeHtml(volume.usedBy.join(", "))}` : ""}"><span class="docker-dot ${volume.inUse ? (volume.runningUse ? "green" : "yellow") : "gray"}"></span>${escapeHtml(usage)}</span>`;
  return `<article class="docker-volume-row" title="${escapeHtml(detail)}"><strong>${escapeHtml(volume.name)}</strong>${users}<span class="docker-volume-storage" title="Available automatically in Storage">Storage</span><button class="button danger small" title="${escapeHtml(deleteTitle)}" onclick="deleteDockerVolume('${escapeHtml(volume.name)}')" ${canDelete ? "" : "disabled"}>Delete</button></article>`;
}
function dockerProjectCard(project, ready) {
  const busy = dockerBusy.has(project.name), managed = !!project.managed, hasCompose = !!project.composeFileExists;
  const error = dockerProjectErrors.get(project.name);
  const active = ["running","partial","degraded"].includes(project.state);
  // `docker compose up -d` is idempotent and also applies configuration changes
  // to an already-running project, so it is valid in every project state.
  const canUp = ready && hasCompose && !busy;
  const canStart = ready && hasCompose && !busy && project.state === "stopped";
  const canStop = ready && !busy && active;
  const canDown = ready && hasCompose && !busy && project.state !== "down";
  const actions = managed ? `<div class="docker-actions">
    <div class="docker-action-group docker-action-lifecycle"><button class="button small" onclick="dockerAction('${project.name}','up')" ${canUp ? "" : "disabled"}>Up</button><button class="button small" onclick="dockerAction('${project.name}','start')" ${canStart ? "" : "disabled"}>Start</button><button class="button small" onclick="dockerAction('${project.name}','stop')" ${canStop ? "" : "disabled"}>Stop</button><button class="button small" onclick="dockerAction('${project.name}','down')" ${canDown ? "" : "disabled"}>Down</button></div>
    <div class="docker-action-group docker-action-files"><button class="button secondary small" onclick="openDockerFile('${project.name}','compose')" ${busy ? "disabled" : ""}>compose.yaml</button><button class="button secondary small" onclick="openDockerFile('${project.name}','env')" ${busy ? "disabled" : ""}>.env</button></div><div class="docker-action-group docker-action-destructive"><button class="button danger small" onclick="deleteDockerProject('${project.name}')" ${!ready || project.state !== "down" || busy ? "disabled" : ""}>Delete</button></div>
  </div>${busy ? `<div class="docker-busy" role="status"><span class="spinner" aria-hidden="true"></span>Running Compose command…</div>` : ""}` : "";
  const containers = (project.containers || []).map(c => dockerContainerRow(c, ready && managed)).join("");
  return `<article class="docker-card"><div class="docker-card-head"><h2>${escapeHtml(project.name)}</h2><span class="status ${escapeHtml(project.state === "running" ? "running" : project.state === "degraded" ? "failure" : project.state || "idle")}">${escapeHtml(project.state || "unknown")}</span></div>${error ? `<div class="docker-project-error" role="alert"><strong>Compose operation failed</strong><span>${escapeHtml(error)}</span></div>` : ""}${actions}${containers}${project.configPath && !managed ? `<div class="docker-path">${escapeHtml(project.configPath)}</div>` : ""}</article>`;
}
function dockerContainerRow(container, ready) {
  const busy=dockerBusy.has(`container:${container.id}`), running=container.state === "running";
  const canAttach=ready && running && !busy && !!systemInfo?.capabilities?.terminal;
  const controls=`<div class="docker-container-actions"><div class="docker-action-group docker-action-lifecycle"><button class="row-icon docker-container-icon" title="Start" aria-label="Start container" onclick="dockerContainerAction('${container.id}','start')" ${ready && !running && !busy ? "" : "disabled"}>▶</button><button class="row-icon docker-container-icon" title="Stop" aria-label="Stop container" onclick="dockerContainerAction('${container.id}','stop')" ${ready && running && !busy ? "" : "disabled"}>■</button></div><div class="docker-action-group docker-action-destructive"><button class="row-icon docker-container-icon" title="Delete stopped container" aria-label="Delete stopped container" onclick="dockerContainerAction('${container.id}','delete')" ${ready && !running && !busy ? "" : "disabled"}>🗑</button></div><div class="docker-action-group docker-action-support"><button class="row-icon docker-container-icon" title="Open terminal" aria-label="Open container terminal" onclick="dockerAttachContainer('${container.id}')" ${canAttach ? "" : "disabled"}>↪</button><button class="row-icon docker-container-icon" title="View logs" aria-label="View logs" onclick="openDockerContainerLog('${container.id}')" ${ready && !busy ? "" : "disabled"}>▤</button></div></div>`;
  return `<div class="docker-container"><span class="docker-dot ${escapeHtml(container.tone || "gray")}"></span><div class="docker-container-main"><strong>${escapeHtml(container.service || container.name)}</strong>${controls}</div><span>${escapeHtml(container.state || "unknown")}${container.health ? ` · ${escapeHtml(container.health)}` : ""}</span></div>`;
}
async function dockerAction(name, action) {
  dockerBusy.add(name); dockerProjectErrors.delete(name); renderDocker();
  try {
    await api(`api/v1/docker/projects/${encodeURIComponent(name)}/actions/${action}`, {method:"POST"});
    toast(`${name}: ${action} completed`);
  } catch(e) {
    dockerProjectErrors.set(name, e.message); renderDocker(); toast(e.message);
  } finally { dockerBusy.delete(name); await refresh(); }
}
async function dockerContainerAction(id, action) {
  const key=`container:${id}`;
  if(action==="delete"&&!confirm("Delete this stopped container?"))return;
  dockerBusy.add(key); renderDocker();
  try {
    await api(`api/v1/docker/containers/${encodeURIComponent(id)}/actions/${action}`,{method:"POST"});
    dockerPendingContainerStates.set(key,{id,running:action === "start" ? true : action === "stop" ? false : null});
    toast(`Container ${action} completed`);
    await new Promise(resolve => setTimeout(resolve, 400));
    await refreshDockerSnapshot();
  } catch(e) {
    dockerBusy.delete(key); dockerPendingContainerStates.delete(key); renderDocker(); toast(e.message);
  }
}
function openDockerContainerLog(id) { openLog("Container logs",()=>api(`api/v1/docker/containers/${encodeURIComponent(id)}/logs`)); }
async function dockerAttachContainer(id) {
  if (!systemInfo?.capabilities?.terminal) { toast("Interactive terminals are unavailable"); return; }
  disposeDockerAttachTerminal();
  const dialog = $("dockerAttachDialog"), pane = $("dockerAttachPane");
  dialog.showModal();
  pane.replaceChildren();
  const term = new Terminal({cursorBlink:true, scrollback:5000, fontFamily:'"Cascadia Code", Consolas, monospace', fontSize:14, theme:{background:'#0b1220'}});
  const fit = new FitAddon.FitAddon(); term.loadAddon(fit); term.open(pane); fit.fit();
  const session = {term, fit, socket:null, observer:null}; dockerAttachTerminal = session;
  term.onData(data => { if (session.socket?.readyState === WebSocket.OPEN) session.socket.send(new TextEncoder().encode(data)); });
  session.observer = new ResizeObserver(() => { fit.fit(); if (session.socket?.readyState === WebSocket.OPEN) session.socket.send(JSON.stringify({type:"resize",cols:term.cols,rows:term.rows})); });
  session.observer.observe(pane);
  try {
    const ticket = await api(`api/v1/docker/containers/${encodeURIComponent(id)}/attach-ticket`, {method:"POST", body:JSON.stringify({cols:term.cols, rows:term.rows})});
    if (dockerAttachTerminal !== session) return;
    const socket = new WebSocket(terminalWebSocketURL(ticket.ticket)); session.socket = socket; socket.binaryType = "arraybuffer";
    socket.onmessage = event => { if (dockerAttachTerminal === session && event.data instanceof ArrayBuffer) term.write(new Uint8Array(event.data)); };
    socket.onerror = () => { if (dockerAttachTerminal === session) term.writeln("\r\nTerminal connection failed."); };
    socket.onclose = event => { if (dockerAttachTerminal === session && !event.wasClean) term.writeln(`\r\n${event.reason || "Terminal connection closed."}`); };
    socket.onopen = () => { socket.send(JSON.stringify({type:"resize",cols:term.cols,rows:term.rows})); term.focus(); };
  } catch (error) { term.writeln(`\r\n${error.message}`); toast(error.message); }
}
function disposeDockerAttachTerminal() { const session = dockerAttachTerminal; dockerAttachTerminal = null; session?.observer?.disconnect(); session?.socket?.close(); session?.term?.dispose(); }
async function deleteDockerProject(name) { if (!confirm(`Delete the RunPilot project directory for ${name}? This removes compose files and .env only; it never removes Docker images or volumes.`)) return; dockerBusy.add(name); dockerProjectErrors.delete(name); renderDocker(); try { await api(`api/v1/docker/projects/${encodeURIComponent(name)}`, {method:"DELETE"}); dockerProjectErrors.delete(name); toast("Compose project deleted"); } catch(e) { dockerProjectErrors.set(name, e.message); renderDocker(); toast(e.message); } finally { dockerBusy.delete(name); await refresh(); } }
async function deleteDockerVolume(name) { if (!confirm(`Permanently delete Docker volume ${name} and all of its data? This cannot be undone.`)) return; const key=`volume:${name}`; dockerBusy.add(key);renderDocker();try{await api(`api/v1/docker/volumes/${encodeURIComponent(name)}`,{method:"DELETE"});toast("Docker volume deleted");}catch(e){toast(e.message)}finally{dockerBusy.delete(key);await refresh();} }
async function deleteDockerNetwork(name) { if (!confirm(`Delete Docker network ${name}? This cannot be undone.`)) return; const key=`network:${name}`;dockerBusy.add(key);renderDocker();try{await api(`api/v1/docker/networks/${encodeURIComponent(name)}`,{method:"DELETE"});toast("Docker network deleted");}catch(e){toast(e.message)}finally{dockerBusy.delete(key);await refresh();} }
async function openDockerFile(name, kind) { try { const out = await api(`api/v1/docker/projects/${encodeURIComponent(name)}/files/${kind}`); dockerEditing = {name,kind}; $("dockerFileTitle").textContent = `${out.content ? "Edit" : "Create"} ${kind === "compose" ? "compose.yaml" : ".env"}`; $("dockerFilePath").textContent = `${name}/${kind === "compose" ? "compose.yaml" : ".env"}`; $("dockerFileContent").value = out.content || (kind === "compose" ? "services: {}\n" : ""); updateDockerFileHighlight(); $("dockerFileDialog").showModal(); } catch(e) { toast(e.message); } }
async function saveDockerFile() { if (!dockerEditing) return; try { await api(`api/v1/docker/projects/${encodeURIComponent(dockerEditing.name)}/files/${dockerEditing.kind}`, {method:"PUT", body:JSON.stringify({content:$("dockerFileContent").value})}); $("dockerFileDialog").close(); toast("File saved"); await refresh(); } catch(e) { toast(e.message); } }
function openDockerCreate() { $("dockerProjectName").value=""; $("dockerCreateError").classList.add("hidden"); $("dockerCreateDialog").showModal(); }
$("dockerCreateForm").addEventListener("submit", async e => { e.preventDefault(); const name=$("dockerProjectName").value.trim(); try { await api("api/v1/docker/projects", {method:"POST",body:JSON.stringify({name})}); $("dockerCreateDialog").close(); await refresh(); } catch(err) { $("dockerCreateError").textContent=err.message; $("dockerCreateError").classList.remove("hidden"); } });
function openDockerVolumeCreate() { $("dockerVolumeName").value="";$("dockerVolumeError").classList.add("hidden");$("dockerVolumeDialog").showModal(); }
$("dockerVolumeForm").addEventListener("submit",async e=>{e.preventDefault();try{await api("api/v1/docker/volumes",{method:"POST",body:JSON.stringify({name:$("dockerVolumeName").value.trim()})});$("dockerVolumeDialog").close();await refresh();}catch(err){$("dockerVolumeError").textContent=err.message;$("dockerVolumeError").classList.remove("hidden");}});
function openDockerNetworkCreate() { $("dockerNetworkName").value="";$("dockerNetworkError").classList.add("hidden");$("dockerNetworkDialog").showModal(); }
$("dockerNetworkForm").addEventListener("submit",async e=>{e.preventDefault();try{await api("api/v1/docker/networks",{method:"POST",body:JSON.stringify({name:$("dockerNetworkName").value.trim()})});$("dockerNetworkDialog").close();await refresh();}catch(err){$("dockerNetworkError").textContent=err.message;$("dockerNetworkError").classList.remove("hidden");}});
$("saveDockerFile").addEventListener("click", saveDockerFile); $("closeDockerFile").addEventListener("click",()=>$("dockerFileDialog").close()); $("cancelDockerFile").addEventListener("click",()=>$("dockerFileDialog").close());
$("closeDockerAttach").addEventListener("click",()=>$("dockerAttachDialog").close());
$("dockerAttachDialog").addEventListener("close",disposeDockerAttachTerminal);

async function loadTerminalInfo() {
  systemInfo = await api("api/v1/system");
  configurePlatformAwareFields(systemInfo.capabilities || {});
  const enabled = !!systemInfo?.capabilities?.terminal;
  $("terminalNav").classList.toggle("hidden", !enabled);
	$("dockerNav").classList.toggle("hidden", !systemInfo?.capabilities?.dockerCompose);
  if (!enabled) return;
  terminalInfo = await api("api/v1/terminal");
  const select = $("terminalShell");
  select.replaceChildren();
  for (const shell of terminalInfo.shells || []) {
    const option = document.createElement("option"); option.value = shell.id; option.textContent = shell.name; select.append(option);
  }
  select.value = terminalInfo.defaultShell || "";
  $("terminalUnavailableMessage").textContent = terminalInfo.available ? "" : "No supported interactive shell could be started on this host.";
  $("terminalUnavailable").classList.toggle("hidden", !!terminalInfo.available);
  $("terminalWorkspace").classList.toggle("hidden", !terminalInfo.available);
  $("newTerminal").disabled = !terminalInfo.available;
}

function configurePlatformAwareFields(capabilities) {
  const labels = {direct:"Direct executable", python:"Python", powershell:"PowerShell", cmd:"CMD / batch", sh:"POSIX shell (sh)", bash:"Bash"};
  const interpreters = new Set(capabilities.commandInterpreters || []);
  document.querySelectorAll("select[id$='Interpreter']").forEach(select => {
    const selected = select.value || "auto";
    for (const option of select.options) {
      if (option.value === "auto" || option.value === "direct") continue;
      option.hidden = !interpreters.has(option.value);
      option.disabled = !interpreters.has(option.value);
      if (labels[option.value]) option.textContent = labels[option.value];
    }
    if (select.querySelector(`option[value="${selected}"]`)?.disabled) select.value = "auto";
  });
  const robocopy = $("backupProvider").querySelector('option[value="robocopy"]');
  if (robocopy) { robocopy.disabled = !capabilities.robocopy; robocopy.hidden = !capabilities.robocopy; }
  const vss = $("resticVss").closest("label");
  if (vss) vss.classList.toggle("hidden", !capabilities.windows);
}

function renderTerminalTabs() {
  const tabs = $("terminalTabs"); tabs.replaceChildren();
  for (const tab of terminalTabs) {
    const button = document.createElement("div"); button.className = `terminal-tab${tab.id === activeTerminalID ? " active" : ""}`; button.tabIndex = 0; button.setAttribute("role", "tab");
    const label = document.createElement("span"); label.textContent = tab.shell.name; button.append(label);
    const close = document.createElement("button"); close.type = "button"; close.className = "terminal-tab-close"; close.textContent = "×"; close.setAttribute("aria-label", `Close ${tab.shell.name}`);
    close.addEventListener("click", event => { event.stopPropagation(); closeTerminal(tab.id); }); button.append(close);
    button.addEventListener("click", () => activateTerminal(tab.id)); button.addEventListener("keydown", event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); activateTerminal(tab.id); } }); tabs.append(button);
  }
  $("terminalWorkspace").classList.toggle("hidden", !terminalInfo?.available || terminalTabs.length === 0);
}

function activateTerminal(id) {
  activeTerminalID = id;
  terminalTabs.forEach(tab => tab.pane.classList.toggle("active", tab.id === id));
  renderTerminalTabs();
  const tab = terminalTabs.find(item => item.id === id); if (tab) { tab.fit.fit(); tab.term.focus(); sendTerminalResize(tab); }
}

function terminalWebSocketURL(ticket) {
  const wsURL = new URL("api/v1/terminal/connect", document.baseURI);
  wsURL.protocol = wsURL.protocol === "https:" ? "wss:" : "ws:";
  wsURL.searchParams.set("ticket", ticket);
  return wsURL;
}

function sendTerminalResize(tab) {
  if (tab.socket?.readyState === WebSocket.OPEN) tab.socket.send(JSON.stringify({type:"resize", cols:tab.term.cols, rows:tab.term.rows}));
}

async function openTerminalSession(shell, requestTicket) {
  const id = crypto.randomUUID();
  const pane = document.createElement("div"); pane.className = "terminal-pane active"; pane.id = `terminal-pane-${id}`; $("terminalPanes").append(pane);
  const term = new Terminal({cursorBlink:true, scrollback:5000, fontFamily:'"Cascadia Code", Consolas, monospace', fontSize:14, theme:{background:'#0b1220'}});
  const fit = new FitAddon.FitAddon(); term.loadAddon(fit); term.open(pane); fit.fit();
  const tab = {id, shell, pane, term, fit, socket:null, observer:null}; terminalTabs.push(tab); activateTerminal(id);
  term.onData(data => { if (tab.socket?.readyState === WebSocket.OPEN) tab.socket.send(new TextEncoder().encode(data)); });
  tab.observer = new ResizeObserver(() => { fit.fit(); sendTerminalResize(tab); }); tab.observer.observe(pane);
  try {
    const ticket = await requestTicket(term);
    const socket = new WebSocket(terminalWebSocketURL(ticket.ticket)); tab.socket = socket; socket.binaryType = "arraybuffer";
    socket.onmessage = event => { if (event.data instanceof ArrayBuffer) term.write(new Uint8Array(event.data)); };
    socket.onerror = () => term.writeln("\r\nTerminal connection failed.");
    socket.onclose = event => { if (!event.wasClean) term.writeln(`\r\n${event.reason || "Terminal connection closed."}`); };
    socket.onopen = () => { sendTerminalResize(tab); term.focus(); };
  } catch (error) { term.writeln(`\r\n${error.message}`); toast(error.message); }
}

async function newTerminal() {
  if (!terminalInfo?.available) { toast("Terminal is unavailable"); return; }
  const shell = (terminalInfo.shells || []).find(item => item.id === $("terminalShell").value) || terminalInfo.shells[0];
  if (!shell) { toast("Shell executable was not found."); return; }
  await openTerminalSession(shell, term => api("api/v1/terminal/ticket", {method:"POST", body:JSON.stringify({shell:shell.id, cols:term.cols, rows:term.rows})}));
}

function closeTerminal(id) {
  const index = terminalTabs.findIndex(tab => tab.id === id); if (index < 0) return;
  const [tab] = terminalTabs.splice(index, 1); tab.observer?.disconnect(); tab.socket?.close(); tab.term.dispose(); tab.pane.remove();
  activeTerminalID = terminalTabs[0]?.id || ""; if (activeTerminalID) activateTerminal(activeTerminalID); else renderTerminalTabs();
}

function setPage(page) {
  currentPage = page;
	if (page !== "remote") document.querySelector("main").classList.remove("remote-active");
  document.querySelectorAll(".nav").forEach(n => n.classList.toggle("active", n.dataset.page === page));
  document.querySelectorAll(".page").forEach(p => p.classList.remove("active"));
  $(`${page}Page`).classList.add("active");
  const [title, sub, action] = pageMeta[page];
  $("pageTitle").textContent = title;
  $("pageSubtitle").textContent = sub;
  $("primaryAction").textContent = action || "";
  $("primaryAction").classList.toggle("hidden", !action);
	$("dockerVolumeAction").classList.toggle("hidden", page !== "docker");
	$("dockerNetworkAction").classList.toggle("hidden", page !== "docker");
	if (page === "software") renderSoftware();
	if (page === "docker") renderDocker();
	if (page === "remote") renderRemote();
	if (page === "terminal" && terminalInfo?.available && terminalTabs.length === 0) newTerminal();
  refresh();
}

document.querySelectorAll(".nav").forEach(n => n.addEventListener("click", () => setPage(n.dataset.page)));
document.querySelectorAll("[data-software-tab]").forEach(button => button.addEventListener("click", () => { softwareTab = button.dataset.softwareTab; renderSoftware(); loadSoftwareView(); }));
$("softwareSearchButton").addEventListener("click", softwareSearch);
$("softwareUpgradeAll").addEventListener("click", softwareUpgradeAll);
$("softwareAddBucket").addEventListener("click", softwareAddBucket);
$("softwareProviderSelect").addEventListener("change", event => changeSoftwareProvider(event.target.value));
$("softwareSearchInput").addEventListener("keydown", event => { if (event.key === "Enter") { event.preventDefault(); softwareSearch(); } });
$("softwareBucketSource").addEventListener("keydown", event => { if (event.key === "Enter") { event.preventDefault(); softwareAddBucket(); } });
$("newTerminal").addEventListener("click", newTerminal);
$("dockerVolumeAction").addEventListener("click", openDockerVolumeCreate);
$("dockerNetworkAction").addEventListener("click", openDockerNetworkCreate);
document.querySelectorAll("[data-dismiss]").forEach(button => button.addEventListener("click", () => {
  $(button.dataset.dismiss).close();
}));
$("primaryAction").addEventListener("click", () => {
	if (currentPage === "tasks") $("taskDialog").showModal();
	if (currentPage === "docker") openDockerCreate();
	if (currentPage === "remote") openRemoteTarget();
});

$("remoteBack").addEventListener("click", closeRemoteSession);
$("remoteStop").addEventListener("click", () => { if (remoteSessionID) stopRemoteSession(remoteSessionID); });
$("remoteFullscreen").addEventListener("click", async () => {
  try {
    const surface = remoteRDPInteraction ? $("remoteRDP") : $("remoteFrame");
    if (document.fullscreenElement) await document.exitFullscreen();
    else await surface.requestFullscreen();
    focusRemoteClient();
  } catch (error) {
    toast(`Fullscreen unavailable: ${error.message}`);
  }
});
document.addEventListener("fullscreenchange", () => { focusRemoteClient(); remoteRDPInteraction?.surfaceChanged?.(); });
$("remoteFrame").addEventListener("load", () => { if (remoteSessionID) { $("remoteLoading").classList.add("hidden"); focusRemoteClient(); } });
$("remoteFrame").addEventListener("pointerenter", focusRemoteClient);
$("remoteFrame").addEventListener("pointerdown", focusRemoteClient);
$("remoteXpraDPIMode").addEventListener("change", updateRemoteXpraSettings);
$("remoteXpraProfile").addEventListener("change", applyRemoteXpraProfile);
[$("remoteXpraEncoding"), $("remoteXpraVideo")].forEach(control => control.addEventListener("change", () => { $("remoteXpraProfile").value="custom"; }));
$("remoteTargetForm").addEventListener("submit", async event => {
  event.preventDefault(); const id=$("remoteTargetId").value, isRDP=$("remoteTargetProvider").value === "rdp"; const command=isRDP ? undefined : commandFromEditor("remote"); if (!isRDP && !command) return;
  const xpra=$("remoteTargetProvider").value === "xpra" ? {profile:$("remoteXpraProfile").value,encoding:$("remoteXpraEncoding").value,video:$("remoteXpraVideo").checked,dpiMode:$("remoteXpraDPIMode").value,dpi:Number($("remoteXpraDPI").value),launchAfterConnect:$("remoteXpraLaunchAfterConnect").checked,clipboard:$("remoteXpraClipboard").checked,dynamicResize:$("remoteXpraDynamicResize").checked,menu:$("remoteXpraMenu").value,toolbarPosition:$("remoteXpraToolbarPosition").value} : undefined;
  const rdpIdentity=splitRDPUsername($("remoteRDPUsername").value), rdp=isRDP ? {host:$("remoteRDPHost").value.trim(),port:Number($("remoteRDPPort").value),username:rdpIdentity.username,domain:rdpIdentity.domain,securityMode:$("remoteRDPSecurity").value,serverLayout:$("remoteRDPLayout").value,resizeMethod:$("remoteRDPResize").value,certificatePolicy:$("remoteRDPCertificate").value,timeoutSeconds:Number($("remoteRDPTimeout").value),clipboardNormalization:$("remoteRDPClipboard").value,performanceProfile:$("remoteRDPPerformance").value,copy:true,paste:true} : undefined;
  const body={name:$("remoteTargetName").value.trim(),provider:$("remoteTargetProvider").value,type:isRDP ? "desktop" : $("remoteTargetType").value,dbusMode:$("remoteDBusMode").value,command,xpra,rdp};
  try { await api(id ? `api/v1/remote/targets/${id}` : "api/v1/remote/targets",{method:id?"PUT":"POST",body:JSON.stringify(body)}); $("remoteTargetDialog").close(); await refresh(); } catch(error) { setCommandError("remote",error.message); }
});
$("remoteRDPConnectForm").addEventListener("submit", async event => { event.preventDefault(); const target=remotePendingTarget; if (!target) return; const identity=splitRDPUsername($("remoteRDPConnectUsername").value), credentials={username:identity.username,domain:identity.domain,password:$("remoteRDPConnectPassword").value}; remotePendingTarget=null; $("remoteRDPConnectDialog").close(); if (target.sessionID) await openRemoteSession(target.sessionID,credentials); else await beginRemoteSession(target.id,credentials); });
$("guacdSettingsForm").addEventListener("submit",async event=>{event.preventDefault();const value=guacdValue(),error=validateGuacdForm(value);setGuacdError(error);if(error)return;try{await api("api/v1/remote/guacd",{method:"PUT",body:JSON.stringify(value)});$("guacdSettingsDialog").close();toast("guacd settings saved.");await refresh()}catch(e){setGuacdError(e.message)}});
$("guacdTest").addEventListener("click",async()=>{const value=guacdValue(),error=validateGuacdForm(value);setGuacdError(error);setGuacdStatus();if(error)return;try{const status=await api("api/v1/remote/guacd/test",{method:"POST",body:JSON.stringify(value)});setGuacdStatus(status.state==="available" ? "Connected to guacd." : status.message||"Unable to connect to guacd.",status.state!=="available")}catch(e){setGuacdStatus(e.message,true)}});

document.querySelectorAll("[data-task-filter]").forEach(button => button.addEventListener("click", () => { taskFilter = button.dataset.taskFilter; document.querySelectorAll("[data-task-filter]").forEach(item => item.classList.toggle("active", item === button)); renderTasks(); }));
document.querySelectorAll("[data-task-kind]").forEach(button => button.addEventListener("click", () => { $("taskDialog").close(); if (button.dataset.taskKind === "continuous") openProcess(); else if (button.dataset.taskKind === "scheduled") openJob(); else openBackup(); }));
$("storageCreate").addEventListener("click",async()=>{if(!storageLocation)return;const name=prompt("Folder name:");if(!name)return;try{await api(`api/v1/storage/${storageLocation}/directories`,{method:"POST",body:JSON.stringify({parentPath:storagePath,name})});browseStorage(storageLocation,storagePath)}catch(e){toast(e.message)}});
$("storageNewFile").addEventListener("click",openNewTextFile);
$("storageUpload").addEventListener("change",async e=>{if(!storageLocation||!e.target.files.length)return;const form=new FormData();form.append("path",storagePath);for(const f of e.target.files)form.append("files",f);try{await api(`api/v1/storage/${storageLocation}/upload`,{method:"POST",body:form});browseStorage(storageLocation,storagePath)}catch(err){toast(err.message)}finally{e.target.value=""}});
$("storageProvider").addEventListener("change",e=>browseStorage(e.target.value,""));
$("storageShowHidden").addEventListener("change",e=>{storageShowHidden=e.target.checked;browseStorage(storageLocation,storagePath)});
$("saveTextEditor").addEventListener("click",saveTextEditor);$("closeTextEditor").addEventListener("click",()=>$("textEditorDialog").close());$("cancelTextEditor").addEventListener("click",()=>$("textEditorDialog").close());
$("textEditorContent").addEventListener("input",updateTextHighlight);
$("textEditorContent").addEventListener("scroll",e=>{ $("textHighlight").scrollTop=e.target.scrollTop; $("textHighlight").scrollLeft=e.target.scrollLeft; });
$("dockerFileContent").addEventListener("input",updateDockerFileHighlight);
$("dockerFileContent").addEventListener("scroll",e=>{ $("dockerFileHighlight").scrollTop=e.target.scrollTop; $("dockerFileHighlight").scrollLeft=e.target.scrollLeft; });

function openProcess(existing = null) {
  $("processForm").reset();
  $("processId").value = existing?.id || "";
  $("processName").value = existing?.name || "";
  populateCommandEditor("process", existing?.command);
  $("processRestart").value = existing?.restart?.mode || "on-failure";
  $("processAutostart").checked = !!existing?.autostart;
  $("processDialogTitle").textContent = existing ? "Edit continuous task" : "Add continuous task";
  $("processDialog").showModal();
}
function editProcess(id) { openProcess(processes.find(v => v.definition.id === id)?.definition); }

$("processForm").addEventListener("submit", async e => {
  e.preventDefault();
  const id = $("processId").value;
  const command = commandFromEditor("process");
  if (!command) return;
  const body = {
    name: $("processName").value.trim(),
    autostart: $("processAutostart").checked,
    command,
    restart: {
      mode: $("processRestart").value,
      initialDelaySeconds: 2,
      maxDelaySeconds: 60,
      maxRetries: 0,
    }
  };
  try {
    await api(id ? `api/v1/processes/${id}` : "api/v1/processes", {method: id ? "PUT" : "POST", body: JSON.stringify(body)});
    $("processDialog").close(); await refresh();
  } catch (err) { setCommandError("process", err.message); }
});

function setScheduleFields(prefix, type) {
  for (const key of ["Interval","Daily","Cron"]) {
    const el = $(`${prefix}${key}Wrap`);
    if (el) el.classList.toggle("hidden", key.toLowerCase() !== type);
  }
}
$("jobScheduleType").addEventListener("change", e => setScheduleFields("job", e.target.value));
$("backupScheduleType").addEventListener("change", e => setScheduleFields("backup", e.target.value));

function scheduleFrom(prefix) {
  const type = $(`${prefix}ScheduleType`).value;
  if (type === "interval") return {type, intervalSeconds: Number($(`${prefix}Interval`).value) * 60};
  if (type === "daily") return {type, timeOfDay: $(`${prefix}Daily`).value, timeZone: ""};
  return {type, cron: $(`${prefix}Cron`).value.trim(), timeZone: ""};
}
function fillSchedule(prefix, s = {}) {
  const type = s.type || (prefix === "backup" ? "daily" : "interval");
  $(`${prefix}ScheduleType`).value = type;
  if ($(`${prefix}Interval`)) $(`${prefix}Interval`).value = Math.max(1, Math.round((s.intervalSeconds || (prefix === "job" ? 1200 : 3600))/60));
  if ($(`${prefix}Daily`)) $(`${prefix}Daily`).value = s.timeOfDay || "03:00";
  if ($(`${prefix}Cron`)) $(`${prefix}Cron`).value = s.cron || "";
  setScheduleFields(prefix, type);
}

function openJob(existing = null) {
  $("jobForm").reset();
  $("jobId").value = existing?.id || "";
  $("jobName").value = existing?.name || "";
  populateCommandEditor("job", existing?.command);
  $("jobEnabled").checked = existing ? !!existing.enabled : true;
  fillSchedule("job", existing?.schedule || {});
  $("jobDialogTitle").textContent = existing ? "Edit scheduled task" : "Add scheduled task";
  $("jobDialog").showModal();
}
function editJob(id) { openJob(jobs.find(v => v.definition.id === id)?.definition); }

$("jobForm").addEventListener("submit", async e => {
  e.preventDefault();
  const id = $("jobId").value;
  const command = commandFromEditor("job");
  if (!command) return;
  const body = {
    name: $("jobName").value.trim(),
    enabled: $("jobEnabled").checked,
    type: "command",
    overlapPolicy: "skip",
    schedule: scheduleFrom("job"),
    command,
  };
  try {
    await api(id ? `api/v1/jobs/${id}` : "api/v1/jobs", {method: id ? "PUT" : "POST", body: JSON.stringify(body)});
    $("jobDialog").close(); await refresh();
  } catch (err) { setCommandError("job", err.message); }
});

function openBackup(existing = null) {
  $("backupForm").reset();
  const b = existing?.backup || {};
  $("backupId").value = existing?.id || "";
  $("backupName").value = existing?.name || "";
  const provider = b.engine || "robocopy", r = b.robocopy || b, restic = b.restic || {}, rdiff = b.rdiffBackup || {};
  $("backupProvider").value = provider;
  $("backupSource").value = r.source || ""; $("backupDestination").value = r.destination || ""; $("backupMode").value = r.mode || "copy"; $("backupRetries").value = r.retries ?? 2;
  $("resticRepository").value=restic.repository||""; $("resticSources").value=(restic.sources||[]).join("\n"); $("resticExcludes").value=(restic.excludes||[]).join("\n"); $("resticTags").value=(restic.tags||[]).join("\n"); $("resticVss").checked=!!restic.useVss; $("resticCheck").checked=!!restic.checkAfterBackup; $("resticPasswordFile").value=restic.passwordFile||""; $("resticExecutable").value=restic.executable||"";
  const rt=restic.retention||{}; $("resticRetentionEnabled").checked=!!restic.retention; $("resticKeepLast").value=rt.keepLast||""; $("resticKeepDaily").value=rt.keepDaily||""; $("resticKeepWeekly").value=rt.keepWeekly||""; $("resticKeepMonthly").value=rt.keepMonthly||""; $("resticKeepYearly").value=rt.keepYearly||""; $("resticKeepWithin").value=rt.keepWithin||""; $("resticPrune").checked=!!rt.prune;
  $("rdiffSource").value=rdiff.source||""; $("rdiffDestination").value=rdiff.destination||""; $("rdiffExcludes").value=(rdiff.excludes||[]).join("\n"); $("rdiffRetentionEnabled").checked=!!rdiff.retention; $("rdiffOlderThan").value=rdiff.retention?.olderThan||""; $("rdiffVerify").checked=!!rdiff.verifyAfterBackup; $("rdiffExecutable").value=rdiff.executable||"";
  $("backupEnabled").checked = existing ? !!existing.enabled : true;
  fillSchedule("backup", existing?.schedule || {type:"daily", timeOfDay:"03:00"});
  updateMirrorWarning();
  updateBackupProvider();
  $("backupDialogTitle").textContent = existing ? "Edit backup task" : "Add backup task";
  $("backupDialog").showModal();
}
function editBackup(id) { openBackup(jobs.find(v => v.definition.id === id)?.definition); }
function updateMirrorWarning() { $("mirrorWarning").classList.toggle("hidden", $("backupMode").value !== "mirror"); }
$("backupMode").addEventListener("change", updateMirrorWarning);
function lines(id) { return $(id).value.split("\n").map(v=>v.trim()).filter(Boolean); }
function updateBackupProvider() {
  const p=$("backupProvider").value, robo=p==="robocopy";
  ["backupMode","backupSource","backupDestination","backupRetries"].forEach(id=>$(id).closest("label").classList.toggle("hidden",!robo));
  $("resticFields").classList.toggle("hidden",p!=="restic"); $("rdiffFields").classList.toggle("hidden",p!=="rdiff-backup");
  $("mirrorWarning").classList.toggle("hidden",!robo || $("backupMode").value!=="mirror");
  $("resticRetentionFields").classList.toggle("hidden",!$("resticRetentionEnabled").checked); $("rdiffRetentionWrap").classList.toggle("hidden",!$("rdiffRetentionEnabled").checked);
}
$("backupProvider").addEventListener("change", updateBackupProvider); $("resticRetentionEnabled").addEventListener("change",updateBackupProvider); $("rdiffRetentionEnabled").addEventListener("change",updateBackupProvider);

$("backupForm").addEventListener("submit", async e => {
  e.preventDefault();
  const id = $("backupId").value;
  const body = {
    name: $("backupName").value.trim(),
    enabled: $("backupEnabled").checked,
    type: "backup",
    overlapPolicy: "skip",
    schedule: scheduleFrom("backup"),
    backup: { engine: $("backupProvider").value }
  };
  if (body.backup.engine === "robocopy") body.backup.robocopy={source:$("backupSource").value.trim(),destination:$("backupDestination").value.trim(),mode:$("backupMode").value,retries:Number($("backupRetries").value),retryWaitSeconds:5};
  if (body.backup.engine === "restic") { const retention=$("resticRetentionEnabled").checked?{keepLast:Number($("resticKeepLast").value)||0,keepDaily:Number($("resticKeepDaily").value)||0,keepWeekly:Number($("resticKeepWeekly").value)||0,keepMonthly:Number($("resticKeepMonthly").value)||0,keepYearly:Number($("resticKeepYearly").value)||0,keepWithin:$("resticKeepWithin").value.trim(),prune:$("resticPrune").checked}:null; body.backup.restic={repository:$("resticRepository").value.trim(),sources:lines("resticSources"),excludes:lines("resticExcludes"),tags:lines("resticTags"),useVss:$("resticVss").checked,checkAfterBackup:$("resticCheck").checked,passwordFile:$("resticPasswordFile").value.trim(),executable:$("resticExecutable").value.trim(),retention}; }
  if (body.backup.engine === "rdiff-backup") body.backup.rdiffBackup={source:$("rdiffSource").value.trim(),destination:$("rdiffDestination").value.trim(),excludes:lines("rdiffExcludes"),verifyAfterBackup:$("rdiffVerify").checked,executable:$("rdiffExecutable").value.trim(),retention:$("rdiffRetentionEnabled").checked?{olderThan:$("rdiffOlderThan").value.trim()}:null};
  try {
    await api(id ? `api/v1/jobs/${id}` : "api/v1/jobs", {method: id ? "PUT" : "POST", body: JSON.stringify(body)});
    $("backupDialog").close(); await refresh();
  } catch (err) { toast(err.message); }
});

async function openProcessLog(id, name) {
  openLog(name, async () => api(`api/v1/processes/${id}/log?lines=500`));
}
async function openRunLog(id, name) {
  openLog(name, async () => api(`api/v1/runs/${id}/log?lines=800`));
}
async function openLog(name, loader) {
  if (logTimer) clearInterval(logTimer);
  logSource = loader;
  $("logTitle").textContent = name;
  $("logSubtitle").textContent = "Latest captured stdout/stderr";
  $("logOutput").textContent = "Loading…";
  $("logDialog").showModal();
  const load = async () => {
    try { $("logOutput").textContent = await loader() || "(no output)"; }
    catch (e) { $("logOutput").textContent = e.message; }
  };
  await load();
  logTimer = setInterval(load, 2000);
}
$("closeLog").addEventListener("click", () => {
  if (logTimer) clearInterval(logTimer);
  logTimer = null; logSource = null; $("logDialog").close();
});

$("loginForm").addEventListener("submit", async e => {
  e.preventDefault();
  const candidate = $("tokenInput").value.trim();
  const old = token;
  token = candidate;
  try {
    await api("api/v1/system");
    localStorage.setItem("runpilot.token", token);
    $("loginError").classList.add("hidden");
    $("loginDialog").close();
    setConnected(true);
    await loadTerminalInfo();
    await refresh();
    startAutoRefresh();
    startConnectionMonitor();
  } catch {
    token = old;
    $("loginError").textContent = "The token was rejected.";
    $("loginError").classList.remove("hidden");
  }
});

(async function init() {
  initializeSidebar();
  initializeTheme();
  if (!token) {
    $("loginDialog").showModal();
    return;
  }
  setConnected(false);
  startConnectionMonitor();
  await refresh();
  const requestedSession = new URLSearchParams(location.search).get("remoteSession");
  if (requestedSession) { setPage("remote"); await openRemoteSession(requestedSession); }
  try { await loadTerminalInfo(); } catch (e) { if (e.message !== "Unauthorized") toast(e.message); }
  startAutoRefresh();
})();

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) { refresh(); probeConnection(); }
});
