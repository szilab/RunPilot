// Tasks plugin frontend. It uses only the public plugin API
// (runpilot.ws / navigation / ui) and never touches core internals.

const PLUGIN = "tasks";
const INTERPRETERS = [
  ["auto", "Auto detect"], ["direct", "Direct executable"], ["powershell", "PowerShell"], ["cmd", "CMD / batch"],
  ["python", "Python"], ["sh", "POSIX shell (sh)"], ["sh-inline", "Inline shell command"], ["bash", "Bash"],
];
const OUTPUT_BYTES = 128 * 1024;
const UNIT_SECONDS = { seconds: 1, minutes: 60, hours: 3600 };

// ---- pure helpers (exported for tests) ---------------------------------

export function splitArgs(text) {
  const out = [];
  let cur = "", quote = null, escape = false, started = false;
  for (const ch of String(text || "").trim()) {
    if (escape) { cur += ch; escape = false; started = true; continue; }
    if (ch === "\\") { escape = true; continue; }
    if (quote) {
      if (ch === quote) quote = null; else cur += ch;
    } else if (ch === '"' || ch === "'") { quote = ch; started = true; }
    else if (/\s/.test(ch)) { if (started) { out.push(cur); cur = ""; started = false; } }
    else { cur += ch; started = true; }
  }
  if (started) out.push(cur);
  return out;
}

