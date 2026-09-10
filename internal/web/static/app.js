const $ = (id) => document.getElementById(id);
let token = localStorage.getItem("runpilot.token") || "";
let currentPage = "overview";
let processes = [];
let jobs = [];
let overview = null;
let logTimer = null;
let logSource = null;
let refreshTimer = null;
let refreshing = false;
const themeStorageKey = "runpilot.theme";

const pageMeta = {
  overview: ["Overview", "RunPilot service and resource health at a glance.", null],
  processes: ["Processes", "Long-running applications supervised by the RunPilot service.", "Add process"],
  jobs: ["Scheduled jobs", "One-shot commands launched on an interval, daily time or cron expression.", "Add job"],
  backups: ["Backups", "Scheduled filesystem backups powered by Windows built-in tools.", "Add backup"],
  history: ["History", "Recent process exits and job executions with exit code and captured output.", null],
};

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Authorization", `Bearer ${token}`);
  if (options.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
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
    const [p, j, h, o] = await Promise.all([
      api("api/v1/processes"),
      api("api/v1/jobs"),
      api("api/v1/runs?lines=100"),
      api("api/v1/overview")
    ]);
    processes = p;
    jobs = j;
    overview = o;
    renderOverview();
    renderProcesses();
    renderJobs();
    renderHistory(h);
    setConnected(true);
  } catch (e) {
    setConnected(false);
    if (e.message === "Unauthorized") $("loginDialog").showModal();
    else toast(e.message);
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
  refresh();
}

document.querySelectorAll(".nav").forEach(n => n.addEventListener("click", () => setPage(n.dataset.page)));
document.querySelectorAll("[data-dismiss]").forEach(button => button.addEventListener("click", () => {
  $(button.dataset.dismiss).close();
}));
$("primaryAction").addEventListener("click", () => {
  if (currentPage === "processes") openProcess();
  if (currentPage === "jobs") openJob();
  if (currentPage === "backups") openBackup();
});

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
