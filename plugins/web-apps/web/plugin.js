export const publicPrefix = (base, mount) => String(base || "/").replace(/\/$/, "") + mount + "/";
export function launchGateway(opened, opener = window.open.bind(window)) {
  const launch = { runtime: opened.session.runtimeId, publication: opened.session.publicationId, stream: opened.session.id, ticket: opened.ticket };
  if (!Object.values(launch).every(value => typeof value === "string" && /^[A-Za-z0-9_-]{32}$/.test(value))) throw new Error("Invalid browser publication launch");
  const base = new URL(opened.session.baseURL, location.origin);
  if (base.origin !== location.origin || base.search || base.hash || !base.pathname.endsWith("/")) throw new Error("Invalid RunPilot base URL");
  const params = new URLSearchParams({ "runpilot-publication": JSON.stringify(launch) });
  opener(base.pathname + "#" + params, "_blank", "noopener,noreferrer");
}

async function removeWorker(prefix) {
  try {
    const registration = await navigator.serviceWorker?.getRegistration(new URL(prefix, location.origin).href);
    if (registration && new URL(registration.scope).pathname === prefix && new URL(registration.active?.scriptURL || registration.waiting?.scriptURL).pathname === prefix + "__runpilot__/sw.js") await registration.unregister();
  } catch { /* Stale workers still fail closed after host-side publication removal. */ }
}