export function formatArgs(args = []) {
  return args.map(a => (a === "" || /[\s"'\\]/.test(a)) ? `"${a.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"` : a).join(" ");
}

export function scheduleText(s) {
  if (!s) return "—";
  if (s.type === "interval") {
    const n = s.intervalSeconds || 0;
    if (n % 3600 === 0 && n >= 3600) return `Every ${n / 3600} h`;
    if (n % 60 === 0 && n >= 60) return `Every ${n / 60} min`;
    return `Every ${n} s`;
  }
  const zone = s.timeZone ? ` (${s.timeZone})` : "";
  if (s.type === "daily") return `Daily ${s.timeOfDay}${zone}`;
  return `Cron ${s.cron}${zone}`;
}

export function splitInterval(seconds) {
  if (seconds >= 3600 && seconds % 3600 === 0) return [seconds / 3600, "hours"];
  if (seconds >= 60 && seconds % 60 === 0) return [seconds / 60, "minutes"];
  return [seconds, "seconds"];
}

const wholeNumber = (value, label, { min = 0 } = {}) => {
  if (value === "" || value === null || value === undefined) return 0;
  const n = Number(value);
  if (!Number.isInteger(n) || n < min) throw new Error(`${label} must be a whole number of at least ${min}`);
  return n;
};

// taskFromForm converts plain form values into the task the backend
// stores. Semantic validation stays in the backend; this only parses.
export function taskFromForm(v) {
  const environment = {};
  for (const [name, value] of v.env || []) {
    const key = String(name).trim();
    if (!key && !value) continue;
    environment[key] = value;
  }
  const task = {
    name: v.name, description: v.description || "", type: v.type,
    command: {
      path: v.path, args: splitArgs(v.args), workingDirectory: v.cwd || "",
      environment, interpreter: v.interpreter === "auto" ? "" : v.interpreter,
    },
  };
  if (v.id) task.id = v.id;
  if (v.type === "continuous") {
    task.autostart = !!v.autostart;
    task.restart = {
      mode: v.restartMode,
      initialDelaySeconds: wholeNumber(v.restartInitial, "Initial delay"),
      maxDelaySeconds: wholeNumber(v.restartMax, "Maximum delay"),
      maxRetries: wholeNumber(v.restartRetries, "Maximum retries"),
    };
  } else {
    task.enabled = !!v.enabled;
    task.overlapPolicy = v.overlap;
    task.timeoutSeconds = wholeNumber(v.timeout, "Timeout");
    const schedule = { type: v.scheduleType };
    if (v.scheduleType === "interval") {
      schedule.intervalSeconds = wholeNumber(v.intervalValue, "Interval", { min: 1 }) * (UNIT_SECONDS[v.intervalUnit] || 1);
    } else if (v.scheduleType === "daily") schedule.timeOfDay = v.time;
    else schedule.cron = v.cron;
    if (v.timeZone) schedule.timeZone = v.timeZone;
    task.schedule = schedule;
  }
  return task;
}

// ---- activation ----------------------------------------------------------

export function activate(runpilot) {
  const { ui } = runpilot;
  const esc = ui.escape;
  const state = { tasks: [], loadError: "", error: "", filter: "all", loaded: false, busy: new Set() };
  let page = null;
  const wired = new WeakSet();

  // Plugin-level failures arrive as {error:{code,message}} results.
  const call = async (method, params = {}) => {
    const result = await runpilot.ws.call(PLUGIN, method, params);
    if (result && result.error) {
      const error = new Error(result.error.message || "Task request failed");
      error.code = result.error.code;
      throw error;
    }
    return result;
  };
  const fail = error => ui.toast(error.message || String(error), "error");
  const fmtDate = value => value ? new Date(value).toLocaleString() : "—";
  const findTask = id => state.tasks.find(view => view.task.id === id);

  // ---- list page

  function statusBadge(view) {
    const current = view.status.state || "idle";
    const cls = current === "restarting" ? "starting" : current;
    return `<span class="status ${esc(cls)}">${esc(current)}</span>`;
  }

  function row(view) {
    const { task, status } = view;
    const id = esc(task.id), busy = state.busy.has(task.id);
    const kv = (label, value) => `<div class="kv"><span>${esc(label)}</span><span>${esc(value)}</span></div>`;
    let details, actions;
    if (task.type === "continuous") {
      const active = ["running", "stopping"].includes(status.state);
      const stopping = status.state === "stopping";
      details = [kv("Started", active ? fmtDate(status.startedAt) : "—"), kv("PID", status.pid || "—"),
        kv("Restart", task.restart?.mode || "on-failure"), kv("Autostart", task.autostart ? "Yes" : "No")];
      if (status.restartCount) details.push(kv("Restarts", status.restartCount));
      actions = active
        ? `<button class="button secondary small" data-action="stop" data-id="${id}" ${busy || stopping ? "disabled" : ""}>Stop</button><button class="button secondary small" data-action="restart" data-id="${id}" ${busy || stopping ? "disabled" : ""}>Restart</button>`
        : `<button class="button primary small" data-action="start" data-id="${id}" ${busy ? "disabled" : ""}>${status.state === "restarting" ? "Start now" : "Start"}</button>${status.state === "restarting" ? `<button class="button secondary small" data-action="stop" data-id="${id}" ${busy ? "disabled" : ""}>Cancel restart</button>` : ""}`;
    } else {
      details = [kv("Schedule", scheduleText(task.schedule)), kv("Schedule enabled", task.enabled ? "Yes" : "No"),
        kv("Overlap", task.overlapPolicy || "skip"), kv("Timeout", task.timeoutSeconds ? `${task.timeoutSeconds} s` : "None"),
        kv("Last run", fmtDate(status.lastRunAt)), kv("Last result", status.lastSuccess == null ? "—" : status.lastSuccess ? "Success" : "Failed")];
      if (status.runningCount > 1) details.push(kv("Running now", status.runningCount));
      actions = `<button class="button primary small" data-action="run" data-id="${id}" ${busy ? "disabled" : ""}>Run now</button>${status.state === "running" ? `<button class="button secondary small" data-action="stop" data-id="${id}" ${busy ? "disabled" : ""}>Stop</button>` : ""}`;
    }
    const notes = [status.error ? `<div class="form-error" role="alert">${esc(status.error)}</div>` : "",
      status.message && status.state !== "running" ? `<div class="meta">${esc(status.message)}</div>` : ""].join("");
    return `<article class="row" data-task="${id}">
      <div class="row-head"><div><h3>${esc(task.name)}</h3><div class="meta">${task.type === "continuous" ? "Continuous" : "Scheduled"} · ${esc(task.command.path)}</div></div>${statusBadge(view)}</div>
      ${task.description ? `<div class="meta">${esc(task.description)}</div>` : ""}
      <div class="row-details">${details.join("")}</div>${notes}
      <div class="row-actions">${actions}<button class="button secondary small" data-action="history" data-id="${id}">History</button><button class="button secondary small" data-action="edit" data-id="${id}" ${busy ? "disabled" : ""}>Edit</button><button class="button danger small" data-action="delete" data-id="${id}" ${busy ? "disabled" : ""}>Delete</button></div>
    </article>`;
  }

  function render() {
    if (!page || !page.isConnected) return;
    const visible = state.tasks.filter(view => state.filter === "all" || view.task.type === state.filter);
    const filters = [["all", "All"], ["continuous", "Continuous"], ["scheduled", "Scheduled"]]
      .map(([key, label]) => `<button class="button secondary small ${state.filter === key ? "active" : ""}" data-action="filter" data-filter="${key}">${label}</button>`).join("");
    const readOnly = !!state.loadError;
    page.innerHTML = `<div class="tasks-plugin">
      <div class="section-head tasks-plugin-toolbar"><div class="task-filters" role="group" aria-label="Task type">${filters}</div>
        <div class="row-actions"><button class="button primary small" data-action="add" data-type="continuous" ${readOnly ? "disabled" : ""}>Add continuous</button><button class="button primary small" data-action="add" data-type="scheduled" ${readOnly ? "disabled" : ""}>Add scheduled</button></div></div>
      ${state.loadError ? `<div class="notice" role="alert"><strong>Task configuration is unavailable</strong><span>${esc(state.loadError)}</span><span>The stored configuration was left untouched. Repair or remove plugins/${PLUGIN}/data/storage.json and restart RunPilot.</span></div>` : ""}
      ${state.error ? `<div class="notice" role="alert"><span>${esc(state.error)}</span></div>` : ""}
      <div class="row-list">${visible.map(row).join("")}</div>
      ${state.loaded && !visible.length && !state.loadError ? `<div class="empty"><h2>No tasks yet</h2><p>Add a continuous command or a scheduled command.</p></div>` : ""}
    </div>`;
    if (!wired.has(page)) {
      wired.add(page);
      page.addEventListener("click", onPageClick);
    }
  }

  async function load() {
    try {
      const result = await call("tasks.list");
      state.tasks = result.tasks || [];
      state.loadError = result.loadError || "";
      state.error = "";
    } catch (error) {
      state.error = error.message;
    }
    state.loaded = true;
    render();
  }

  async function act(id, method) {
    state.busy.add(id);
    render();
    try {
      const result = await call(method, { id });
      const view = result.task?.task ? result.task : result;
      if (method === "tasks.delete") state.tasks = state.tasks.filter(v => v.task.id !== id);
      else if (view?.task?.id) upsert(view);
    } catch (error) { fail(error); }
    state.busy.delete(id);
    render();
  }

  function upsert(view) {
    const index = state.tasks.findIndex(v => v.task.id === view.task.id);
    if (index >= 0) state.tasks[index] = view; else state.tasks.push(view);
    state.tasks.sort((a, b) => a.task.name.toLowerCase().localeCompare(b.task.name.toLowerCase()));
  }

  function onPageClick(event) {
    const button = event.target.closest("[data-action]");
    if (!button || button.disabled) return;
    const { action, id, filter, type } = button.dataset;
    if (action === "filter") { state.filter = filter; render(); }
    else if (action === "add") openEditor(null, type);
    else if (action === "edit") openEditor(findTask(id));
    else if (action === "history") openHistory(findTask(id));
    else if (action === "delete") {
      const view = findTask(id);
      if (view && confirm(`Delete task "${view.task.name}"? Its run history is kept.`)) act(id, "tasks.delete");
    } else act(id, `tasks.${action}`);
  }

  // ---- editor dialog

  let editor = null;
  const field = name => editor.querySelector(`[name="${name}"]`);
  const optionList = (items, selected) => items.map(([value, label]) => `<option value="${esc(value)}" ${value === selected ? "selected" : ""}>${esc(label)}</option>`).join("");

  function ensureEditor() {
    if (editor) return editor;
    editor = document.createElement("dialog");
    editor.className = "dialog tasks-plugin-dialog";
    document.body.append(editor);
    editor.addEventListener("click", event => {
      const target = event.target.closest("[data-editor]");
      if (!target) return;
      if (target.dataset.editor === "close") editor.close();
      else if (target.dataset.editor === "add-env") addEnvRow();
      else if (target.dataset.editor === "remove-env") target.closest(".environment-row").remove();
    });
    editor.addEventListener("change", event => { if (event.target.name === "scheduleType") syncSchedule(); });
    editor.addEventListener("submit", async event => {
      event.preventDefault();
      const error = editor.querySelector("[data-editor-error]");
      error.classList.add("hidden");
      try {
        const task = taskFromForm(readForm());
        const view = await call(task.id ? "tasks.update" : "tasks.create", { task });
        upsert(view);
        editor.close();
        render();
      } catch (err) {
        error.textContent = err.message;
        error.classList.remove("hidden");
      }
    });
    return editor;
  }

  function addEnvRow(name = "", value = "") {
    const envRow = document.createElement("div");
    envRow.className = "environment-row";
    envRow.innerHTML = `<input class="environment-name" aria-label="Variable name" placeholder="NAME"><input class="environment-value" aria-label="Variable value" placeholder="Value"><button class="button secondary small" type="button" data-editor="remove-env" aria-label="Remove variable">Remove</button>`;
    envRow.querySelector(".environment-name").value = name;
    envRow.querySelector(".environment-value").value = value;
    editor.querySelector("[data-env-rows]").append(envRow);
  }

  function syncSchedule() {
    const type = field("scheduleType")?.value;
    for (const key of ["interval", "daily", "cron"]) {
      editor.querySelector(`[data-schedule="${key}"]`)?.classList.toggle("hidden", type !== key);
    }
  }

  function readForm() {
    const value = name => field(name)?.value ?? "";
    const checked = name => !!field(name)?.checked;
    return {
      id: value("id"), type: value("type"), name: value("name"), description: value("description"),
      interpreter: value("interpreter"), path: value("path"), args: value("args"), cwd: value("cwd"),
      env: [...editor.querySelectorAll(".environment-row")].map(r => [r.querySelector(".environment-name").value, r.querySelector(".environment-value").value]),
      autostart: checked("autostart"), restartMode: value("restartMode"), restartInitial: value("restartInitial"),
      restartMax: value("restartMax"), restartRetries: value("restartRetries"),
      enabled: checked("enabled"), overlap: value("overlap"), timeout: value("timeout"),
      scheduleType: value("scheduleType"), intervalValue: value("intervalValue"), intervalUnit: value("intervalUnit"),
      time: value("time"), cron: value("cron"), timeZone: value("timeZone"),
    };
  }

  function openEditor(view, addType) {
    const dialog = ensureEditor();
    const task = view?.task;
    const type = task?.type || addType;
    const command = task?.command || {};
    const [amount, unit] = splitInterval(task?.schedule?.intervalSeconds || 1200);
    const schedule = task?.schedule || { type: "interval" };
    const restart = task?.restart || { mode: "on-failure", initialDelaySeconds: 2, maxDelaySeconds: 60, maxRetries: 0 };
    const continuousFields = `
      <label class="check span-2"><input name="autostart" type="checkbox" ${task?.autostart ? "checked" : ""}> Start automatically with RunPilot</label>`;
    const continuousAdvancedFields = `
      <label>Restart policy<select name="restartMode">${optionList([["on-failure", "On failure"], ["always", "Always"], ["never", "Never"]], restart.mode)}</select></label>
      <label>Initial restart delay (s)<input name="restartInitial" type="number" min="1" value="${esc(restart.initialDelaySeconds)}"></label>
      <label>Maximum restart delay (s)<input name="restartMax" type="number" min="1" value="${esc(restart.maxDelaySeconds)}"></label>
      <label>Maximum retries (0 = unlimited)<input name="restartRetries" type="number" min="0" value="${esc(restart.maxRetries)}"></label>`;
    const scheduledFields = `
      <label>Schedule<select name="scheduleType">${optionList([["interval", "Interval"], ["daily", "Daily"], ["cron", "Cron"]], schedule.type)}</select></label>
      <label data-schedule="interval">Every<span class="tasks-plugin-inline"><input name="intervalValue" type="number" min="1" value="${esc(amount)}"><select name="intervalUnit" aria-label="Interval unit">${optionList([["seconds", "seconds"], ["minutes", "minutes"], ["hours", "hours"]], unit)}</select></span></label>
      <label data-schedule="daily" class="hidden">Time<input name="time" type="time" value="${esc(schedule.timeOfDay || "03:00")}"></label>
      <label data-schedule="cron" class="hidden">Cron (5 or 6 fields)<input name="cron" placeholder="0 */20 * * * *" value="${esc(schedule.cron || "")}"></label>
      <label class="check span-2"><input name="enabled" type="checkbox" ${!task || task.enabled ? "checked" : ""}> Schedule enabled</label>`;
    const scheduledAdvancedFields = `
      <label>Time zone (optional)<input name="timeZone" placeholder="Europe/Budapest" value="${esc(schedule.timeZone || "")}"></label>
      <label>If still running<select name="overlap">${optionList([["skip", "Skip the new run"], ["allow", "Run in parallel"]], task?.overlapPolicy || "skip")}</select></label>
      <label>Timeout (s, 0 = none)<input name="timeout" type="number" min="0" value="${esc(task?.timeoutSeconds || 0)}"></label>`;
    dialog.innerHTML = `<form method="dialog">
      <div class="dialog-head"><div><h2>${task ? "Edit" : "Add"} ${type === "continuous" ? "continuous" : "scheduled"} task</h2><p>${type === "continuous" ? "Run continuously and supervise its lifecycle." : "Run a command according to a schedule."}</p></div><button class="icon-btn" type="button" data-editor="close" aria-label="Close">×</button></div>
      <input type="hidden" name="id" value="${esc(task?.id || "")}"><input type="hidden" name="type" value="${esc(type)}">
      <div class="form-grid">
        <label class="span-2">Name<input name="name" required maxlength="200" value="${esc(task?.name || "")}"></label>
        <label class="span-2">Executable, script, or inline command<input name="path" required placeholder="/opt/runpilot/myapp, C:\\Tools\\task.ps1, or ls -la /" value="${esc(command.path || "")}"></label>
        <label class="span-2">Arguments<input name="args" placeholder="--port 8080 --mode service" value="${esc(formatArgs(command.args))}"></label>
        ${type === "continuous" ? continuousFields : scheduledFields}
        <details class="tasks-plugin-advanced span-2">
          <summary>Advanced parameters</summary>
          <div class="form-grid tasks-plugin-advanced-fields">
            <label class="span-2">Description<input name="description" maxlength="2000" placeholder="Optional" value="${esc(task?.description || "")}"></label>
            <label>Interpreter<select name="interpreter">${optionList(INTERPRETERS, command.interpreter || "auto")}</select></label>
            <label>Working directory<input name="cwd" placeholder="Optional" value="${esc(command.workingDirectory || "")}"></label>
            <div class="environment-editor span-2"><div class="environment-head"><div><strong>Environment variables</strong><span>Optional values passed only to this task.</span></div><button class="button secondary small" type="button" data-editor="add-env">+ Add variable</button></div><div class="environment-rows" data-env-rows></div></div>
            ${type === "continuous" ? continuousAdvancedFields : scheduledAdvancedFields}
          </div>
        </details>
      </div>
      <p class="form-error hidden" role="alert" data-editor-error></p>
      <div class="dialog-actions"><button class="button secondary" type="button" data-editor="close">Cancel</button><button class="button primary" value="default">Save task</button></div>
    </form>`;
    for (const [name, value] of Object.entries(command.environment || {})) addEnvRow(name, value);
    if (type === "scheduled") syncSchedule();
    dialog.showModal();
  }

  // ---- history + output dialog

  const history = { dialog: null, task: null, runs: [], selected: "", loading: false };

  function ensureHistory() {
    if (history.dialog) return history.dialog;
    const dialog = document.createElement("dialog");
    dialog.className = "dialog run-history-dialog";
    dialog.innerHTML = `<div class="dialog-head"><div><h2 data-history-title>Run history</h2><p>Captured output from previous runs.</p></div><button class="icon-btn" type="button" data-history="close" aria-label="Close">×</button></div>
      <div class="run-history-layout"><div class="row-list" data-history-list></div><section class="run-history-log"><pre class="tasks-plugin-output" data-history-output>No run selected.</pre></section></div>`;
    document.body.append(dialog);
    dialog.addEventListener("click", event => {
      if (event.target.closest("[data-history='close']")) { dialog.close(); return; }
      const item = event.target.closest("[data-run]");
      if (item) selectRun(item.dataset.run);
    });
    dialog.addEventListener("keydown", event => {
      const item = event.target.closest?.("[data-run]");
      if (item && (event.key === "Enter" || event.key === " ")) { event.preventDefault(); selectRun(item.dataset.run); }
    });
    dialog.addEventListener("close", () => { history.selected = ""; });
    history.dialog = dialog;
    return dialog;
  }

  const duration = run => {
    const end = run.finishedAt ? new Date(run.finishedAt) : new Date();
    const seconds = Math.max(0, Math.round((end - new Date(run.startedAt)) / 1000));
    return seconds >= 60 ? `${Math.floor(seconds / 60)}m ${seconds % 60}s` : `${seconds}s`;
  };

  function renderRuns() {
    const list = history.dialog.querySelector("[data-history-list]");
    list.innerHTML = history.runs.length ? history.runs.map(run => {
      const label = run.finishedAt ? (run.success ? "success" : "failure") : "running";
      const code = run.exitCode == null ? "" : ` · exit ${run.exitCode}`;
      return `<article class="row run-history-row ${run.id === history.selected ? "active" : ""}" role="button" tabindex="0" data-run="${esc(run.id)}" aria-label="Open output from ${esc(fmtDate(run.startedAt))}"><div class="row-head"><div><strong>${esc(fmtDate(run.startedAt))}</strong><div class="meta">${esc(duration(run))}${esc(code)}</div></div><span class="status ${label}">${label}</span></div>${run.message ? `<div class="meta">${esc(run.message)}</div>` : ""}</article>`;
    }).join("") : `<div class="empty compact"><p>No runs recorded yet.</p></div>`;
  }

  async function loadRuns(selectLatest) {
    try {
      history.runs = (await call("tasks.history", { id: history.task.task.id, limit: 50 })).runs || [];
    } catch (error) { history.runs = []; fail(error); }
    renderRuns();
    if (selectLatest && history.runs.length) await selectRun(history.runs[0].id);
    else if (history.selected) await loadOutput();
  }

  async function loadOutput() {
    if (!history.selected || history.loading) return;
    history.loading = true;
    const pre = history.dialog.querySelector("[data-history-output]");
    try {
      const result = await call("tasks.output", { runId: history.selected, maxBytes: OUTPUT_BYTES });
      const atEnd = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
      const note = result.truncated ? `[showing the last ${Math.round(OUTPUT_BYTES / 1024)} KiB of ${Math.round(result.size / 1024)} KiB]\n` : "";
      pre.textContent = note + (result.output || (result.run?.finishedAt ? "(no output)" : "Waiting for output…"));
      if (atEnd) pre.scrollTop = pre.scrollHeight;
    } catch (error) { pre.textContent = error.message; }
    history.loading = false;
  }

  async function selectRun(runId) {
    history.selected = runId;
    renderRuns();
    history.dialog.querySelector("[data-history-output]").textContent = "Loading…";
    await loadOutput();
    const pre = history.dialog.querySelector("[data-history-output]");
    pre.scrollTop = pre.scrollHeight;
  }

  function openHistory(view) {
    if (!view) return;
    const dialog = ensureHistory();
    history.task = view;
    history.selected = "";
    dialog.querySelector("[data-history-title]").textContent = `${view.task.name} — run history`;
    dialog.querySelector("[data-history-output]").textContent = "No run selected.";
    dialog.showModal();
    loadRuns(true);
  }

  // ---- events: the only refresh mechanism (no polling)

  const unsubscribers = [
    runpilot.ws.on(PLUGIN, "tasks.changed", () => load()),
    runpilot.ws.on(PLUGIN, "tasks.status", view => { if (view?.task?.id) { upsert(view); render(); } }),
    runpilot.ws.on(PLUGIN, "tasks.run.completed", data => {
      if (data && data.success === false && data.message !== "stopped") ui.toast(`${data.name || "Task"} failed: ${data.message || "unknown error"}`, "error");
      if (history.dialog?.open && history.task?.task.id === data?.id) loadRuns(false);
    }),
    runpilot.ws.on(PLUGIN, "history.output", data => {
      if (history.dialog?.open && data?.id === history.selected) loadOutput();
    }),
  ];

  runpilot.navigation.register({
    id: "tasks", title: "Tasks", icon: { src: new URL("./icon.svg", import.meta.url).href },
    render: root => { page = root; render(); load(); },
  });
  load();

  return () => {
    unsubscribers.forEach(off => off());
    editor?.remove();
    history.dialog?.remove();
  };
}
