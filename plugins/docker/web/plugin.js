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
  const busy = new Set();
  const listeners = [];
  async function rpc(method, params = {}) {
    const result = await runpilot.ws.call(PLUGIN, method, params);
    if (result?.error) throw new Error(result.error.message || "Docker request failed");
    return result;
  }
  function button(label, action, key, disabled = false, danger = false, title = "", extraClass = "") {
    return `<button type="button" class="button ${danger ? "danger" : "secondary"} small${extraClass ? ` ${esc(extraClass)}` : ""}" data-action="${esc(action)}" data-key="${esc(key)}" ${title ? `title="${esc(title)}"` : ""} ${disabled ? "disabled" : ""}>${esc(label)}</button>`;
  }
  function iconButton(symbol, label, action, key, disabled = false, danger = false, reason = "") {
    return `<button type="button" class="button ${danger ? "danger" : "secondary"} small docker-plugin-icon-button" title="${esc(reason || label)}" aria-label="${esc(label)}" data-action="${esc(action)}" data-key="${esc(key)}" ${disabled ? "disabled" : ""}>${symbol}</button>`;
  }
  function containerRow(item, managed, ready) {
    const running = item.state === "running";
    const disabled = !managed || !ready || busy.has(item.id);
    const reason = !managed ? "External Compose containers are read only" : "";
    return `<div class="docker-plugin-container"><span class="docker-plugin-dot ${esc(item.tone || "gray")}"></span><div><strong>${esc(item.service || item.name)}</strong><small>${esc(item.image || "")} · ${esc(item.status || item.state || "unknown")}${item.health ? ` · ${esc(item.health)}` : ""}</small></div><div class="docker-plugin-actions">${iconButton("▶", "Start container", "container-start", item.id, disabled || running, false, reason)}${iconButton("■", "Stop container", "container-stop", item.id, disabled || !running, false, reason)}${iconButton("🗑", "Delete stopped container", "container-delete", item.id, disabled || running, true, reason)}${iconButton("↪", "Open terminal", "terminal", item.id, disabled || !running, false, reason)}${iconButton("▤", "View logs", "logs", item.id, !ready || busy.has(item.id))}</div></div>`;
  }
  function projectCard(item, ready) {
    const managed = !!item.managed, locked = busy.has(item.name);
    const active = ["running", "partial", "degraded"].includes(item.state);
    const readOnlyReason = managed ? "" : "External Compose projects are read only";
    const actions = `<div class="docker-plugin-actions">${button("Up", "project-up", item.name, !managed || !ready || !item.composeFileExists || locked, false, readOnlyReason)}${button("Start", "project-start", item.name, !managed || !ready || !item.composeFileExists || item.state !== "stopped" || locked, false, readOnlyReason)}${button("Stop", "project-stop", item.name, !managed || !ready || !active || locked, false, readOnlyReason)}${button("Down", "project-down", item.name, !managed || !ready || !item.composeFileExists || item.state === "down" || locked, false, readOnlyReason)}${managed ? `${button("compose.yaml", "file-compose", item.name, locked)}${button(".env", "file-env", item.name, locked)}${button("Delete", "project-delete", item.name, !ready || item.state !== "down" || locked, true)}` : ""}</div>${managed ? "" : `<p class="meta docker-plugin-readonly">External project · read only${item.configPath ? ` · ${esc(item.configPath)}` : ""}. Lifecycle controls require a RunPilot-managed project.</p>`}`;
    return `<article class="docker-plugin-card"><div class="docker-plugin-card-head"><h3>${esc(item.name)}</h3><span class="status ${item.state === "running" ? "running" : item.state === "degraded" ? "failure" : "idle"}">${esc(item.state || "unknown")}</span></div>${actions}<div class="docker-plugin-container-list">${(item.containers || []).map(c => containerRow(c, managed, ready)).join("") || '<p class="meta">No containers</p>'}</div></article>`;
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
    root.innerHTML = `<div class="docker-plugin"><div class="docker-plugin-toolbar"><div><h2>Docker</h2><p class="meta">Compose projects and Docker resources</p></div><div class="docker-plugin-actions">${button("Refresh", "refresh", "")}${button("Create project", "project-create", "", !ready)}${button("Create volume", "volumes-create", "", !ready)}${button("Create network", "networks-create", "", !ready)}</div></div>${error ? `<div class="notice" role="alert">${esc(error)}</div>` : ""}${!ready ? `<div class="notice" role="status"><strong>Docker unavailable</strong> ${esc(snapshot?.runtime?.message || (loading ? "Checking Docker…" : "Docker status has not been checked."))}${snapshot?.runtime?.identity ? ` Running as ${esc(snapshot.runtime.identity)}.` : ""}</div>` : ""}<section><h3>Compose projects</h3><div class="docker-plugin-grid docker-plugin-projects rp-masonry">${(snapshot?.projects || []).map(item => projectCard(item, ready)).join("") || '<p class="empty">No Compose projects found.</p>'}</div></section><div class="docker-plugin-grid docker-plugin-resources"><section><h3>Volumes</h3><div class="docker-plugin-card">${(snapshot?.volumes || []).map(v => resourceRow(v, "volumes", ready)).join("") || '<p class="meta">No Docker volumes found.</p>'}</div></section><section><h3>Networks</h3><div class="docker-plugin-card">${(snapshot?.networks || []).map(n => resourceRow(n, "networks", ready)).join("") || '<p class="meta">No Docker networks found.</p>'}</div></section></div></div>`;
  }
  async function refresh() {
    if (loading) return;
    loading = true; render();
    try { snapshot = await rpc("docker.snapshot"); error = ""; }
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
  async function openTerminal(id) {
    await terminalAssets();
    closeModal();
    const node = document.createElement("dialog"); node.className = "dialog docker-plugin-terminal-dialog";
    node.innerHTML = `<div class="dialog-head"><h2>Container terminal</h2><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div><div class="docker-plugin-terminal"></div>`;
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
    if (type === "refresh") return refresh();
    if (type.endsWith("-create")) return create(type.split("-")[0]);
    if (type.startsWith("file-")) return editFile(key, type.slice(5));
    if (type === "logs") { const result = await rpc("docker.containers.logs", {id:key}); showLogs(result.content); return; }
    if (type === "terminal") return openTerminal(key);
    const [kind, operation] = type.split("-");
    if (operation === "delete") {
      const message = kind === "project" ? `Delete managed project ${key} and its files?` : kind === "volumes" ? `Permanently delete Docker volume ${key} and its data?` : kind === "networks" ? `Delete Docker network ${key}?` : `Delete stopped container ${key}?`;
      if (!window.confirm(message)) return;
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
  runpilot.navigation.register({id:"docker",title:"Docker",icon:{src:new URL("./icon.svg",import.meta.url).href},render:page => {root = page;root.removeEventListener("click",click);root.addEventListener("click",click);render();void refresh();}});
  listeners.push(runpilot.ws.on(PLUGIN,"process.session.output",event => { if (terminal?.id === event?.id) terminal.term.write(dec(event.data)); }));
  listeners.push(runpilot.ws.on(PLUGIN,"process.session.error",event => { if (terminal?.id === event?.id) terminal.term.writeln(`\r\n${event.message || "Terminal I/O error"}`); }));
  listeners.push(runpilot.ws.on(PLUGIN,"process.session.exit",event => { if (terminal?.id === event?.id) {terminal.term.writeln(`\r\n[session ${event.reason || "exited"}]`);terminal.id = "";} }));
  return () => { listeners.forEach(off => off());root?.removeEventListener("click",click);root = null;if (terminal) {const id=terminal.id;terminal.observer?.disconnect();terminal.term.dispose();terminal=null;if(id) void rpc("docker.containers.terminal.close",{id,force:true}).catch(()=>{});}closeModal(); };
}
