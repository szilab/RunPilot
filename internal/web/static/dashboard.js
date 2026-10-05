(function (global) {
  const el = (tag, className = "", text = "") => { const node = document.createElement(tag); node.className = className; node.textContent = text; return node; };
  const bytes = value => value > 0 ? `${(value / 1073741824).toFixed(1)} GiB` : "Unavailable";
  const duration = seconds => {
    if (!seconds) return "Unavailable";
    const days = Math.floor(seconds / 86400), hours = Math.floor(seconds % 86400 / 3600), minutes = Math.floor(seconds % 3600 / 60);
    return days ? `${days}d ${hours}h` : hours ? `${hours}h ${minutes}m` : `${minutes}m`;
  };
  const percent = (used, total) => total > 0 ? Math.max(0, Math.min(100, Math.round(used * 100 / total))) : 0;
  function fact(root, label, value) { const row = el("div", "overview-fact"); row.append(el("span", "", label), el("strong", "", String(value))); root.append(row); }
  function meter(root, label, used, total) {
    const line = el("div", "overview-meter-label"); line.append(el("span", "", label), el("strong", "", total > 0 ? `${bytes(used)} / ${bytes(total)} · ${percent(used,total)}%` : "Unavailable"));
    const track = el("div", "overview-meter"); const fill = el("span"); fill.style.width = `${percent(used,total)}%`; track.append(fill); root.append(line, track);
  }
  function validURL(value, mode) {
    if (mode === "runpilot") return /^\/p\/[^\\\r\n]*$/.test(value);
    try { const url = new URL(value); return ["http:","https:"].includes(url.protocol) && !!url.hostname && !url.username && !url.password; } catch { return false; }
  }
  function register(widgets, api, navigate, toast) {
    let statusValue = null, statusAt = 0, statusPromise = null;
    const status = async () => {
      if (statusValue && Date.now() - statusAt < 15000) return statusValue;
      if (!statusPromise) statusPromise = api("api/v1/dashboard/status").then(value => { statusValue = value; statusAt = Date.now(); return value; }).finally(() => { statusPromise = null; });
      return statusPromise;
    };
    let systemBody, runpilotBody, launcherBody, entries = [], launcherLoaded = false;
    async function drawSystem(body) {
      const data = await status(), host = data.host || {};
      if (!body.isConnected) return;
      body.replaceChildren();
      fact(body, "Host", host.hostname || "Unavailable");
      fact(body, "Platform", [host.os, host.architecture].filter(Boolean).join(" · ") || "Unavailable");
      fact(body, "Host uptime", duration(data.hostUptimeSeconds));
      fact(body, "CPU", Number.isFinite(host.cpuPercent) ? `${Math.round(host.cpuPercent)}%` : "Unavailable");
      if (host.gpuAvailable) fact(body, "GPU", `${Math.round(host.gpuPercent)}%`);
      const total = host.memoryTotalBytes || 0;
      meter(body, "Memory", Math.max(0, total - (host.memoryFreeBytes || 0)), total);
      const disks = host.disks || [];
      const diskTotal = disks.reduce((sum, disk) => sum + (disk.totalBytes || 0), 0);
      const diskFree = disks.reduce((sum, disk) => sum + (disk.freeBytes || 0), 0);
      meter(body, "Disks", diskTotal - diskFree, diskTotal);
      for (const disk of disks) meter(body, disk.path, (disk.totalBytes || 0) - (disk.freeBytes || 0), disk.totalBytes || 0);
      if (host.error) body.append(el("p", "overview-widget-note", host.error));
    }
    async function drawRunPilot(body) {
      const data = await status(); if (!body.isConnected) return;
      body.replaceChildren();
      fact(body, "Version", data.version || "Development"); fact(body, "Uptime", duration(data.uptimeSeconds));
      const row = el("div", "overview-stat-row");
      for (const [label, key] of [["Installed", "installedPlugins"],["Enabled","enabledPlugins"],["Loaded","loadedPlugins"]]) {
        const cell = el("div", "overview-stat"); cell.append(el("strong", "", data[key] ?? 0), el("span", "", label)); row.append(cell);
      }
      body.append(row);
      const settings = el("button", "button secondary small", "Manage plugins"); settings.type = "button"; settings.onclick = () => navigate("settings"); body.append(settings);
    }
    widgets.register("core", {id:"core.system",title:"System",size:"wide",order:10,mount(body){systemBody=body;return drawSystem(body);},refresh(){return systemBody && drawSystem(systemBody);},dispose(){systemBody=null;}});
    widgets.register("core", {id:"core.runpilot",title:"RunPilot",size:"medium",order:20,mount(body){runpilotBody=body;return drawRunPilot(body);},refresh(){return runpilotBody && drawRunPilot(runpilotBody);},dispose(){runpilotBody=null;}});
    async function loadLauncher() { const result = await api("api/v1/launcher"); entries = result.entries || []; launcherLoaded = true; if (launcherBody?.isConnected) drawLauncher(launcherBody); }
    async function saveLauncher(next) { const result = await api("api/v1/launcher", {method:"PUT",body:JSON.stringify({entries:next})}); entries = result.entries || []; drawLauncher(launcherBody); }
    function editor(existing = null) {
      const dialog = el("dialog", "dialog overview-launcher-dialog");
      const form = el("form"); form.noValidate = true;
      const heading = el("h2", "", existing ? "Edit shortcut" : "Add shortcut"); form.append(heading);
      const field = (label, input) => { const wrapper = el("label"); wrapper.append(el("span", "", label), input); form.append(wrapper); return input; };
      const name = field("Name", el("input")); name.value = existing?.name || ""; name.maxLength = 100; name.required = true;
      const url = field("URL or /p/ path", el("input")); url.value = existing?.url || ""; url.maxLength = 2048; url.required = true;
      const icon = field("Icon URL (optional)", el("input")); icon.value = existing?.icon || ""; icon.maxLength = 2048;
      const mode = field("Open", el("select")); for (const [value,label] of [["external","External tab"],["runpilot","Inside RunPilot (/p/ only)"]]) { const option = el("option", "", label); option.value = value; mode.append(option); } mode.value = existing?.openMode || "external";
      const hint = el("p", "overview-widget-note", "Inside RunPilot accepts an existing /p/ proxy path. Embedded apps may restrict framing or require their own session."); form.append(hint);
      const error = el("p", "overview-widget-error"); error.setAttribute("role","alert"); form.append(error);
      const actions = el("div", "dialog-actions"); const cancel = el("button", "button secondary", "Cancel"); cancel.type = "button"; cancel.onclick = () => dialog.close(); const submit = el("button", "button primary", "Save"); submit.type = "submit"; actions.append(cancel, submit); form.append(actions);
      form.onsubmit = async event => {
        event.preventDefault(); const entry = {id:existing?.id || "",name:name.value.trim(),url:url.value.trim(),icon:icon.value.trim(),openMode:mode.value};
        if (!entry.name || !validURL(entry.url,entry.openMode) || (entry.icon && !validURL(entry.icon,"external"))) { error.textContent = "Enter a name and valid HTTP(S) URL, or a /p/ path for Inside RunPilot."; return; }
        submit.disabled = true;
        try { await saveLauncher(existing ? entries.map(item => item.id === existing.id ? entry : item) : [...entries, entry]); dialog.close(); }
        catch (cause) { error.textContent = cause.message; submit.disabled = false; }
      };
      dialog.append(form); dialog.addEventListener("close", () => dialog.remove(), {once:true}); document.body.append(dialog); dialog.showModal(); name.focus();
    }
    function drawLauncher(body) {
      if (!body) return; body.replaceChildren();
      const toolbar = el("div", "overview-launcher-toolbar"); const add = el("button", "button primary small", "Add shortcut"); add.type = "button"; add.onclick = () => editor(); toolbar.append(add); body.append(toolbar);
      if (!launcherLoaded) { body.append(el("p", "overview-widget-note", "Loading shortcuts…")); return; }
      if (!entries.length) { body.append(el("p", "overview-widget-note", "Pin a web page or proxied application here.")); return; }
      const grid = el("div", "overview-launcher-grid");
      entries.forEach((item,index) => {
        const card = el("div", "overview-launcher-item");
        const launch = el("button", "overview-launcher-open"); launch.type = "button"; launch.title = item.url;
        const icon = el("span", "overview-launcher-icon", "▦");
        if (item.icon && validURL(item.icon,"external")) { const image = el("img"); image.src = item.icon; image.alt = ""; image.referrerPolicy = "no-referrer"; image.onerror = () => image.replaceWith(el("span", "", "▦")); icon.replaceChildren(image); }
        launch.append(icon, el("strong", "", item.name), el("small", "", item.openMode === "runpilot" ? "Inside RunPilot" : "External tab"));
        launch.onclick = () => { if (item.openMode === "runpilot" && validURL(item.url,"runpilot")) global.RunPilotOpenEmbedded(item); else if (validURL(item.url,"external")) window.open(item.url,"_blank","noopener,noreferrer"); else toast("Invalid shortcut URL"); };
        const controls = el("div", "overview-launcher-controls");
        const button = (label, action, disabled = false) => { const node = el("button", "button secondary small", label); node.type="button"; node.disabled=disabled; node.setAttribute("aria-label",`${label} ${item.name}`); node.onclick=action; controls.append(node); };
        button("↑", () => { const next=[...entries]; [next[index-1],next[index]]=[next[index],next[index-1]]; saveLauncher(next).catch(cause=>toast(cause.message)); }, index===0);
        button("↓", () => { const next=[...entries]; [next[index+1],next[index]]=[next[index],next[index+1]]; saveLauncher(next).catch(cause=>toast(cause.message)); }, index===entries.length-1);
        button("Edit", () => editor(item)); button("Delete", () => { if (confirm(`Delete shortcut “${item.name}”?`)) saveLauncher(entries.filter(entry=>entry.id!==item.id)).catch(cause=>toast(cause.message)); });
        card.append(launch, controls); grid.append(card);
      });
      body.append(grid);
    }
    widgets.register("core", {id:"core.launcher",title:"Launcher",size:"wide",order:50,mount(body){launcherBody=body;drawLauncher(body);return loadLauncher();},refresh(){return loadLauncher();},dispose(){launcherBody=null;}});
  }
  global.RunPilotDashboard = {register, validURL};
  if (typeof module !== "undefined") module.exports = global.RunPilotDashboard;
})(typeof window === "undefined" ? globalThis : window);
