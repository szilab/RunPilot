const $ = (id) => document.getElementById(id);
let token = localStorage.getItem("runpilot.token") || "";
let currentPage = "overview";
let processes = [];
let jobs = [];
let storage = [], storageLocation = null, storagePath = "";
let softwareProviders = [], softwareProviderID = "", softwarePackages = [], softwareUpdates = [], softwareSearchResults = [], softwareBuckets = [], softwareTab = "installed", softwareBusy = false, softwareBusyLabel = "", softwareLoading = false, softwareLoadingKey = "", softwareLoadedKey = "", softwareLoadSequence = 0, softwareRootDrafts = {};
let storageClipboard = null, editingTextPath = null, storageShowHidden = false;
let overview = null;
let logTimer = null;
let logSource = null;
let refreshTimer = null;
let refreshing = false;
const themeStorageKey = "runpilot.theme";

const pageMeta = {
  overview: ["Overview", "RunPilot service and resource health at a glance.", null],
  processes: ["Processes", "Long-running applications supervised by the RunPilot service.", "Add process"],
  jobs: ["Scheduler", "One-shot commands launched on an interval, daily time or cron expression.", "Add job"],
  backups: ["Backups", "Scheduled filesystem backups powered by Windows built-in tools.", "Add backup"],
  storage: ["Storage", "Helyi fájlrendszer kezelése.", "Tároló hozzáadása"],
  software: ["Software", "Install and maintain portable applications in a RunPilot-managed Scoop root.", null],
  history: ["History", "Recent process exits and job executions with exit code and captured output.", null],
};

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Authorization", `Bearer ${token}`);
  if (options.body && !(options.body instanceof FormData) && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const res = await fetch(path, {...options, headers});
  if (res.status === 401) {
    setConnected(false);
    throw new Error("Unauthorized");
  }
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try { message = (await res.json()).error || message; } catch {}
    throw new Error(message);
  }
  if (res.status === 204) return null;
  const ct = res.headers.get("content-type") || "";
  return ct.includes("application/json") ? res.json() : res.text();
}

