const PLUGIN = "docker";
const enc = value => {
  const bytes = new TextEncoder().encode(String(value));
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
};
const dec = value => Uint8Array.from(atob(value || ""), character => character.charCodeAt(0));

function loadAsset(url, kind) {
  return new Promise((resolve, reject) => {
    const element = document.createElement(kind === "script" ? "script" : "link");
    if (kind === "script") element.src = url;
    else { element.rel = "stylesheet"; element.href = url; }
    element.onload = resolve;
    element.onerror = () => reject(new Error("Docker terminal assets could not be loaded"));
    document.head.append(element);
  });
}
let assetPromise;
function terminalAssets() {
  if (!assetPromise) {
    const base = new URL("./vendor/", import.meta.url);
    assetPromise = (async () => {
      await loadAsset(new URL("xterm.css", base).href, "style");
      await loadAsset(new URL("xterm.js", base).href, "script");
      await loadAsset(new URL("addon-fit.js", base).href, "script");
    })();
  }
  return assetPromise;
}

export async function activate(runpilot) {
  const esc = runpilot.ui.escape;
  let root = null, snapshot = null, error = "", loading = false, modal = null, terminal = null;
  let settingsForm = null;
  const settingsState = {loaded:false,loading:false,saving:false,value:"",usesDefault:false,error:"",status:""};
  let view = "projects";
  const busy = new Set();
  const listeners = [];
  let snapshotRequest = null;
  function loadSnapshot() {
    if (!snapshotRequest) snapshotRequest = rpc("docker.snapshot").finally(() => { snapshotRequest = null; });
    return snapshotRequest;
  }
  async function rpc(method, params = {}) {
    const result = await runpilot.ws.call(PLUGIN, method, params);
    if (result?.error) throw new Error(result.error.message || "Docker request failed");
    return result;
  }
  function button(label, action, key, disabled = false, danger = false, title = "", extraClass = "", primary = false) {
    return `<button type="button" class="button ${primary ? "primary" : danger ? "danger" : "secondary"} small${extraClass ? ` ${esc(extraClass)}` : ""}" data-action="${esc(action)}" data-key="${esc(key)}" ${title ? `title="${esc(title)}"` : ""} ${disabled ? "disabled" : ""}>${esc(label)}</button>`;
  }
  function iconButton(symbol, label, action, key, disabled = false, danger = false, reason = "") {
    return `<button type="button" class="button ${danger ? "danger" : "secondary"} icon" title="${esc(reason || label)}" aria-label="${esc(label)}" data-action="${esc(action)}" data-key="${esc(key)}" ${disabled ? "disabled" : ""}>${symbol}</button>`;
  }
  function containerRow(item, managed, ready) {
    const running = item.state === "running";
    const disabled = !managed || !ready || busy.has(item.id);
    const reason = !managed ? "External Compose containers are read only" : "";
    const dotState = item.tone === "green" ? "running" : item.tone === "yellow" ? "starting" : item.tone === "red" ? "failure" : "";
    return `<div class="docker-plugin-container"><span class="dot ${dotState}" aria-hidden="true"></span><div><strong>${esc(item.service || item.name)}</strong><small>${esc(item.image || "")} · ${esc(item.status || item.state || "unknown")}${item.health ? ` · ${esc(item.health)}` : ""}</small></div><div class="docker-plugin-actions">${managed ? `${iconButton("▶", "Start container", "container-start", item.id, disabled || running, false, reason)}${iconButton("■", "Stop container", "container-stop", item.id, disabled || !running, false, reason)}${iconButton("×", "Delete stopped container", "container-delete", item.id, disabled || running, true, reason)}${iconButton("↪", "Open terminal", "terminal", item.id, disabled || !running, false, reason)}` : ""}${iconButton("▤", "View logs", "logs", item.id, !ready || busy.has(item.id))}</div></div>`;
  }
  function projectCard(item, ready) {
    const managed = !!item.managed, locked = busy.has(item.name);
    const active = ["running", "partial", "degraded"].includes(item.state);
    const readOnlyReason = managed ? "" : "External Compose projects are read only";
    const actions = `<div class="docker-plugin-actions">${managed ? `${button("Up", "project-up", item.name, !managed || !ready || !item.composeFileExists || locked, false, readOnlyReason)}${button("Start", "project-start", item.name, !managed || !ready || !item.composeFileExists || item.state !== "stopped" || locked, false, readOnlyReason)}${button("Stop", "project-stop", item.name, !managed || !ready || !active || locked, false, readOnlyReason)}${button("Down", "project-down", item.name, !managed || !ready || !item.composeFileExists || item.state === "down" || locked, false, readOnlyReason)}${button("compose.yaml", "file-compose", item.name, locked)}${button(".env", "file-env", item.name, locked)}${button("Delete", "project-delete", item.name, !ready || item.state !== "down" || locked, true)}` : ""}</div>${managed ? "" : `<p class="meta docker-plugin-readonly"${item.configPath ? ` title="${esc(item.configPath)}"` : ""}>External project · read only. View logs for its containers.</p>`}`;
    return `<article class="rp-card docker-plugin-card"><div class="docker-plugin-card-head"><h3>${esc(item.name)}</h3><span class="status ${item.state === "running" ? "running" : item.state === "degraded" ? "failure" : "idle"}">${esc(item.state || "unknown")}</span></div>${actions}<div class="docker-plugin-container-list">${(item.containers || []).map(c => containerRow(c, managed, ready)).join("") || '<p class="meta">No containers</p>'}</div></article>`;
  }
  function resourceRow(item, kind, ready) {
    const usage = item.inUse ? (item.runningUse ? "In use by running container" : "Used by stopped container") : "Unused";
    const defaultNetwork = kind === "networks" && ["bridge", "host", "none"].includes(item.name);
    const composeNetwork = kind === "networks" && !!item.composeProject;
    const reason = !ready ? "Docker is unavailable" : item.inUse ? "Resource is in use by a container" : defaultNetwork ? "Default Docker networks cannot be deleted" : composeNetwork ? "Compose-managed networks cannot be deleted directly" : busy.has(item.name) ? "Operation in progress" : "";
    const deleteButton = button("Delete", `${kind}-delete`, item.name, !!reason, true, reason, "docker-plugin-resource-delete");
    return `<div class="docker-plugin-resource"><div><strong>${esc(item.name)}</strong><small>${esc(item.driver || "")} · ${esc(item.scope || "")} · ${esc(usage)}${(item.usedBy || []).length ? ` · ${esc(item.usedBy.join(", "))}` : ""}</small></div><span class="docker-plugin-resource-action"${reason ? ` title="${esc(reason)}"` : ""}>${deleteButton}</span></div>`;
  }
  function render() {
    if (!root) return;
    const ready = !!snapshot?.runtime?.available;
    const list = snapshot?.[view] || [];
    const tabs = [["projects","Projects"],["volumes","Volumes"],["networks","Networks"]].map(([id,title])=>`<button class="button secondary small ${view===id?"active":""}" role="tab" aria-selected="${view===id}" tabindex="${view===id?0:-1}" data-action="view" data-key="${id}">${title}<span class="docker-plugin-count">${snapshot?(snapshot[id]||[]).length:"—"}</span></button>`).join("");
    const content = view === "projects" ? `<div class="docker-plugin-grid docker-plugin-projects rp-masonry">${list.map(item=>projectCard(item,ready)).join("") || `<div class="empty"><h3>${loading&&!snapshot?"Loading projects…":"No Compose projects"}</h3><p>Create a managed project to define its Compose services.</p></div>`}</div>` : `<div class="rp-card docker-plugin-card docker-plugin-resource-panel">${list.map(item=>resourceRow(item,view,ready)).join("") || `<div class="empty"><h3>No ${view} found</h3><p>Resources appear when they are available from Docker.</p></div>`}</div>`;
    const create = view === "projects" ? button("Create project","project-create","",!ready,false,"","",true) : button(view === "volumes" ? "Create volume" : "Create network",`${view}-create`,"",!ready,false,"","",true);
    root.innerHTML = `<div class="docker-plugin">${error?`<div class="notice" role="alert">${esc(error)}</div>`:""}${!ready?`<div class="notice" role="status"><strong>${loading&&!snapshot?"Checking Docker":"Docker unavailable"}</strong>${esc(snapshot?.runtime?.message||(loading?"Connecting to Engine and Compose…":"Docker status has not been checked."))}${snapshot?.runtime?.identity?` Running as ${esc(snapshot.runtime.identity)}.`:""}${snapshot?.runtime?.diagnostic?`<p class="meta docker-plugin-diagnostic">${esc(snapshot.runtime.diagnostic)}</p>`:""}</div>`:""}<div class="docker-plugin-viewbar"><div class="docker-plugin-tabs rp-segmented" role="tablist" aria-label="Docker resources">${tabs}</div>${create}</div><section role="tabpanel" aria-label="${view}">${content}</section></div>`;
  }
  const tabsKey = event => {
    const tab = event.target.closest?.('[data-action="view"]');
    if (!tab || !["ArrowLeft","ArrowRight","Home","End"].includes(event.key)) return;
    event.preventDefault(); const ids=["projects","volumes","networks"], index=ids.indexOf(tab.dataset.key);
    const next=event.key==="Home"?ids[0]:event.key==="End"?ids[2]:ids[(index+(event.key==="ArrowRight"?1:2))%3];
    void action("view",next);root.querySelector?.(`[data-action="view"][data-key="${next}"]`)?.focus();
  };
  async function refresh() {
    if (loading) return;
    loading = true; render();
    try { snapshot = await loadSnapshot(); error = ""; }
    catch (cause) { error = cause.message; }
    finally { loading = false; render(); }
  }
  function closeModal() { if (modal) { modal.close(); modal.remove(); modal = null; } }
  function dialog(title, content, onSubmit, submitLabel = "Save") {
    closeModal();
    const node = document.createElement("dialog"); node.className = "dialog docker-plugin-dialog";
    node.innerHTML = `<form method="dialog"><div class="dialog-head"><h2>${esc(title)}</h2><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div>${content}<p class="form-error hidden" role="alert"></p><div class="dialog-actions"><button class="button secondary" type="button" data-close>Cancel</button><button class="button primary" type="submit">${esc(submitLabel)}</button></div></form>`;
    document.body.append(node); modal = node;
    node.querySelectorAll("[data-close]").forEach(b => b.addEventListener("click", closeModal));
    node.addEventListener("close", () => { if (modal === node) modal = null; node.remove(); });
    node.querySelector("form").addEventListener("submit", async event => {
      event.preventDefault();
      const form = event.currentTarget, submit = form.querySelector('[type="submit"]'); submit.disabled = true;
      try { await onSubmit(form); closeModal(); await refresh(); }
      catch (cause) { const output = form.querySelector(".form-error"); output.textContent = cause.message; output.classList.remove("hidden"); submit.disabled = false; }
    });
    node.showModal(); node.querySelector("input,textarea")?.focus();
    return node;
  }
  function create(kind) {
    const label = {project: "Compose project", volumes: "Docker volume", networks: "Docker network"}[kind];
    dialog(`Create ${label}`, `<label>Name<input name="name" required maxlength="128" autocomplete="off" placeholder="${kind === "project" ? "jellyfin" : "myapp_data"}"></label>`, async form => {
      const name = form.elements.name.value.trim();
      await rpc(kind === "project" ? "docker.projects.create" : `docker.${kind}.create`, {name});
      runpilot.ui.toast(`${label} created`);
    }, "Create");
  }
  async function editFile(name, kind) {
    const {content, file} = await rpc("docker.projects.file.get", {name, kind});
    const filename = file || (kind === "compose" ? "compose.yaml" : ".env");
    dialog(`Edit ${filename} · ${name}`, `<label class="docker-plugin-editor-label">${esc(name)}/${esc(filename)}<textarea name="content" class="docker-plugin-editor" spellcheck="false" maxlength="1048576"></textarea></label>`, async form => { await rpc("docker.projects.file.set", {name, kind, content: form.elements.content.value}); runpilot.ui.toast("File saved"); });
    modal.querySelector("textarea").value = content || (kind === "compose" ? "services: {}\n" : "");
  }
  function showLogs(content) { dialog("Container logs", `<pre class="docker-plugin-log"></pre>`, async () => {}, "Close"); modal.querySelector("pre").textContent = content || "No log output."; }
  function renderSettingsForm() {
    if (!settingsForm) return;
    const disabled = !settingsState.loaded || settingsState.saving;
    settingsForm.innerHTML = `<div class="plugin-setting-info"><strong>Docker settings</strong><div class="meta">Choose the folder used for managed Compose projects.</div></div>${settingsState.error ? `<div class="form-error" role="alert">${esc(settingsState.error)}</div>` : ""}${settingsState.status ? `<div class="notice" role="status">${esc(settingsState.status)}</div>` : ""}<label>Compose projects folder<input name="projectsFolder" type="text" required maxlength="4096" value="${esc(settingsState.value)}" placeholder="/absolute/path/to/compose-projects" autocomplete="off" spellcheck="false" ${disabled ? "disabled" : ""}></label><p class="meta">${settingsState.usesDefault ? "Using the default RunPilot plugin workspace folder. " : ""}Enter an absolute path anywhere on this system. The RunPilot service account must have access. Existing files stay in their current folder.</p>${!settingsState.loaded && !settingsState.loading ? '<button class="button secondary small" type="button" data-settings-retry>Retry loading settings</button>' : ""}<div class="plugin-settings-actions"><button class="button primary small" type="submit" ${disabled ? "disabled" : ""}>${settingsState.saving ? "Saving…" : "Save"}</button></div>`;
    settingsForm.setAttribute("aria-busy", settingsState.loading || settingsState.saving ? "true" : "false");
    settingsForm.elements.projectsFolder.addEventListener("input", event => { settingsState.value = event.target.value; });
    settingsForm.querySelector("[data-settings-retry]")?.addEventListener("click", loadSettings);
  }
  async function loadSettings() {
    if (settingsState.loading) return;
    settingsState.loading = true; settingsState.error = ""; renderSettingsForm();
    try {
      const value = await rpc("docker.settings.get");
      settingsState.value = value.projectsFolder || ""; settingsState.usesDefault = !!value.usesDefault; settingsState.loaded = true;
    } catch (cause) { settingsState.error = cause.message; }
    finally { settingsState.loading = false; renderSettingsForm(); }
  }
  function dockerSettingsForm() {
    settingsForm = document.createElement("form");
    settingsForm.className = "plugin-settings-form docker-plugin-settings";
    settingsForm.addEventListener("submit", async event => {
      event.preventDefault();
      if (!settingsState.loaded || settingsState.saving) return;
      settingsState.value = settingsForm.elements.projectsFolder.value.trim(); settingsState.saving = true; settingsState.error = ""; settingsState.status = ""; renderSettingsForm();
      try {
        await rpc("docker.settings.set", {projectsFolder:settingsState.value});
        settingsState.status = "Docker settings saved."; settingsState.usesDefault = !settingsState.value;
        runpilot.ui.toast("Docker settings saved");
        runpilot.settings.close?.();
      } catch (cause) { settingsState.error = cause.message; }
      finally { settingsState.saving = false; renderSettingsForm(); }
    });
    renderSettingsForm();
    if (!settingsState.loaded && !settingsState.loading) void loadSettings();
    return settingsForm;
  }
  async function openTerminal(id) {
    await terminalAssets();
    closeModal();
    const node = document.createElement("dialog"); node.className = "dialog docker-plugin-terminal-dialog";
    node.innerHTML = `<div class="dialog-head"><h2>Container terminal</h2><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div><div class="docker-plugin-terminal" data-rp-interactive></div>`;
    document.body.append(node); modal = node; node.showModal();
    const term = new window.Terminal({cursorBlink: true, convertEol: true});
    const fit = new window.FitAddon.FitAddon(); term.loadAddon(fit); term.open(node.querySelector(".docker-plugin-terminal")); fit.fit();
    const item = {node, term, fit, id: "", observer: null}; terminal = item;
    const dispose = () => { if (terminal !== item) return; terminal = null; item.observer?.disconnect(); if (item.id) void rpc("docker.containers.terminal.close", {id:item.id,force:true}).catch(() => {}); term.dispose(); closeModal(); };
    node.querySelector("[data-close]").addEventListener("click", dispose); node.addEventListener("close", dispose);
    try {
      const result = await rpc("docker.containers.terminal.open", {id, rows:term.rows, columns:term.cols});
      if (terminal !== item) { void rpc("docker.containers.terminal.close", {id:result.id,force:true}); return; }
      item.id = result.id;
      term.onData(data => { if (item.id) void rpc("docker.containers.terminal.write", {id:item.id,data:enc(data)}).catch(cause => term.writeln(`\r\n${cause.message}`)); });
      item.observer = new ResizeObserver(() => { fit.fit(); if (item.id) void rpc("docker.containers.terminal.resize", {id:item.id,rows:term.rows,columns:term.cols}).catch(() => {}); });
      item.observer.observe(node.querySelector(".docker-plugin-terminal"));
    } catch (cause) { term.writeln(`\r\n${cause.message}`); }
  }
  async function action(type, key) {
    if (type === "view") { if (["projects","volumes","networks"].includes(key)) { view=key;render(); } return; }
    if (type.endsWith("-create")) return create(type.split("-")[0]);
    if (type.startsWith("file-")) return editFile(key, type.slice(5));
    if (type === "logs") { const result = await rpc("docker.containers.logs", {id:key}); showLogs(result.content); return; }
    if (type === "terminal") return openTerminal(key);
    const [kind, operation] = type.split("-");
    if (operation === "delete") {
      const message = kind === "project" ? `Delete managed project ${key} and its files?` : kind === "volumes" ? `Permanently delete Docker volume ${key} and its data?` : kind === "networks" ? `Delete Docker network ${key}?` : `Delete stopped container ${key}?`;
      if (!await (runpilot.ui.confirm ? runpilot.ui.confirm({title:"Confirm deletion",message,confirmLabel:"Delete",danger:true}) : window.confirm(message))) return;
    }
    const method = kind === "project" ? (operation === "delete" ? "docker.projects.delete" : "docker.projects.action") : kind === "container" ? "docker.containers.action" : `docker.${kind}.${operation}`;
    const params = kind === "project" ? {name:key,action:operation} : kind === "container" ? {id:key,action:operation} : {name:key};
    busy.add(key); render();
    try { await rpc(method, params); runpilot.ui.toast("Docker operation completed"); }
    finally { busy.delete(key); await refresh(); }
  }
  const click = event => {
    const target = event.target.closest("[data-action]"); if (!target || !root?.contains(target)) return;
    void action(target.dataset.action, target.dataset.key || "").catch(cause => { error = cause.message; runpilot.ui.toast(cause.message); render(); });
  };
  runpilot.navigation.register({id:"docker",title:"Docker", shortcut:{title:"Open Docker",description:"Containers and Compose",order:20},description:"Manage Compose projects, containers, volumes, and networks.",icon:{src:new URL("./icon.svg",import.meta.url).href},render:page => {root = page;root.removeEventListener("click",click);root.addEventListener("click",click);root.removeEventListener("keydown",tabsKey);root.addEventListener("keydown",tabsKey);render();void refresh();}});
  runpilot.settings.register({id:"docker",render:dockerSettingsForm});
  const refreshTimer = setInterval(() => { if (!document.hidden && root?.isConnected && root.closest(".page")?.classList.contains("active")) void refresh(); }, 30000);
  listeners.push(runpilot.ws.on(PLUGIN,"process.session.output",event => { if (terminal?.id === event?.id) terminal.term.write(dec(event.data)); }));
  listeners.push(runpilot.ws.on(PLUGIN,"process.session.error",event => { if (terminal?.id === event?.id) terminal.term.writeln(`\r\n${event.message || "Terminal I/O error"}`); }));
  listeners.push(runpilot.ws.on(PLUGIN,"process.session.exit",event => { if (terminal?.id === event?.id) {terminal.term.writeln(`\r\n[session ${event.reason || "exited"}]`);terminal.id = "";} }));
  return () => { clearInterval(refreshTimer);listeners.forEach(off => off());root?.removeEventListener("click",click);root?.removeEventListener("keydown",tabsKey);root = null;settingsForm = null;if (terminal) {const id=terminal.id;terminal.observer?.disconnect();terminal.term.dispose();terminal=null;if(id) void rpc("docker.containers.terminal.close",{id,force:true}).catch(()=>{});}closeModal(); };
}