export async function activate(runpilot) {
  const call = (method, params = {}) => runpilot.ws.call("web.apps", method, params);
  const escape = runpilot.ui.escape;
  const base = new URL(document.baseURI).pathname.replace(/\/$/, "");
  let page, targets = [], sessions = [], message = "", disposed = false;
  function edit(existing = {}) {
    const dialog = document.createElement("dialog"); dialog.className = "dialog webapps-dialog";
    const field = (label, name, fallback = "") => `<label><span>${label}</span><input name="${name}" value="${escape(existing[name] || fallback)}" maxlength="${name === "name" ? 100 : name === "upstreamURL" ? 2048 : 1024}" required></label>`;
    const customHeaders = Object.entries(existing.customHeaders || {}).map(([key, value]) => `${key}: ${value}`).join("\n");
    dialog.innerHTML = `<form method="dialog"><div class="dialog-head"><h2>${existing.id ? "Edit Web App" : "Add Web App"}</h2></div>${field("Name", "name")}${field("Mount path", "mountPath", "/app")}${field("Upstream HTTP(S) origin", "upstreamURL", "http://127.0.0.1:8080")}${field("Upstream base path", "upstreamBasePath", "/")}<details class="webapps-advanced"><summary>Advanced Settings</summary><div class="webapps-advanced-content"><label><span>Base path header</span><select name="basePathHeader"><option value="">None</option><option value="X-Forwarded-Prefix">X-Forwarded-Prefix</option><option value="X-Script-Name">X-Script-Name</option></select></label><label class="webapps-base-header-value" hidden><span>Header value <small>(leave blank to use the public mount path)</small></span><input name="basePathHeaderValue" maxlength="1024" value="${escape(existing.basePathHeaderValue || "")}" autocomplete="off"></label><label class="webapps-toggle"><input type="checkbox" name="forwardPublicHost"><span>Forward public host</span></label><label class="webapps-toggle"><input type="checkbox" name="forwardPublicScheme"><span>Forward public scheme</span></label><label class="webapps-tls-toggle"><span>Ignore upstream TLS certificate validation<small>Enable only for trusted devices with self-signed certificates. The connection stays encrypted, but the server identity will not be verified.</small></span><input type="checkbox" role="switch" name="insecureSkipVerify"></label><label><span>Static headers (one Name: value per line; values are editable; not for secrets)</span><textarea name="customHeaders" rows="4" maxlength="8192">${escape(customHeaders)}</textarea></label></div></details><p>Relative URLs and an application's own base URL setting are the most reliable ways to serve it below a nested path. JavaScript access to server cookies is not supported.</p><p data-error role="alert"></p><div class="dialog-actions"><button type="button" class="button secondary" data-cancel>Cancel</button><button type="submit" class="button primary">Save</button></div></form>`;
    dialog.querySelector('[name="basePathHeader"]').value = existing.basePathHeader || "";
    const baseHeaderValue = dialog.querySelector(".webapps-base-header-value");
    const syncBaseHeaderValue = () => { baseHeaderValue.hidden = !dialog.querySelector('[name="basePathHeader"]').value; };
    dialog.querySelector('[name="basePathHeader"]').addEventListener("change", syncBaseHeaderValue);
    syncBaseHeaderValue();
    dialog.querySelector('[name="forwardPublicHost"]').checked = !!existing.forwardPublicHost;
    dialog.querySelector('[name="forwardPublicScheme"]').checked = !!existing.forwardPublicScheme;
    dialog.querySelector('[name="insecureSkipVerify"]').checked = !!existing.insecureSkipVerify;
    dialog.querySelector("[data-cancel]").onclick = () => dialog.close();
    dialog.querySelector("form").onsubmit = async event => {
      event.preventDefault(); const form = event.target, button = form.querySelector('[type="submit"]'); button.disabled = true;
      const target = { id: existing.id || "" }; for (const key of ["name", "mountPath", "upstreamURL", "upstreamBasePath"]) target[key] = form.elements[key].value;
      if (form.elements.basePathHeader.value) {
        target.basePathHeader = form.elements.basePathHeader.value;
        if (form.elements.basePathHeaderValue.value.trim()) target.basePathHeaderValue = form.elements.basePathHeaderValue.value.trim();
      }
      if (form.elements.forwardPublicHost.checked) target.forwardPublicHost = true;
      if (form.elements.forwardPublicScheme.checked) target.forwardPublicScheme = true;
      if (form.elements.insecureSkipVerify.checked) target.insecureSkipVerify = true;
      target.customHeaders = {};
      for (const line of form.elements.customHeaders.value.split(/\r?\n/).filter(line => line.trim())) {
        const split = line.indexOf(":"); if (split < 1) { button.disabled = false; dialog.querySelector("[data-error]").textContent = "Custom headers must use Name: value format"; return; }
        target.customHeaders[line.slice(0, split).trim()] = line.slice(split + 1).trim();
      }
      if (!Object.keys(target.customHeaders).length) delete target.customHeaders;
      try { await call("apps.targets.save", { target }); if (existing.mountPath && existing.mountPath !== target.mountPath) await removeWorker(publicPrefix(base, existing.mountPath)); dialog.close(); await load(); }
      catch (error) { dialog.querySelector("[data-error]").textContent = error.message; button.disabled = false; }
    };
    dialog.addEventListener("close", () => dialog.remove(), { once: true }); document.body.append(dialog); dialog.showModal();
  }
  async function action(work) { try { message = ""; await work(); await load(); } catch (error) { message = error.message; render(); } }
  function render() {
    if (!page || disposed) return; page.replaceChildren();
    const root = document.createElement("div"); root.className = "webapps-list";
    if (message) { const notice = document.createElement("p"); notice.setAttribute("role", "alert"); notice.textContent = message; root.append(notice); }
    if (!targets.length) root.append(runpilot.ui.EmptyState({ title: "No Web Apps", message: "Add a host-reachable HTTP application to get started." }));
    for (const target of targets) {
      const card = document.createElement("article"); card.className = "docker-card remote-card webapps-card";
      card.innerHTML = `<div class="docker-card-head"><div class="webapps-facts"><h2>${escape(target.name)}</h2></div></div><div class="webapps-meta-row"><div class="webapps-facts"><span title="${escape(target.upstreamURL)}">${escape(target.upstreamURL)}</span><small title="${escape(publicPrefix(base, target.mountPath))}">${escape(publicPrefix(base, target.mountPath))}</small></div></div>`;
      const actions = document.createElement("div"); actions.className = "webapps-actions";
      const button = (label, style, work) => { const el = document.createElement("button"); el.type = "button"; el.className = "button " + style; el.textContent = label; el.onclick = work; actions.append(el); };
      button("Open", "primary small", () => action(async () => { const params = { targetId: target.id }; if (target.forwardPublicHost || target.forwardPublicScheme) { params.publicHost = location.host; params.publicScheme = location.protocol.slice(0, -1); } const opened = await call("apps.session.open", params); launchGateway(opened); }));
      button("Edit", "secondary small", () => edit(target));
      button("Delete", "danger small", () => { if (confirm(`Delete Web App “${target.name}”?`)) void action(async () => { await call("apps.targets.delete", { id: target.id }); await removeWorker(publicPrefix(base, target.mountPath)); }); });
      const active = sessions.filter(session => session.targetId === target.id);
      if (active.length) button(`Close sessions (${active.length})`, "secondary small", () => action(async () => { for (const session of active) await call("apps.session.close", { id: session.id }); }));
      card.querySelector(".webapps-meta-row").append(actions); root.append(card);
    }
    page.append(root);
  }
  async function load() {
    if (disposed) return;
    try { const [t, s] = await Promise.all([call("apps.targets.list"), call("apps.sessions.list")]); targets = t.targets; sessions = s.sessions; }
    catch (error) { message = error.message; } render();
  }
  runpilot.navigation.register({ id: "web-apps", title: "Web Apps", icon: "◈", render(root) { page = root; void load(); }, headerActions(root) { const button = document.createElement("button"); button.className = "button primary small"; button.textContent = "Add Web App"; button.onclick = () => edit(); root.append(button); } });
  const refresh = setInterval(() => { if (page?.isConnected && page.classList.contains("active")) void load(); }, 30000);
  return { dispose() { disposed = true; clearInterval(refresh); } };
}