function setConnected(ok) {
  $("connectionDot").classList.toggle("ok", ok);
  $("connectionText").textContent = ok ? "Connected" : "Offline";
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

function toast(message) {
  const el = $("toast");
  el.textContent = message;
  el.classList.remove("hidden");
  setTimeout(() => el.classList.add("hidden"), 2600);
}

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
      setCommandError(prefix, `Environment variables "${names.get(canonicalName)}" and "${name}" conflict on Windows.`);
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
    const [p, j, h, o, st, sw] = await Promise.all([
      api("api/v1/processes"),
      api("api/v1/jobs"),
      api("api/v1/runs?lines=100"),
      api("api/v1/overview"), api("api/v1/storage"),
      currentPage === "software" ? api("api/v1/software/providers") : Promise.resolve(null)
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
    renderProcesses();
    renderJobs();
    renderHistory(h);
    renderStorage();
    if (currentPage === "software") renderSoftware();
    setConnected(true);
  } catch (e) {
    softwareLoading = false; softwareLoadingKey = "";
    setConnected(false);
    if (e.message === "Unauthorized") $("loginDialog").showModal();
    else toast(e.message);
    if (currentPage === "software") renderSoftware();
  } finally {
    refreshing = false;
  }
}

function renderOverview() {
  if (!overview) return;
  const host = overview.host || {};
  const memoryUsed = Math.max(0, (host.memoryTotalBytes || 0) - (host.memoryFreeBytes || 0));
  $("overviewMetrics").innerHTML = [
    utilizationMetric("CPU", host.cpuPercent, host.cpuAveragePercent, "blue"),
    `<div class="metric"><span>Memory</span><strong>${escapeHtml(`${fmtBytes(memoryUsed)} / ${fmtBytes(host.memoryTotalBytes)}`)}</strong>${meter(percent(memoryUsed, host.memoryTotalBytes), "violet")}</div>`,
    utilizationMetric("GPU", host.gpuPercent, host.gpuAveragePercent, "pink", host.gpuAvailable),
    ["Processes", `${overview.runningProcesses || 0} running / ${overview.processCount || 0}`, percent(overview.runningProcesses || 0, overview.processCount || 0), "green"],
    ["Jobs", `${overview.runningJobs || 0} running / ${overview.jobCount || 0}`, percent(overview.runningJobs || 0, overview.jobCount || 0), "amber"],
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
  </article>`).join("") : `<div class="empty compact"><h2>All clear</h2><p>No current process, job or host errors were reported.</p></div>`;
}

function startAutoRefresh() {
  if (refreshTimer) return;
  refreshTimer = setInterval(() => {
    if (!document.hidden) refresh();
  }, 5000);
}

function renderProcesses() {
  const grid = $("processGrid");
  grid.innerHTML = processes.map(v => {
    const d = v.definition, s = v.status;
    const running = ["running","starting","stopping"].includes(s.state);
    return `<article class="row">
      <div class="row-head">
        <div><h3>${escapeHtml(d.name)}</h3><div class="meta">${escapeHtml(d.command.path)}</div></div>
        ${statusBadge(s.state)}
      </div>
      <div class="row-details">
        <div class="kv"><span>PID</span><span>${s.pid || "—"}</span></div>
        <div class="kv"><span>Started</span><span>${fmtDate(s.startedAt)}</span></div>
        <div class="kv"><span>Restart</span><span>${escapeHtml(d.restart?.mode || "on-failure")}</span></div>
        <div class="kv"><span>Autostart</span><span>${d.autostart ? "Yes" : "No"}</span></div>
      </div>
      <div class="row-actions">
        ${running
          ? `<button class="button secondary small" onclick="processAction('${d.id}','stop')">Stop</button>
             <button class="button secondary small" onclick="processAction('${d.id}','restart')">Restart</button>`
          : `<button class="button primary small" onclick="processAction('${d.id}','start')">Start</button>`}
        <button class="button secondary small" onclick="openProcessLog('${d.id}','${escapeHtml(d.name)}')">Log</button>
        <button class="button secondary small" onclick="editProcess('${d.id}')">Edit</button>
        <button class="button danger small" onclick="deleteProcess('${d.id}')">Delete</button>
      </div>
    </article>`;
  }).join("");
  $("processEmpty").classList.toggle("hidden", processes.length > 0);
}

function renderJobs() {
  const commands = jobs.filter(v => v.definition.type === "command");
  const backups = jobs.filter(v => v.definition.type === "backup");
  $("jobGrid").innerHTML = commands.map(renderJobRow).join("");
  $("backupGrid").innerHTML = backups.map(renderBackupRow).join("");
  $("jobEmpty").classList.toggle("hidden", commands.length > 0);
  $("backupEmpty").classList.toggle("hidden", backups.length > 0);
}

function renderJobRow(v) {
  const d = v.definition, s = v.status;
  const state = s.running ? "running" : (s.lastSuccess === false ? "failure" : "idle");
  return `<article class="row">
    <div class="row-head"><div><h3>${escapeHtml(d.name)}</h3><div class="meta">${escapeHtml(d.command?.path || "")}</div></div>${statusBadge(state)}</div>
    <div class="row-details">
      <div class="kv"><span>Schedule</span><span>${escapeHtml(scheduleText(d.schedule))}</span></div>
      <div class="kv"><span>Enabled</span><span>${d.enabled ? "Yes" : "No"}</span></div>
      <div class="kv"><span>Last run</span><span>${fmtDate(s.lastRunAt)}</span></div>
      <div class="kv"><span>Last exit</span><span>${s.lastExitCode ?? "—"}</span></div>
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
  const state = s.running ? "running" : (s.lastSuccess === false ? "failure" : "idle");
  return `<article class="row">
    <div class="row-head"><div><h3>${escapeHtml(d.name)}</h3><div class="meta">${escapeHtml(b.source)} → ${escapeHtml(b.destination)}</div></div>${statusBadge(state)}</div>
    <div class="row-details">
      <div class="kv"><span>Mode</span><span>${escapeHtml(b.mode || "copy")}</span></div>
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

function renderHistory(runs) {
  $("historyBody").innerHTML = runs.map(r => `<tr>
    <td><strong>${escapeHtml(r.targetName)}</strong></td>
    <td>${escapeHtml(r.kind)}</td>
    <td>${fmtDate(r.startedAt)}</td>
    <td>${r.success == null ? "—" : statusBadge(r.success ? "success" : "failure")}</td>
    <td>${r.exitCode ?? "—"}</td>
    <td><button class="button secondary small" onclick="openRunLog('${r.id}','${escapeHtml(r.targetName)}')">Log</button></td>
  </tr>`).join("");
  $("historyEmpty").classList.toggle("hidden", runs.length > 0);
}

function renderStorage() {
  const local = storage.find(d => d.id === "storage-local") || storage[0];
  $("storageEmpty").classList.toggle("hidden", !!local);
  if (local && !storageLocation) browseStorage(local.id);
}
async function browseStorage(id, p = "") { try { const listing = await api(`api/v1/storage/${id}/entries?` + new URLSearchParams({path:p})); storageLocation=id; storagePath=listing.path; $("storageBrowserTitle").textContent=storage.find(x=>x.id===id)?.name || "Helyi fájlrendszer"; $("storageBreadcrumbs").textContent=listing.path || "Ez a gép"; $("storageUp").disabled=listing.parentPath == null; $("storageUp").onclick=()=>browseStorage(id,listing.parentPath || ""); const root=$("storageEntries"); root.replaceChildren(); listing.entries.forEach(e => { const row=document.createElement("article"); row.className="row storage-entry"; row.innerHTML=`<div class="row-head"><div><h3>${escapeHtml(e.name)}</h3><div class="meta">${escapeHtml(e.type)}</div></div></div><div class="row-details"><div class="kv"><span>Size</span><span>${fmtBytes(e.size)}</span></div><div class="kv"><span>Modified</span><span>${fmtDate(e.modifiedAt)}</span></div></div><div class="row-actions"></div>`; if(e.type!=="file") { row.classList.add("openable"); row.addEventListener("click",()=>browseStorage(id,e.path)); } const actions=row.querySelector(".row-actions"); const menu=document.createElement("select"); menu.className="storage-menu"; menu.innerHTML=`<option value="">További műveletek…</option>${e.type==="file"?'<option value="download">Letöltés</option>':''}${e.type!=="filesystem-root"?'<option value="copy">Másolás</option><option value="rename">Átnevezés</option><option value="move">Áthelyezés</option><option value="delete">Törlés</option>':''}`; menu.addEventListener("click",event=>event.stopPropagation()); menu.addEventListener("change",()=>{const action=menu.value;menu.value="";if(action==="download")downloadStorage(id,e.path);if(action==="copy")copyStorage(id,e.path);if(action==="rename")renameStorage(id,e.path);if(action==="move")moveStorage(id,e.path);if(action==="delete")deleteStorageObject(id,e.path,e.type)}); actions.append(menu); root.append(row) }); } catch(e) { toast(e.message); } }
async function downloadStorage(id,p){try{const t=await api(`api/v1/storage/${id}/download-ticket`,{method:"POST",body:JSON.stringify({path:p})});window.location.assign(t.url)}catch(e){toast(e.message)}}
async function deleteStorageObject(id,p,type){if(!confirm(`Delete ${type === "directory" ? "folder and all contents" : "file"}? This cannot be undone through RunPilot.`))return;try{await api(`api/v1/storage/${id}/delete`,{method:"POST",body:JSON.stringify({path:p})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}
async function renameStorage(id,p){const n=prompt("New name:");if(!n)return;try{await api(`api/v1/storage/${id}/rename`,{method:"POST",body:JSON.stringify({path:p,newName:n})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}
async function moveStorage(id,p){const d=prompt("Destination folder path (provider-relative):",storagePath);if(d===null)return;try{await api(`api/v1/storage/${id}/move`,{method:"POST",body:JSON.stringify({sourcePath:p,destinationDirectory:d})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}
async function copyStorage(id,p){const d=prompt("Célmappa útvonala:",storagePath);if(d===null)return;try{await api(`api/v1/storage/${id}/copy`,{method:"POST",body:JSON.stringify({sourcePath:p,destinationDirectory:d})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}

function renderStorage() {
  const select = $("storageProvider"), previous = storageLocation || select.value;
  select.replaceChildren();
  storage.forEach(d => { const option = document.createElement("option"); option.value = d.id; option.textContent = d.name; select.append(option); });
  const local = storage.find(d => d.id === "storage-local") || storage[0];
  $("storageEmpty").classList.toggle("hidden", !!local);
  if (local) { select.value = storage.some(d => d.id === previous) ? previous : local.id; if (!storageLocation) browseStorage(select.value, ""); }
}
function isEditableText(name) { return !/\.[^./\\]+$/.test(name) || /\.(txt|log|yaml|yml|json|ini|cfg|conf|toml|xml|csv|md|bat|cmd|ps1|go|js|html|css|ts|tsx|jsx|py|rb|java|c|h|cpp|cs|rs|sh|sql)$/i.test(name); }
function button(label, title, action, disabled = false) { const b=document.createElement("button"); b.className="row-icon"+(title==="Letöltés"?" download-icon":""); b.type="button"; b.textContent=label; b.title=title; b.disabled=disabled; b.addEventListener("click", event=>{event.stopPropagation();action()}); return b; }
function actionSlot(control = null) { if (control) return control; const slot=document.createElement("span");slot.className="row-icon-slot";slot.setAttribute("aria-hidden","true");return slot; }
function entryIcon(entry) { if (entry.type === "directory" || entry.type === "filesystem-root") return "folder"; const ext=(entry.name.split(".").pop()||"").toLowerCase(); if (["txt","log","yaml","yml","json","ini","cfg","conf","toml","xml","csv","md","go","js","ts","py","sh","sql"].includes(ext)||!/\.[^./\\]+$/.test(entry.name)) return "text"; if(["jpg","jpeg","png","gif","webp","svg"].includes(ext)) return "image"; if(["mp3","wav","flac","ogg"].includes(ext)) return "audio"; if(["mp4","mkv","avi","mov","webm"].includes(ext)) return "video"; return "file"; }
function renderBreadcrumbs(id, value) { const root=$("storageBreadcrumbs"); root.replaceChildren(); const home=document.createElement("button");home.className="breadcrumb-link";home.textContent="Ez a gép";home.addEventListener("click",()=>browseStorage(id,""));root.append(home); let built=""; for(const segment of value ? value.split("/") : []) { const sep=document.createElement("span");sep.textContent="/";root.append(sep);built=built?`${built}/${segment}`:segment;const link=document.createElement("button");link.className="breadcrumb-link";link.textContent=segment;const target=built;link.addEventListener("click",()=>browseStorage(id,target));root.append(link); } }
async function browseStorage(id, p = "") {
  try {
    const listing=await api(`api/v1/storage/${id}/entries?`+new URLSearchParams({path:p,showHidden:storageShowHidden})); storageLocation=id; storagePath=listing.path;
    $("storageProvider").value=id; $("storageBrowserTitle").textContent=storage.find(x=>x.id===id)?.name || "Tároló"; renderBreadcrumbs(id,listing.path);
    $("storageUp").disabled=listing.parentPath==null; $("storageUp").onclick=()=>browseStorage(id,listing.parentPath||"");
    const root=$("storageEntries");root.replaceChildren(); const header=document.createElement("div");header.className="storage-list-head";header.innerHTML="<span>Name</span><span>Size</span><span>Modified</span><span>Actions</span>";root.append(header);
    listing.entries.forEach(entry=>{
      const row=document.createElement("article");row.className="row storage-entry storage-row";
      const head=document.createElement("div");head.className="storage-name";const icon=document.createElement("span");icon.className=`entry-icon ${entryIcon(entry)}`;icon.setAttribute("aria-hidden","true");const title=document.createElement("h3");title.textContent=entry.name;head.append(icon);
      head.append(title);const meta=document.createElement("div");meta.className="meta";meta.textContent=entry.type;head.append(meta);
      const details=document.createElement("div");details.className="row-details";details.innerHTML=`<div class="kv"><span>Méret</span><span>${fmtBytes(entry.size)}</span></div><div class="kv"><span>Módosítva</span><span>${fmtDate(entry.modifiedAt)}</span></div>`;
      const actions=document.createElement("div");actions.className="row-actions";
      const mutable=entry.type!=="filesystem-root", editable=entry.type==="file" && isEditableText(entry.name);
      actions.append(actionSlot(entry.type==="file" ? button("↓","Letöltés",()=>downloadStorage(id,entry.path)) : null));
      actions.append(actionSlot(editable ? button("✎","Szerkesztés",()=>openTextEditor(id,entry.path,entry.name)) : null));
      actions.append(actionSlot(mutable ? button("↺","Átnevezés",()=>renameStorage(id,entry.path)) : null));
      actions.append(actionSlot(mutable ? button("⧉","Másolás",()=>setStorageClipboard("copy",entry)) : null));
      actions.append(actionSlot(mutable ? button("✂","Kivágás",()=>setStorageClipboard("move",entry)) : null));
      actions.append(button("📌","Beillesztés",()=>pasteStorage(id),!storageClipboard));
      actions.append(actionSlot(mutable ? button("🗑","Törlés",()=>deleteStorageObject(id,entry.path,entry.type)) : null));
      const size=document.createElement("div");size.className="storage-cell";size.textContent=entry.type==="file"?fmtBytes(entry.size):"—";const modified=document.createElement("div");modified.className="storage-cell";modified.textContent=fmtDate(entry.modifiedAt);row.append(head,size,modified,actions);
      if(entry.type!=="file") { row.classList.add("openable");row.addEventListener("click",()=>browseStorage(id,entry.path)); }
      else { row.classList.add("openable");row.addEventListener("click",()=>isEditableText(entry.name)?openTextEditor(id,entry.path,entry.name):downloadStorage(id,entry.path)); }
      root.append(row);
    });
  } catch(e) { toast(e.message); }
}
function setStorageClipboard(operation, entry) { storageClipboard={operation,path:entry.path,name:entry.name}; toast(operation==="copy" ? `Másolásra kijelölve: ${entry.name}` : `Áthelyezésre kijelölve: ${entry.name}`); browseStorage(storageLocation,storagePath); }
async function pasteStorage(id) { if(!storageClipboard)return; try { const endpoint=storageClipboard.operation==="copy"?"copy":"move"; await api(`api/v1/storage/${id}/${endpoint}`,{method:"POST",body:JSON.stringify({sourcePath:storageClipboard.path,destinationDirectory:storagePath})}); const message=storageClipboard.operation==="copy"?"Másolva":"Áthelyezve"; if(storageClipboard.operation==="move")storageClipboard=null;toast(message);browseStorage(id,storagePath); }catch(e){toast(e.message)} }
function syntaxSpan(kind, value) { return `<span class="syntax-${kind}">${escapeHtml(value)}</span>`; }
function highlightText(content, name) {
  const ext=(name.split(".").pop()||"").toLowerCase(); const structured=["json","yaml","yml","toml","ini","xml","html","css"].includes(ext);
  return content.split("\n").map(line=>{
    if (/^\s*(#|\/\/)/.test(line)) return syntaxSpan("comment",line);
    const chunks=line.split(/("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')/g);
    return chunks.map((part,index)=>{
      if (index%2) return syntaxSpan("string",part);
      let safe=escapeHtml(part);
      if (structured) safe=safe.replace(/\b(true|false|null|yes|no)\b/gi,'<span class="syntax-keyword">$1</span>').replace(/\b(-?\d+(?:\.\d+)?)\b/g,'<span class="syntax-number">$1</span>');
      else safe=safe.replace(/\b(func|function|return|if|else|for|while|package|import|const|var|let|class|public|private|def|true|false|null|nil)\b/g,'<span class="syntax-keyword">$1</span>');
      return safe;
    }).join("");
  }).join("\n")+"\n";
}
function updateTextHighlight() { const code=$("textHighlight").querySelector("code"); code.innerHTML=highlightText($("textEditorContent").value,editingTextPath?.path||""); }
async function openTextEditor(id,path,name) { try { const data=await api(`api/v1/storage/${id}/text?`+new URLSearchParams({path})); editingTextPath={id,path};$("textEditorTitle").textContent=`Szerkesztés: ${name}`;$("textEditorPath").textContent=path;$("textEditorContent").value=data.content;updateTextHighlight();$("textEditorDialog").showModal(); }catch(e){toast(e.message)} }
async function saveTextEditor() { if(!editingTextPath)return;try{await api(`api/v1/storage/${editingTextPath.id}/text`,{method:"PUT",body:JSON.stringify({path:editingTextPath.path,content:$("textEditorContent").value})});$("textEditorDialog").close();toast("Fájl mentve");browseStorage(storageLocation,storagePath)}catch(e){toast(e.message)} }

async function processAction(id, action) {
  try { await api(`api/v1/processes/${id}/${action}`, {method:"POST"}); toast(`Process ${action} requested`); setTimeout(refresh, 250); }
  catch (e) { toast(e.message); }
}

async function runJob(id) {
  try { await api(`api/v1/jobs/${id}/run`, {method:"POST"}); toast("Job started"); setTimeout(refresh, 250); }
  catch (e) { toast(e.message); }
}

async function deleteProcess(id) {
  if (!confirm("Delete this managed process?")) return;
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
  if (!provider) { card.innerHTML = `<div class="empty compact"><h2>Software provider unavailable</h2><p>RunPilot has no configured Software provider.</p></div>`; return; }
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

function setPage(page) {
  currentPage = page;
  document.querySelectorAll(".nav").forEach(n => n.classList.toggle("active", n.dataset.page === page));
  document.querySelectorAll(".page").forEach(p => p.classList.remove("active"));
  $(`${page}Page`).classList.add("active");
  const [title, sub, action] = pageMeta[page];
  $("pageTitle").textContent = title;
  $("pageSubtitle").textContent = sub;
  $("primaryAction").textContent = action || "";
  $("primaryAction").classList.toggle("hidden", !action);
	if (page === "software" && !softwareProviders.length) {
		$("softwareProviderCard").innerHTML = `<div class="empty compact"><h2>Preparing RunPilot Software Management…</h2><p>Initializing the managed Scoop runtime for this RunPilot data directory.</p></div>`;
		$("softwarePackages").innerHTML = `<div class="software-loading" role="status"><span class="spinner" aria-hidden="true"></span><strong>Loading applications…</strong><span>Querying the selected provider.</span></div>`;
	}
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
document.querySelectorAll("[data-dismiss]").forEach(button => button.addEventListener("click", () => {
  $(button.dataset.dismiss).close();
}));
$("primaryAction").addEventListener("click", () => {
  if (currentPage === "processes") openProcess();
  if (currentPage === "jobs") openJob();
  if (currentPage === "backups") openBackup();
  if (currentPage === "storage") $("storageDialog").showModal();
});
$("storageCreate").addEventListener("click",async()=>{if(!storageLocation)return;const name=prompt("Folder name:");if(!name)return;try{await api(`api/v1/storage/${storageLocation}/directories`,{method:"POST",body:JSON.stringify({parentPath:storagePath,name})});browseStorage(storageLocation,storagePath)}catch(e){toast(e.message)}});
$("storageUpload").addEventListener("change",async e=>{if(!storageLocation||!e.target.files.length)return;const form=new FormData();form.append("path",storagePath);for(const f of e.target.files)form.append("files",f);try{await api(`api/v1/storage/${storageLocation}/upload`,{method:"POST",body:form});browseStorage(storageLocation,storagePath)}catch(err){toast(err.message)}finally{e.target.value=""}});
$("storageProvider").addEventListener("change",e=>browseStorage(e.target.value,""));
$("storageShowHidden").addEventListener("change",e=>{storageShowHidden=e.target.checked;browseStorage(storageLocation,storagePath)});
$("storageScope").addEventListener("change",()=>$("storageRootWrap").classList.toggle("hidden",$("storageScope").value==="host"));
$("storageForm").addEventListener("submit",async e=>{e.preventDefault();const scope=$("storageScope").value;try{await api("api/v1/storage",{method:"POST",body:JSON.stringify({name:$("storageName").value.trim(),type:$("storageType").value,local:{scope,root:scope==="root"?$("storageRoot").value.trim():""}})});$("storageDialog").close();await refresh()}catch(err){toast(err.message)}});
$("saveTextEditor").addEventListener("click",saveTextEditor);$("closeTextEditor").addEventListener("click",()=>$("textEditorDialog").close());$("cancelTextEditor").addEventListener("click",()=>$("textEditorDialog").close());
$("textEditorContent").addEventListener("input",updateTextHighlight);
$("textEditorContent").addEventListener("scroll",e=>{ $("textHighlight").scrollTop=e.target.scrollTop; $("textHighlight").scrollLeft=e.target.scrollLeft; });

function openProcess(existing = null) {
  $("processForm").reset();
  $("processId").value = existing?.id || "";
  $("processName").value = existing?.name || "";
  populateCommandEditor("process", existing?.command);
  $("processRestart").value = existing?.restart?.mode || "on-failure";
  $("processAutostart").checked = !!existing?.autostart;
  $("processDialogTitle").textContent = existing ? "Edit process" : "Add process";
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
  $("jobDialogTitle").textContent = existing ? "Edit scheduled job" : "Add scheduled job";
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
  $("backupSource").value = b.source || "";
  $("backupDestination").value = b.destination || "";
  $("backupMode").value = b.mode || "copy";
  $("backupRetries").value = b.retries ?? 2;
  $("backupEnabled").checked = existing ? !!existing.enabled : true;
  fillSchedule("backup", existing?.schedule || {type:"daily", timeOfDay:"03:00"});
  updateMirrorWarning();
  $("backupDialogTitle").textContent = existing ? "Edit backup" : "Add backup";
  $("backupDialog").showModal();
}
function editBackup(id) { openBackup(jobs.find(v => v.definition.id === id)?.definition); }
function updateMirrorWarning() { $("mirrorWarning").classList.toggle("hidden", $("backupMode").value !== "mirror"); }
$("backupMode").addEventListener("change", updateMirrorWarning);

$("backupForm").addEventListener("submit", async e => {
  e.preventDefault();
  const id = $("backupId").value;
  const body = {
    name: $("backupName").value.trim(),
    enabled: $("backupEnabled").checked,
    type: "backup",
    overlapPolicy: "skip",
    schedule: scheduleFrom("backup"),
    backup: {
      engine: "robocopy",
      source: $("backupSource").value.trim(),
      destination: $("backupDestination").value.trim(),
      mode: $("backupMode").value,
      retries: Number($("backupRetries").value),
      retryWaitSeconds: 5,
    }
  };
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
    await refresh();
    startAutoRefresh();
  } catch {
    token = old;
    $("loginError").textContent = "The token was rejected.";
    $("loginError").classList.remove("hidden");
  }
});

(async function init() {
  initializeTheme();
  if (!token) {
    $("loginDialog").showModal();
    return;
  }
  await refresh();
  startAutoRefresh();
})();

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refresh();
});
