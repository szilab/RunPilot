// Backup plugin frontend. It uses only the public plugin API
// (runpilot.ws / navigation / ui) and never touches core internals.
import { ENGINE_LABELS, KIND_LABELS, browseFromNative, escapeHTML as esc, formatArgs, formatBytes, jobFromForm, jobTarget, lines, nativeFromBrowse, relativeTime, repositoryFromForm, runState, scheduleText, splitInterval, toolHint } from "./backup-helpers.js";

export * from "./backup-helpers.js";

const PLUGIN = "backup";
const OUTPUT_BYTES = 128 * 1024;
const HISTORY_LIMIT = 100; // history.list maximum
const PAGE_SIZE = 10;

export function activate(runpilot) {
  const { ui } = runpilot;
  const state = { jobs: [], repositories: [], tab: "backups", tools: [], os: "linux", loadError: "", error: "", loaded: false, busy: new Set() };
  let page = null, headerRoot = null;
  const wired = new WeakSet();
  const dialogs = new Set();

  const call = async (method, params = {}) => {
    const result = await runpilot.ws.call(PLUGIN, method, params);
    if (result && result.error) {
      const error = new Error(result.error.message || "Backup request failed");
      error.code = result.error.code;
      throw error;
    }
    return result;
  };
  const fail = error => ui.toast(error.message || String(error), "error");
  const fmtDate = value => value ? new Date(value).toLocaleString() : "—";
  const findJob = id => state.jobs.find(view => view.job.id === id);
  const findRepository = id => state.repositories.find(view => view.repository.id === id);
  const toolFor = engine => state.tools.find(tool => tool.engine === engine);
  const timeTag = value => `<time datetime="${esc(value)}" title="${esc(fmtDate(value))}">${esc(relativeTime(value))}</time>`;
  const optionList = (items, selected) => items.map(([value, label]) => `<option value="${esc(value)}" ${value === selected ? "selected" : ""}>${esc(label)}</option>`).join("");
  const mirrorEngine = () => state.os === "windows" ? "robocopy" : "rsync";

  function createDialog(className) {
    const dialog = document.createElement("dialog");
    dialog.className = `dialog ${className}`;
    document.body.append(dialog);
    dialogs.add(dialog);
    return dialog;
  }

  // ---- list page

  function card(view) {
    const { job, status } = view;
    const id = esc(job.id), busy = state.busy.has(job.id);
    const button = (action, label, style = "secondary", disabled = busy) => `<button class="button ${style} small" type="button" data-action="${action}" data-id="${id}" ${disabled ? "disabled" : ""}>${label}</button>`;
    const running = status.state === "running" || status.state === "stopping";
    const facts = [];
    if (running) {
      const step = status.steps > 1 ? ` · step ${esc(status.stepIndex)}/${esc(status.steps)} ${esc(status.step)}` : "";
      facts.push(`<span class="backup-card-running">${esc(KIND_LABELS[status.kind] || "Running")} running${step}</span>`);
      if (status.startedAt) facts.push(`Started ${timeTag(status.startedAt)}`);
    } else if (status.lastRunAt) {
      const kind = status.lastKind && status.lastKind !== "backup" ? `${esc(KIND_LABELS[status.lastKind] || status.lastKind)} ` : "Last run ";
      const result = status.lastSuccess == null ? `<span class="backup-card-result skipped">Skipped</span>` : status.lastSuccess ? `<span class="backup-card-result ok">Succeeded</span>` : `<span class="backup-card-result failed">Failed</span>`;
      facts.push(`${kind}${timeTag(status.lastRunAt)} · ${result}`);
    } else facts.push("Not run yet");
    if (job.schedule && !job.enabled) facts.push(`<span class="backup-card-paused">Schedule paused</span>`);
    else if (status.nextRunAt) facts.push(`Next ${timeTag(status.nextRunAt)}`);
    const tool = toolFor(job.engine);
    const ownExecutable = job.engine === "restic" ? findRepository(job.restic?.repositoryId)?.repository.executable : job.mirror?.executable;
    const notes = [
      status.error ? `<div class="form-error" role="alert">${esc(status.error)}</div>` : "",
      tool && !tool.available && !ownExecutable ? `<div class="form-error" role="alert">${esc(tool.engine)} is not installed on this host.</div>` : "",
      !running && status.lastSuccess === false && status.lastMessage ? `<div class="meta backup-card-note">${esc(status.lastMessage)}</div>` : "",
    ].join("");
    const label = status.state === "failure" ? "failed" : status.state;
    const primary = running ? button("stop", "Stop", "secondary", busy || status.state === "stopping") : button("run", "Run now", "primary");
    const secondary = button("history", "History", "secondary", false) + button("edit", "Edit") + button("delete", "Delete", "danger", busy || running);
    return `<article class="rp-card backup-card" data-job="${id}">
      <div class="backup-card-head"><h3 title="${esc(job.name)}">${esc(job.name)}</h3><span class="status ${esc(status.state)}">${esc(label)}</span></div>
      <div class="backup-card-summary"><span>${esc(ENGINE_LABELS[job.engine] || job.engine)} · ${esc(scheduleText(job.schedule))}</span><code title="${esc(jobTarget(job, state.repositories))}">${esc(jobTarget(job, state.repositories))}</code></div>
      <div class="backup-card-facts">${facts.map(fact => `<span>${fact}</span>`).join("")}</div>${notes}
      <div class="backup-card-actions"><div>${primary}</div><div>${secondary}</div></div>
    </article>`;
  }

  function repositoryCard(view) {
    const { repository, status, jobs } = view;
    const id = esc(repository.id), busy = state.busy.has(repository.id);
    const button = (action, label, style = "secondary", disabled = busy, title = "") => `<button class="button ${style} small" type="button" data-action="${action}" data-id="${id}" ${title ? `title="${esc(title)}"` : ""} ${disabled ? "disabled" : ""}>${label}</button>`;
    const running = status.state === "running" || status.state === "stopping";
    const facts = [];
    if (running) {
      facts.push(`<span class="backup-card-running">${esc(KIND_LABELS[status.kind] || "Running")} running</span>`);
      if (status.startedAt) facts.push(`Started ${timeTag(status.startedAt)}`);
    } else if (status.lastRunAt) {
      const result = status.lastSuccess == null ? `<span class="backup-card-result skipped">Skipped</span>` : status.lastSuccess ? `<span class="backup-card-result ok">Succeeded</span>` : `<span class="backup-card-result failed">Failed</span>`;
      facts.push(`${esc(KIND_LABELS[status.lastKind] || status.lastKind || "Last run")} ${timeTag(status.lastRunAt)} · ${result}`);
    } else facts.push("No repository operation yet");
    facts.push(jobs === 1 ? "1 backup" : `${jobs} backups`);
    const tool = toolFor("restic");
    const notes = [
      status.error ? `<div class="form-error" role="alert">${esc(status.error)}</div>` : "",
      tool && !tool.available && !repository.executable ? `<div class="form-error" role="alert">restic is not installed on this host.</div>` : "",
      !running && status.lastSuccess === false && status.lastMessage ? `<div class="meta backup-card-note">${esc(status.lastMessage)}</div>` : "",
    ].join("");
    const label = status.state === "failure" ? "failed" : status.state;
    const primary = running ? button("repo-stop", "Stop", "secondary", busy || status.state === "stopping") : button("repo-snapshots", "Snapshots", "primary", false);
    const secondary = button("repo-init", "Initialize", "secondary", busy || running, "Create the repository at this location. Only needed once, for a new repository.") + button("repo-check", "Check", "secondary", busy || running) + button("repo-unlock", "Unlock", "secondary", busy || running, "Remove stale locks")
      + button("repo-history", "History", "secondary", false) + button("repo-edit", "Edit") + button("repo-delete", "Delete", "danger", busy || running || jobs > 0, jobs > 0 ? "Delete the backups that use this repository first" : "");
    return `<article class="rp-card backup-card" data-repository="${id}">
      <div class="backup-card-head"><h3 title="${esc(repository.name)}">${esc(repository.name)}</h3><span class="status ${esc(status.state)}">${esc(label)}</span></div>
      <div class="backup-card-summary"><span>Restic repository</span><code title="${esc(repository.location)}">${esc(repository.location)}</code></div>
      <div class="backup-card-facts">${facts.map(fact => `<span>${fact}</span>`).join("")}</div>${notes}
      <div class="backup-card-actions"><div>${primary}</div><div>${secondary}</div></div>
    </article>`;
  }

  function renderHeader() {
    if (!headerRoot || (page?.closest && !page.closest(".page")?.classList.contains("active"))) return;
    const repositories = state.tab === "repositories";
    headerRoot.innerHTML = `<button class="button primary small" type="button" data-action="${repositories ? "repo-add" : "add"}" ${state.loadError ? "disabled" : ""}>${repositories ? "Add repository" : "Add backup"}</button>`;
    headerRoot.querySelector?.("[data-action]")?.addEventListener("click", () => repositories ? openRepositoryEditor(null) : openEditor(null));
  }

  function toolNotices() {
    // Jobs and repositories with their own executable do not depend on the default command.
    const used = new Set(state.jobs.filter(view => view.job.engine !== "restic" && !view.job.mirror?.executable).map(view => view.job.engine));
    if (state.repositories.some(view => !view.repository.executable)) used.add("restic");
    return state.tools.filter(tool => !tool.available && used.has(tool.engine)).map(tool => `<div class="notice backup-tool-notice" role="status"><strong>${esc(ENGINE_LABELS[tool.engine])} is not available</strong><span>${esc(toolHint(tool, state.os))}</span><div><button class="button secondary small" type="button" data-action="tools">Check again</button></div></div>`).join("");
  }

  const TABS = [["backups", "Backups"], ["repositories", "Repositories"]];

  function tabContent() {
    if (state.tab === "repositories") {
      if (state.repositories.length) return `<div class="backup-grid">${state.repositories.map(repositoryCard).join("")}</div>`;
      return state.loaded && !state.loadError ? `<div class="empty"><h2>No repositories yet</h2><p>A repository is where restic keeps its snapshots. Add one, initialize it, then choose it when you add a backup.</p></div>` : "";
    }
    if (state.jobs.length) return `<div class="backup-grid">${state.jobs.map(card).join("")}</div>`;
    return state.loaded && !state.loadError ? `<div class="empty"><h2>No backups yet</h2><p>Back up folders into a restic repository, or mirror a folder with ${esc(ENGINE_LABELS[mirrorEngine()])}.</p></div>` : "";
  }

  function render() {
    if (!page || !page.isConnected) return;
    renderHeader();
    const tabs = TABS.map(([id, label]) => `<button class="button secondary${state.tab === id ? " active" : ""}" type="button" role="tab" aria-selected="${state.tab === id}" data-tab="${id}">${label}</button>`).join("");
    page.innerHTML = `<div class="backup-plugin">
      <div class="rp-segmented backup-tabs" role="tablist" aria-label="Backup section">${tabs}</div>
      ${state.loadError ? `<div class="notice" role="alert"><strong>Backup configuration is unavailable</strong><span>${esc(state.loadError)}</span><span>The stored configuration was left untouched. Repair or remove plugins/${PLUGIN}/data/storage.json and restart RunPilot.</span></div>` : ""}
      ${state.error ? `<div class="notice" role="alert"><span>${esc(state.error)}</span></div>` : ""}
      ${toolNotices()}
      ${tabContent()}
      ${!state.loaded ? `<div class="notice" role="status">Loading backups…</div>` : ""}
    </div>`;
    if (!wired.has(page)) {
      wired.add(page);
      page.addEventListener("click", onPageClick);
    }
  }

  async function load() {
    try {
      const result = await call("backup.list");
      state.jobs = result.jobs || [];
      state.repositories = result.repositories || [];
      state.tools = result.tools || [];
      state.os = result.os || "linux";
      state.loadError = result.loadError || "";
      state.error = "";
    } catch (error) { state.error = error.message; }
    state.loaded = true;
    render();
  }

  function upsert(view) {
    const index = state.jobs.findIndex(v => v.job.id === view.job.id);
    if (index >= 0) state.jobs[index] = view; else state.jobs.push(view);
    state.jobs.sort((a, b) => a.job.name.toLowerCase().localeCompare(b.job.name.toLowerCase()));
  }

  function upsertRepository(view) {
    const index = state.repositories.findIndex(v => v.repository.id === view.repository.id);
    if (index >= 0) state.repositories[index] = view; else state.repositories.push(view);
    state.repositories.sort((a, b) => a.repository.name.toLowerCase().localeCompare(b.repository.name.toLowerCase()));
  }

  // actRepository runs a repository RPC and refreshes the repository list.
  async function actRepository(id, method, params = {}) {
    state.busy.add(id);
    render();
    try {
      const result = await call(method, { id, ...params });
      if (method === "backup.repository.delete") state.repositories = state.repositories.filter(v => v.repository.id !== id);
      else if (result?.repository?.id) upsertRepository(result);
      return result;
    } catch (error) { fail(error); } finally {
      state.busy.delete(id);
      render();
    }
  }

  async function act(id, method) {
    state.busy.add(id);
    render();
    try {
      const result = await call(method, { id });
      const view = result?.job?.job ? result.job : result;
      if (method === "backup.delete") state.jobs = state.jobs.filter(v => v.job.id !== id);
      else if (view?.job?.id) upsert(view);
    } catch (error) { fail(error); }
    state.busy.delete(id);
    render();
  }

  async function onPageClick(event) {
    const tab = event.target.closest("[data-tab]");
    if (tab) {
      if (state.tab !== tab.dataset.tab) { state.tab = tab.dataset.tab; render(); }
      return;
    }
    const button = event.target.closest("[data-action]");
    if (!button || button.disabled) return;
    const { action, id } = button.dataset;
    const view = findJob(id);
    const repoView = findRepository(id);
    if (action.startsWith("repo-")) {
      if (!repoView) return;
      const { repository } = repoView;
      if (action === "repo-edit") openRepositoryEditor(repoView);
      else if (action === "repo-history") openHistory({ id: repository.id, name: repository.name });
      else if (action === "repo-snapshots") openSnapshots(repoView);
      else if (action === "repo-stop") actRepository(id, "backup.repository.stop");
      else if (action === "repo-delete") {
        if (await ui.confirm({ title: "Delete repository?", message: `Remove “${repository.name}” from RunPilot? Its run history is kept, and the repository itself and its snapshots are not touched.`, confirmLabel: "Delete repository", danger: true })) actRepository(id, "backup.repository.delete");
      } else if (["repo-init", "repo-check", "repo-unlock"].includes(action)) runMaintenance(repoView, action.slice(5));
      return;
    }
    if (action === "edit") openEditor(view);
    else if (action === "history") openHistory(view && { id: view.job.id, name: view.job.name });
    else if (action === "tools") {
      try { state.tools = (await call("backup.tools", { refresh: true })).tools || []; } catch (error) { fail(error); }
      render();
    } else if (action === "delete") {
      if (view && await ui.confirm({ title: "Delete backup?", message: `Remove “${view.job.name}”? Its run history is kept, and nothing is deleted from the backup destination.`, confirmLabel: "Delete backup", danger: true })) act(id, "backup.delete");
    } else if (action === "run" || action === "stop") act(id, `backup.${action}`);
  }

  // runMaintenance starts init, check or unlock on a repository.
  async function runMaintenance(repoView, kind) {
    const { repository } = repoView;
    if (kind === "unlock" && !await ui.confirm({ title: "Remove stale locks?", message: "Only do this when no other restic process uses the repository.", confirmLabel: "Remove locks" })) return false;
    if (kind === "init" && !await ui.confirm({ title: "Initialize repository?", message: `Create a new restic repository at ${repository.location}. This is only needed once; an existing repository is left unchanged.`, confirmLabel: "Initialize" })) return false;
    const result = await actRepository(repository.id, "backup.repository.maintenance", { kind });
    if (result) ui.toast(`${KIND_LABELS[kind]} started; follow it in History.`);
    return !!result;
  }

  // ---- folder picker

  function pickFolder(current, title = "Choose folder") {
    return new Promise(resolve => {
      const dialog = createDialog("backup-picker-dialog");
      let path = browseFromNative(current, state.os), result = null;
      dialog.innerHTML = `<div class="dialog-head"><h2>${esc(title)}</h2><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div>
        <div class="backup-picker"><div class="backup-picker-path"></div><div class="backup-picker-list" role="listbox" aria-label="Folders"></div></div>
        <p class="form-error hidden" role="alert" data-picker-error></p>
        <div class="dialog-actions"><button class="button secondary" type="button" data-close>Cancel</button><button class="button primary" type="button" data-choose>Choose this folder</button></div>`;
      const pathNode = dialog.querySelector(".backup-picker-path"), listNode = dialog.querySelector(".backup-picker-list");
      const error = dialog.querySelector("[data-picker-error]"), choose = dialog.querySelector("[data-choose]");
      const display = () => path || state.os === "windows" ? nativeFromBrowse(path, state.os) : "/";
      async function show() {
        pathNode.innerHTML = `<button type="button" class="button secondary small" data-up ${path ? "" : "disabled"} aria-label="Parent folder">↑</button><span title="${esc(path || state.os !== "windows" ? display() : "This PC")}">${esc(path || state.os !== "windows" ? display() : "This PC")}</span>`;
        listNode.innerHTML = `<p class="backup-picker-empty">Loading…</p>`;
        choose.disabled = state.os === "windows" && !path;
        try {
          const listed = await call("backup.browse", { path });
          listNode.innerHTML = listed.folders.length ? listed.folders.map(name => `<button type="button" class="backup-picker-folder" data-folder="${esc(name)}"><span aria-hidden="true">📁</span><span>${esc(name)}</span></button>`).join("") : `<p class="backup-picker-empty">No subfolders.</p>`;
          error.classList.add("hidden");
        } catch (cause) {
          listNode.innerHTML = "";
          error.textContent = cause.message;
          error.classList.remove("hidden");
        }
      }
      dialog.addEventListener("click", event => {
        const target = event.target.closest("button");
        if (!target) return;
        if (target.dataset.close !== undefined) dialog.close();
        else if (target.dataset.folder) { path = path ? `${path}/${target.dataset.folder}` : target.dataset.folder; void show(); }
        else if (target.dataset.up !== undefined) { path = path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : ""; void show(); }
        else if (target.dataset.choose !== undefined && !target.disabled) { result = display(); dialog.close(); }
      });
      dialog.addEventListener("close", () => { dialogs.delete(dialog); dialog.remove(); resolve(result); }, { once: true });
      dialog.showModal();
      void show();
    });
  }

  // ---- editor dialog

  function openEditor(view) {
    const dialog = createDialog("backup-editor-dialog");
    const job = view?.job;
    let engine = job?.engine || "restic";
    const restic = job?.restic || {}, mirror = job?.mirror || {}, retention = restic.retention || {};
    const schedule = job?.schedule || (job ? null : { type: "daily", timeOfDay: "03:00" });
    const [amount, unit] = splitInterval(schedule?.intervalSeconds || 86400);
    const advancedInUse = !!(restic.excludes?.length || restic.tags?.length || restic.retention || restic.checkAfterBackup || mirror.excludes?.length || mirror.additionalArgs?.length || mirror.executable || job?.timeoutSeconds || schedule?.timeZone);
    const engineChoice = job ? "" : `<div class="span-2 backup-engine-choice"><span>Engine</span><div class="rp-segmented" role="radiogroup" aria-label="Backup engine">${[["restic", "Restic repository"], [mirrorEngine(), `${ENGINE_LABELS[mirrorEngine()]} mirror`]].map(([value, label]) => `<label class="button secondary small"><input type="radio" name="engine" value="${value}" ${value === engine ? "checked" : ""}>${esc(label)}</label>`).join("")}</div><p class="meta backup-engine-help" data-engine-help></p></div>`;
    const browse = (target, label = "Browse…") => `<button class="button secondary small" type="button" data-pick="${target}">${label}</button>`;
    dialog.innerHTML = `<form method="dialog">
      <div class="dialog-head"><h2>${job ? `Edit ${esc(ENGINE_LABELS[engine])} backup` : "Add backup"}</h2><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div>
      <input type="hidden" name="id" value="${esc(job?.id || "")}">
      <div class="form-grid">
        <label class="span-2">Name<input name="name" required maxlength="200" value="${esc(job?.name || "")}" placeholder="Photos offsite"></label>
        ${engineChoice}
        <fieldset class="form-grid span-2 backup-section" data-engine-section="restic">
          <label class="span-2">Folders to back up<span class="backup-field-row"><textarea name="sources" rows="3" spellcheck="false" placeholder="One folder per line">${esc((restic.sources || []).join("\n"))}</textarea>${browse("sources", "Add folder…")}</span></label>
          <label class="span-2">Repository<select name="repositoryId" required ${job || !state.repositories.length ? "disabled" : ""}>${job ? "" : `<option value="">Choose a repository…</option>`}${optionList(state.repositories.map(view => [view.repository.id, `${view.repository.name} — ${view.repository.location}`]), restic.repositoryId || "")}</select>
            <span class="meta">${job ? "A backup stays with the repository it was created for. To use another one, delete this backup and add a new one." : state.repositories.length ? "Where the snapshots are stored. Repositories are managed on the Repositories tab." : `There is no repository yet. <button class="button secondary small" type="button" data-goto-repositories>Add a repository</button> first.`}</span></label>
        </fieldset>
        <fieldset class="form-grid span-2 backup-section" data-engine-section="mirror">
          <label class="span-2">Source folder<span class="backup-field-row"><input name="source" spellcheck="false" value="${esc(mirror.source || "")}">${browse("source")}</span></label>
          <label class="span-2">Destination<span class="backup-field-row"><input name="destination" spellcheck="false" placeholder="${state.os === "windows" ? "D:\\Backup or \\\\server\\share" : "/backup/folder or user@host:/backup"}" value="${esc(mirror.destination || "")}">${browse("destination")}</span></label>
          <label class="span-2">Mode<select name="mode">${optionList([["copy", "Copy — keep files deleted from the source"], ["mirror", "Mirror — also delete them from the destination"]], mirror.delete ? "mirror" : "copy")}</select></label>
        </fieldset>
        <label>Repeat<select name="scheduleType">${optionList([["manual", "Manual only"], ["interval", "Every…"], ["daily", "Daily at…"], ["cron", "Cron expression"]], schedule?.type || "manual")}</select></label>
        <span data-schedule="interval" class="backup-inline backup-schedule-value"><input name="intervalValue" type="number" min="1" aria-label="Interval" value="${esc(amount)}"><select name="intervalUnit" aria-label="Interval unit">${optionList([["minutes", "minutes"], ["hours", "hours"]], unit)}</select></span>
        <span data-schedule="daily" class="backup-schedule-value"><input name="time" type="time" aria-label="Time of day" value="${esc(schedule?.timeOfDay || "03:00")}"></span>
        <span data-schedule="cron" class="backup-schedule-value"><input name="cron" aria-label="Cron expression (5 or 6 fields)" placeholder="0 30 2 * * 1-5" value="${esc(schedule?.cron || "")}"></span>
        <span data-schedule="manual" class="backup-schedule-value meta">Runs only when you start it.</span>
        <details class="rp-disclosure backup-advanced span-2" ${advancedInUse ? "open" : ""}>
          <summary>Advanced settings</summary>
          <div class="form-grid backup-advanced-fields">
            <fieldset class="form-grid span-2 backup-section" data-engine-section="restic">
              <label class="span-2">Exclude<textarea name="excludes" rows="2" spellcheck="false" placeholder="One pattern per line, for example *.tmp">${esc((restic.excludes || []).join("\n"))}</textarea></label>
              <label class="span-2">Tags<input name="tags" spellcheck="false" placeholder="photos, offsite" value="${esc((restic.tags || []).join(", "))}"></label>
              <div class="span-2 backup-retention"><strong>Keep snapshots</strong><span class="meta">Older snapshots are forgotten after each backup. Leave empty to keep everything.</span>
                <div class="backup-retention-grid">
                  <label>Last<input name="keepLast" type="number" min="0" value="${esc(retention.keepLast || "")}"></label>
                  <label>Daily<input name="keepDaily" type="number" min="0" value="${esc(retention.keepDaily || "")}"></label>
                  <label>Weekly<input name="keepWeekly" type="number" min="0" value="${esc(retention.keepWeekly || "")}"></label>
                  <label>Monthly<input name="keepMonthly" type="number" min="0" value="${esc(retention.keepMonthly || "")}"></label>
                  <label>Yearly<input name="keepYearly" type="number" min="0" value="${esc(retention.keepYearly || "")}"></label>
                  <label>Within<input name="keepWithin" placeholder="30d" value="${esc(retention.keepWithin || "")}"></label>
                </div>
                <label class="check"><input name="prune" type="checkbox" ${retention.prune || !restic.retention ? "checked" : ""}> Free the space of forgotten snapshots (prune)</label>
              </div>
              <label class="check span-2"><input name="checkAfterBackup" type="checkbox" ${restic.checkAfterBackup ? "checked" : ""}> Check the repository after each backup</label>
              ${state.os === "windows" ? `<label class="check span-2"><input name="useFsSnapshot" type="checkbox" ${restic.useFsSnapshot ? "checked" : ""}> Use a Volume Shadow Copy snapshot (needs administrator rights)</label>` : ""}
            </fieldset>
            <fieldset class="form-grid span-2 backup-section" data-engine-section="mirror">
              <label class="span-2">Exclude folders${engine === "rsync" || (!job && mirrorEngine() === "rsync") ? " and files" : ""}<textarea name="excludes" rows="2" spellcheck="false" placeholder="One pattern per line">${esc((mirror.excludes || []).join("\n"))}</textarea></label>
              ${mirrorEngine() === "robocopy" ? `<label class="span-2">Exclude files<textarea name="excludeFiles" rows="2" spellcheck="false" placeholder="*.tmp">${esc((mirror.excludeFiles || []).join("\n"))}</textarea></label>
              <label>Retries<input name="retries" type="number" min="0" value="${esc(mirror.retries || 0)}"></label>
              <label>Retry wait (s)<input name="retryWait" type="number" min="0" value="${esc(mirror.retryWaitSeconds || 5)}"></label>` : ""}
              <label>Additional arguments<input name="additionalArgs" spellcheck="false" value="${esc(formatArgs(mirror.additionalArgs || []))}"></label>
              <label>Executable<input name="executable" spellcheck="false" placeholder="${esc(mirrorEngine())}" value="${esc(mirror.executable || "")}"></label>
            </fieldset>
            <label>Time zone<input name="timeZone" placeholder="Server time" value="${esc(schedule?.timeZone || "")}"></label>
            <label>Timeout (minutes, 0 = none)<input name="timeoutMinutes" type="number" min="0" value="${esc(Math.round((job?.timeoutSeconds || 0) / 60))}"></label>
            <label class="check span-2"><input name="enabled" type="checkbox" ${!job || job.enabled ? "checked" : ""}> Schedule enabled</label>
          </div>
        </details>
      </div>
      <p class="form-error hidden" role="alert" data-editor-error></p>
      <div class="dialog-actions"><button class="button secondary" type="button" data-close>Cancel</button><button class="button primary" value="default">Save backup</button></div>
    </form>`;
    const form = dialog.querySelector("form");
    const field = name => form.querySelector(`.backup-section:not([disabled]) [name="${name}"]`) || form.querySelector(`[name="${name}"]`);
    function sync() {
      const section = engine === "restic" ? "restic" : "mirror";
      for (const node of form.querySelectorAll("[data-engine-section]")) {
        const hidden = node.dataset.engineSection !== section;
        node.classList.toggle("hidden", hidden);
        node.disabled = hidden;
      }
      const type = form.querySelector("[name='scheduleType']").value;
      for (const node of form.querySelectorAll("[data-schedule]")) {
        node.classList.toggle("hidden", node.dataset.schedule !== type);
        node.querySelectorAll("input,select").forEach(input => { input.disabled = node.dataset.schedule !== type; });
      }
      const help = form.querySelector("[data-engine-help]");
      if (help) {
        const tool = toolFor(engine);
        help.textContent = tool && !tool.available ? toolHint(tool, state.os) : engine === "restic" ? "Versioned, deduplicated and encrypted snapshots you can restore." : "A plain copy of one folder, easy to open without any tool.";
        help.classList.toggle("form-error", !!tool && !tool.available);
      }
    }
    form.addEventListener("change", event => {
      if (event.target.name === "engine") engine = event.target.value;
      if (event.target.name === "engine" || event.target.name === "scheduleType") sync();
    });
    form.addEventListener("click", async event => {
      const target = event.target.closest("button");
      if (!target) return;
      if (target.dataset.close !== undefined) { dialog.close(); return; }
      if (target.dataset.gotoRepositories !== undefined) { dialog.close(); state.tab = "repositories"; render(); return; }
      const pick = target.dataset.pick;
      if (!pick) return;
      const input = field(pick);
      const start = pick === "sources" ? lines(input.value).at(-1) : input.value;
      const chosen = await pickFolder(start, pick === "sources" ? "Add folder to back up" : "Choose folder");
      if (!chosen) return;
      if (pick === "sources") input.value = [...lines(input.value), chosen].join("\n");
      else input.value = chosen;
    });
    form.addEventListener("submit", async event => {
      event.preventDefault();
      const error = dialog.querySelector("[data-editor-error]");
      error.classList.add("hidden");
      const value = name => field(name)?.disabled ? "" : field(name)?.value ?? "";
      const checked = name => !!field(name)?.checked;
      try {
        const saved = jobFromForm({
          id: value("id"), name: value("name"), engine, enabled: checked("enabled"), timeoutMinutes: value("timeoutMinutes"),
          scheduleType: value("scheduleType"), intervalValue: value("intervalValue"), intervalUnit: value("intervalUnit"), time: value("time"), cron: value("cron"), timeZone: value("timeZone"),
          repositoryId: job ? job.restic?.repositoryId : value("repositoryId"), sources: value("sources"), excludes: value("excludes"), tags: value("tags"),
          executable: value("executable"), useFsSnapshot: checked("useFsSnapshot"), checkAfterBackup: checked("checkAfterBackup"),
          keepLast: value("keepLast"), keepDaily: value("keepDaily"), keepWeekly: value("keepWeekly"), keepMonthly: value("keepMonthly"), keepYearly: value("keepYearly"), keepWithin: value("keepWithin"), prune: checked("prune"),
          source: value("source"), destination: value("destination"), mode: value("mode"), excludeFiles: value("excludeFiles"), retries: value("retries"), retryWait: value("retryWait"), additionalArgs: value("additionalArgs"),
        });
        upsert(await call(saved.id ? "backup.update" : "backup.create", { job: saved }));
        dialog.close();
        render();
      } catch (err) {
        error.textContent = err.message;
        error.classList.remove("hidden");
      }
    });
    dialog.addEventListener("close", () => { dialogs.delete(dialog); dialog.remove(); }, { once: true });
    sync();
    dialog.showModal();
  }

  // ---- repository editor

  function openRepositoryEditor(view) {
    const dialog = createDialog("backup-editor-dialog backup-repository-dialog");
    const repository = view?.repository || {};
    const locked = !!view && view.jobs > 0;
    const advancedInUse = !!repository.executable;
    dialog.innerHTML = `<form method="dialog">
      <div class="dialog-head"><h2>${view ? "Edit repository" : "Add repository"}</h2><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div>
      <input type="hidden" name="id" value="${esc(repository.id || "")}">
      <div class="form-grid">
        <label class="span-2">Name<input name="name" required maxlength="200" value="${esc(repository.name || "")}" placeholder="Offsite B2"></label>
        <label class="span-2">Location<span class="backup-field-row"><input name="location" required spellcheck="false" ${locked ? "readonly" : ""} placeholder="/backup/restic or sftp:user@host:/repo" value="${esc(repository.location || "")}">${locked ? "" : `<button class="button secondary small" type="button" data-pick="location">Browse…</button>`}</span>
          <span class="meta">${locked ? "The location cannot change while backups use this repository." : "A folder on this host or any location restic supports. Use Initialize on the repository card to create a new one."}</span></label>
        <label class="span-2">Password file<input name="passwordFile" spellcheck="false" placeholder="${state.os === "windows" ? "C:\\ProgramData\\restic\\password" : "/etc/restic/password"}" value="${esc(repository.passwordFile || "")}"><span class="meta">A file on this host that contains the repository password. RunPilot never stores the password.</span></label>
        <label class="span-2">Environment file<input name="environmentFile" spellcheck="false" placeholder="Optional, for example /etc/restic/b2.env" value="${esc(repository.environmentFile || "")}"><span class="meta">NAME=value lines passed to restic, such as RESTIC_PASSWORD or cloud storage keys. Read at every run, never stored. Needed when there is no password file.</span></label>
        <details class="rp-disclosure backup-advanced span-2" ${advancedInUse ? "open" : ""}>
          <summary>Advanced settings</summary>
          <div class="form-grid backup-advanced-fields"><label class="span-2">Restic executable<input name="executable" spellcheck="false" placeholder="restic" value="${esc(repository.executable || "")}"></label></div>
        </details>
      </div>
      <p class="form-error hidden" role="alert" data-editor-error></p>
      <div class="dialog-actions"><button class="button secondary" type="button" data-close>Cancel</button><button class="button primary" value="default">Save repository</button></div>
    </form>`;
    const form = dialog.querySelector("form");
    form.addEventListener("click", async event => {
      const target = event.target.closest("button");
      if (!target) return;
      if (target.dataset.close !== undefined) { dialog.close(); return; }
      if (target.dataset.pick) {
        const input = form.querySelector(`[name="${target.dataset.pick}"]`);
        const chosen = await pickFolder(input.value, "Choose repository folder");
        if (chosen) input.value = chosen;
      }
    });
    form.addEventListener("submit", async event => {
      event.preventDefault();
      const error = dialog.querySelector("[data-editor-error]");
      error.classList.add("hidden");
      const value = name => form.querySelector(`[name="${name}"]`)?.value ?? "";
      try {
        const saved = repositoryFromForm({ id: value("id"), name: value("name"), location: value("location"), passwordFile: value("passwordFile"), environmentFile: value("environmentFile"), executable: value("executable") });
        upsertRepository(await call(saved.id ? "backup.repository.update" : "backup.repository.create", { repository: saved }));
        dialog.close();
        render();
      } catch (err) {
        error.textContent = err.message;
        error.classList.remove("hidden");
      }
    });
    dialog.addEventListener("close", () => { dialogs.delete(dialog); dialog.remove(); }, { once: true });
    dialog.showModal();
  }

  // ---- snapshots and restore (a repository's contents)

  function openSnapshots(view) {
    if (!view) return;
    const dialog = createDialog("backup-snapshots-dialog");
    const { repository } = view;
    let snapshots = [], restoring = null;
    dialog.innerHTML = `<div class="dialog-head"><div><h2>${esc(repository.name)} — snapshots</h2><p class="meta" title="${esc(repository.location)}">${esc(repository.location)}</p></div><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div>
      <div class="backup-snapshot-tools"><button class="button secondary small" type="button" data-refresh>Refresh</button></div>
      <div class="backup-snapshot-list" data-list><p class="meta">Loading snapshots…</p></div>
      <form class="backup-restore hidden" data-restore>
        <h3>Restore <span data-restore-title></span></h3>
        <p class="meta">Files are restored with their full original paths under a new or empty folder. Nothing is overwritten.</p>
        <label>Restore into<span class="backup-field-row"><input name="target" required spellcheck="false" placeholder="${state.os === "windows" ? "D:\\Restore\\photos" : "/restore/photos"}"><button class="button secondary small" type="button" data-pick-target>Browse…</button></span></label>
        <label>Only these paths<textarea name="include" rows="2" spellcheck="false" placeholder="Optional, one path per line; empty restores everything"></textarea></label>
        <p class="form-error hidden" role="alert" data-restore-error></p>
        <div class="dialog-actions"><button class="button secondary" type="button" data-restore-cancel>Cancel</button><button class="button primary" type="submit">Restore</button></div>
      </form>`;
    const list = dialog.querySelector("[data-list]"), restoreForm = dialog.querySelector("[data-restore]");
    function renderList() {
      list.innerHTML = snapshots.length ? `<table class="backup-snapshot-table"><thead><tr><th>Time</th><th>ID</th><th>Folders</th><th>Tags</th><th>Size</th><th></th></tr></thead><tbody>${snapshots.map(item => `<tr>
        <td>${esc(fmtDate(item.time))}</td><td><code>${esc(item.shortId || item.id.slice(0, 8))}</code></td>
        <td class="backup-snapshot-paths" title="${esc(item.paths.join("\n"))}">${esc(item.paths.join(", "))}</td><td>${esc((item.tags || []).join(", "))}</td><td>${esc(formatBytes(item.bytes))}</td>
        <td><button class="button secondary small" type="button" data-restore-id="${esc(item.id)}">Restore…</button></td></tr>`).join("")}</tbody></table>`
        : `<div class="empty compact"><p>No snapshots yet. Run a backup that uses this repository to create the first one.</p></div>`;
    }
    async function refresh() {
      list.innerHTML = `<p class="meta">Loading snapshots…</p>`;
      try {
        snapshots = (await call("backup.repository.snapshots", { id: repository.id })).snapshots || [];
        renderList();
      } catch (error) {
        const missing = /config file|does not exist|repository not found|no such file/i.test(error.message);
        list.innerHTML = `<div class="notice" role="alert"><strong>Could not list snapshots</strong><span>${esc(error.message)}</span>${missing ? `<span>If this is a new repository, initialize it first.</span><div><button class="button secondary small" type="button" data-init>Initialize repository</button></div>` : ""}</div>`;
      }
    }
    dialog.addEventListener("click", async event => {
      const target = event.target.closest("button");
      if (!target) return;
      if (target.dataset.close !== undefined) dialog.close();
      else if (target.dataset.refresh !== undefined) refresh();
      else if (target.dataset.init !== undefined) { if (await runMaintenance(view, "init")) dialog.close(); }
      else if (target.dataset.restoreId) {
        restoring = snapshots.find(item => item.id === target.dataset.restoreId);
        dialog.querySelector("[data-restore-title]").textContent = `${restoring.shortId || restoring.id.slice(0, 8)} from ${fmtDate(restoring.time)}`;
        restoreForm.classList.remove("hidden");
        restoreForm.querySelector("[name='target']").focus();
      } else if (target.dataset.restoreCancel !== undefined) {
        restoring = null;
        restoreForm.classList.add("hidden");
      } else if (target.dataset.pickTarget !== undefined) {
        const input = restoreForm.querySelector("[name='target']");
        const chosen = await pickFolder(input.value, "Restore into");
        if (chosen) input.value = chosen;
      }
    });
    restoreForm.addEventListener("submit", async event => {
      event.preventDefault();
      const error = dialog.querySelector("[data-restore-error]");
      error.classList.add("hidden");
      try {
        await call("backup.repository.restore", { id: repository.id, snapshot: restoring.id, target: restoreForm.querySelector("[name='target']").value.trim(), include: lines(restoreForm.querySelector("[name='include']").value) });
        ui.toast("Restore started; follow it in History.");
        dialog.close();
      } catch (err) {
        error.textContent = err.message;
        error.classList.remove("hidden");
      }
    });
    dialog.addEventListener("close", () => { dialogs.delete(dialog); dialog.remove(); }, { once: true });
    dialog.showModal();
    refresh();
  }

  // ---- history and output

  const history = { dialog: null, job: null, runs: [], selected: "", loading: false, page: 0 };

  const duration = run => {
    const end = run.finishedAt ? new Date(run.finishedAt) : new Date();
    const seconds = Math.max(0, Math.round((end - new Date(run.startedAt)) / 1000));
    if (seconds >= 3600) return `${Math.floor(seconds / 3600)}h ${Math.floor(seconds % 3600 / 60)}m`;
    return seconds >= 60 ? `${Math.floor(seconds / 60)}m ${seconds % 60}s` : `${seconds}s`;
  };

  function renderRuns() {
    const listNode = history.dialog.querySelector("[data-history-list]");
    const pager = history.dialog.querySelector("[data-history-pager]");
    const pages = Math.max(1, Math.ceil(history.runs.length / PAGE_SIZE));
    history.page = Math.min(Math.max(0, history.page), pages - 1);
    const first = history.page * PAGE_SIZE;
    const runs = history.runs.slice(first, first + PAGE_SIZE);
    listNode.innerHTML = runs.length ? runs.map(run => {
      const label = runState(run);
      const kind = KIND_LABELS[run.kind] || run.kind;
      const message = run.message && label !== "success" ? `<span class="backup-history-message">${esc(run.message)}</span>` : "";
      return `<button type="button" class="backup-history-row ${run.id === history.selected ? "active" : ""}" data-run="${esc(run.id)}" aria-pressed="${run.id === history.selected}"><span class="backup-history-when"><strong>${esc(fmtDate(run.startedAt))}</strong><span class="meta">${esc(kind)} · ${esc(duration(run))}</span>${message}</span><span class="status ${label === "skipped" ? "idle" : label}">${label}</span></button>`;
    }).join("") : `<div class="empty compact"><p>No runs recorded yet.</p></div>`;
    pager.innerHTML = history.runs.length > PAGE_SIZE
      ? `<button class="button secondary small" type="button" data-history-page="-1" aria-label="Newer runs" ${history.page === 0 ? "disabled" : ""}>‹</button><span>${first + 1}–${first + runs.length} of ${history.runs.length}</span><button class="button secondary small" type="button" data-history-page="1" aria-label="Older runs" ${history.page >= pages - 1 ? "disabled" : ""}>›</button>`
      : "";
  }

  async function loadRuns(selectLatest) {
    try { history.runs = (await call("backup.history", { id: history.job.id, limit: HISTORY_LIMIT })).runs || []; } catch (error) { history.runs = []; fail(error); }
    renderRuns();
    if (selectLatest && history.runs.length) await selectRun(history.runs[0].id);
    else if (history.selected) await loadOutput();
  }

  async function loadOutput() {
    if (!history.selected || history.loading) return;
    history.loading = true;
    const pre = history.dialog.querySelector("[data-history-output]");
    try {
      const result = await call("backup.output", { runId: history.selected, maxBytes: OUTPUT_BYTES });
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

  // openHistory shows the runs of a backup or a repository ({ id, name }).
  function openHistory(subject) {
    if (!subject) return;
    const dialog = createDialog("backup-history-dialog");
    history.dialog = dialog;
    history.job = subject;
    history.selected = "";
    history.page = 0;
    dialog.innerHTML = `<div class="dialog-head"><div><h2>${esc(subject.name)} — run history</h2><p>Backups, restores and repository operations with their output.</p></div><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div>
      <div class="backup-history-layout"><div class="backup-history-runs"><div class="backup-history-list" data-history-list></div><div class="backup-history-pager" data-history-pager></div></div><section class="backup-history-log"><pre class="backup-output" data-history-output>No run selected.</pre></section></div>`;
    dialog.addEventListener("click", event => {
      if (event.target.closest("[data-close]")) { dialog.close(); return; }
      const pager = event.target.closest("[data-history-page]");
      if (pager && !pager.disabled) { history.page += Number(pager.dataset.historyPage); renderRuns(); return; }
      const item = event.target.closest("[data-run]");
      if (item) selectRun(item.dataset.run);
    });
    dialog.addEventListener("close", () => { dialogs.delete(dialog); dialog.remove(); if (history.dialog === dialog) history.dialog = null; }, { once: true });
    dialog.showModal();
    loadRuns(true);
  }

  // ---- events: the only refresh mechanism (no polling)

  const unsubscribers = [
    runpilot.ws.on(PLUGIN, "backup.changed", () => load()),
    runpilot.ws.on(PLUGIN, "backup.status", view => { if (view?.job?.id) { upsert(view); render(); } }),
    runpilot.ws.on(PLUGIN, "backup.repository.status", view => { if (view?.repository?.id) { upsertRepository(view); render(); } }),
    runpilot.ws.on(PLUGIN, "backup.run.completed", data => {
      if (data && data.success === false && data.message !== "stopped") ui.toast(`${data.name || "Backup"}: ${KIND_LABELS[data.kind] || "run"} failed — ${data.message || "unknown error"}`, "error");
      else if (data?.success && data.kind !== "backup") ui.toast(`${data.name}: ${KIND_LABELS[data.kind] || data.kind} finished.`);
      if (history.dialog?.open && history.job?.id === data?.id) loadRuns(false);
    }),
    runpilot.ws.on(PLUGIN, "history.output", data => {
      if (history.dialog?.open && data?.id === history.selected) loadOutput();
    }),
  ];

  runpilot.navigation.register({
    id: "backup", title: "Backup",
    description: "Back up folders on a schedule, manage repositories and restore snapshots.", icon: { src: new URL("./icon.svg", import.meta.url).href },
    headerActions: root => { headerRoot = root; renderHeader(); },
    render: root => { page = root; render(); load(); },
  });
  load();

  return () => {
    unsubscribers.forEach(off => off());
    dialogs.forEach(dialog => dialog.remove());
    headerRoot = null;
  };
}
