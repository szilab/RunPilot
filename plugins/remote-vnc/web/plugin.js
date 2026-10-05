import RFB from "./vendor/novnc/core/rfb.js";
import { provideVNCCredentials } from "./credentials.js";
import { createVNCChannelAdapter } from "./raw-channel.js";

const PLUGIN = "remote.vnc";
const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, character => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;", "'":"&#39;"}[character]));

function field(label, name, value, type = "text", attrs = "") {
  return `<label class="vnc-field"><span>${label}</span><input name="${name}" type="${type}" value="${escapeHTML(value)}" ${attrs}></label>`;
}

function credentialPrompt(name, username = "") {
  return new Promise(resolve => {
    const dialog = document.createElement("dialog");
    dialog.className = "dialog vnc-credentials-dialog";
    dialog.innerHTML = `<form method="dialog" class="vnc-form"><div class="dialog-head"><h2>VNC credentials</h2><button class="icon-btn" type="button" data-cancel aria-label="Cancel">×</button></div><p>Enter the password for ${escapeHTML(name)}. It is kept only for this connection.</p>${field("Password", "password", "", "password", 'autocomplete="current-password"')}<div class="dialog-actions"><button class="button secondary" type="button" data-cancel>Cancel</button><button class="button primary" type="submit">Connect</button></div></form>`;
    const form = dialog.querySelector("form");
    const settle = value => { if (!dialog.isConnected) return; dialog.close(); dialog.remove(); resolve(value); };
    form.addEventListener("submit", event => {
      event.preventDefault();
      const password = form.elements.password.value;
      form.elements.password.value = "";
      settle({ username, password });
    });
    dialog.querySelectorAll("[data-cancel]").forEach(button => button.addEventListener("click", () => settle(null)));
    dialog.addEventListener("cancel", event => { event.preventDefault(); settle(null); });
    document.body.append(dialog); dialog.showModal(); form.elements.password.focus();
  });
}

function clearCredentials(state) {
  state.clearCredentials?.();
  state.clearCredentials = null;
}

export async function activate(runpilot) {
  const call = (method, params = {}) => runpilot.ws.call(PLUGIN, method, params);
  let page = null, targets = [], message = "", active = null, headerAction = null, disposed = false;

  function addTargetDialog(existing = null) {
    const dialog = document.createElement("dialog");
    dialog.className = "dialog vnc-target-dialog";
    dialog.innerHTML = `<form method="dialog" class="vnc-form"><div class="dialog-head"><h2>${existing ? "Edit VNC target" : "Add VNC target"}</h2><button class="icon-btn" type="button" data-cancel aria-label="Cancel">×</button></div><input type="hidden" name="id" value="${escapeHTML(existing?.id || "")}">${field("Name", "name", existing?.name || "", "text", 'maxlength="100" required autocomplete="off"')}${field("Host", "host", existing?.host || "", "text", 'maxlength="253" required autocomplete="off"')}${field("Port", "port", existing?.port || 5900, "number", 'min="1" max="65535" required')}${field("Username (optional)", "username", existing?.username || "", "text", 'maxlength="256" autocomplete="username"')}${field("Connect timeout (seconds)", "connectTimeoutSeconds", existing?.connectTimeoutSeconds || 10, "number", 'min="1" max="30" required')}<p class="vnc-form-error" role="alert" hidden></p><div class="dialog-actions"><button class="button secondary" type="button" data-cancel>Cancel</button><button class="button primary" type="submit">Save target</button></div></form>`;
    const form = dialog.querySelector("form"), error = dialog.querySelector(".vnc-form-error");
    dialog.querySelectorAll("[data-cancel]").forEach(button => button.addEventListener("click", () => dialog.close()));
    form.addEventListener("submit", async event => {
      event.preventDefault(); const submit = form.querySelector('[type="submit"]'); submit.disabled = true; error.hidden = true;
      const data = new FormData(form);
      const target = { id: data.get("id"), name: data.get("name"), host: data.get("host"), port: Number(data.get("port")), username: data.get("username"), connectTimeoutSeconds: Number(data.get("connectTimeoutSeconds")) };
      try { await call("vnc.targets.save", { target }); dialog.close(); await loadTargets(); }
      catch (cause) { error.textContent = cause.message || "Could not save VNC target"; error.hidden = false; submit.disabled = false; }
    });
    dialog.addEventListener("close", () => dialog.remove(), { once: true });
    document.body.append(dialog); dialog.showModal(); form.elements.name.focus();
  }

  async function deleteTarget(target) {
    if (!confirm(`Delete VNC target “${target.name}”?`)) return;
    try { await call("vnc.targets.delete", { id: target.id }); await loadTargets(); }
    catch (cause) { message = cause.message || "Could not delete target"; renderList(); }
  }

  function renderList() {
    if (!page?.isConnected || active) return;
    page.replaceChildren();
    const section = document.createElement("section"); section.className = "vnc-plugin";
    if (message) { const notice = document.createElement("p"); notice.className = "vnc-plugin-notice"; notice.setAttribute("role", "alert"); notice.textContent = message; section.append(notice); message = ""; }
    const list = document.createElement("div"); list.className = "vnc-target-list";
    if (!targets.length) { const empty = document.createElement("p"); empty.className = "vnc-empty"; empty.textContent = "No VNC targets yet. Add one to get started."; list.append(empty); }
    for (const target of targets) {
      const endpoint = `${target.host}${target.port !== 5900 ? `:${target.port}` : ""}`;
      const identity = target.username || "Password requested when required";
      const card = document.createElement("article"); card.className = "docker-card remote-card vnc-target-card";
      card.innerHTML = `<div class="docker-card-head"><div class="vnc-target-facts"><h2>${escapeHTML(target.name)}</h2></div></div><div class="vnc-target-meta-row"><div class="vnc-target-facts"><span title="${escapeHTML(endpoint)}">${escapeHTML(endpoint)}</span><small class="vnc-target-identity" title="${escapeHTML(identity)}">${escapeHTML(identity)}</small></div></div>`;
      const actions = document.createElement("div"); actions.className = "vnc-target-actions";
      const button = (label, style, handler) => { const control = document.createElement("button"); control.type = "button"; control.className = `button ${style}`; control.textContent = label; control.addEventListener("click", handler); actions.append(control); };
      button("Delete", "danger small", () => void deleteTarget(target)); button("Edit", "secondary small", () => addTargetDialog(target)); button("Connect", "primary small", () => openSession(target));
      card.querySelector(".vnc-target-meta-row").append(actions); list.append(card);
    }
    section.append(list); page.append(section);
  }

  async function loadTargets() {
    if (disposed) return;
    try { const result = await call("vnc.targets.list"); targets = Array.isArray(result.targets) ? result.targets : []; message = ""; }
    catch (cause) { targets = []; message = cause.message || "Could not load VNC targets"; }
    renderList();
  }

  async function closeSession(returnToList = true) {
    const state = active; if (!state || state.closing) return; state.closing = true; active = null;
    if (headerAction?.isConnected) headerAction.hidden = false;
    clearCredentials(state);
    state.stopResize?.();
    try { state.rfb?.disconnect(); } catch {}
    state.channel?.close(); state.stream?.close();
    if (state.session) await call("vnc.session.close", { id: state.session.id }).catch(() => {});
    state.view.dispose();
    if (returnToList && page?.isConnected) renderList();
  }

  async function openSession(target) {
    if (active) return;
    const view = runpilot.ui.createInteractiveSessionView({ container: page, title: target.name, onBack: () => void closeSession(true), onDisconnect: () => void closeSession(true) });
    view.element.classList.add("vnc-interactive-view");
    const labelIcon = (element, name, label) => { element.classList.add("vnc-toolbar-icon", `vnc-toolbar-${name}`); element.setAttribute("aria-label", label); element.title = label; };
    const backButton = view.element.querySelector(".rp-interactive-heading > button");
    const [fullscreenButton, disconnectButton] = view.actions.querySelectorAll("button");
    view.actions.prepend(backButton);
    labelIcon(backButton, "back", "Back to VNC targets");
    labelIcon(fullscreenButton, "fullscreen", "Toggle fullscreen");
    labelIcon(disconnectButton, "disconnect", "Disconnect");
    const state = { view, target, session: null, stream: null, channel: null, rfb: null, clearCredentials: null, authFailure: false, closing: false };
    active = state; view.setStatus("connecting"); view.setLoading(true, `Connecting to ${target.name}…`);
    if (headerAction?.isConnected) headerAction.hidden = true;
    try {
      const result = await call("vnc.session.open", { targetId: target.id });
      if (active !== state) { await call("vnc.session.close", { id: result.session.id }).catch(() => {}); return; }
      state.session = result.session;
      state.stream = await runpilot.ws.openStream(PLUGIN, result.streamId);
      if (active !== state) { state.stream.close(); return; }
      state.channel = createVNCChannelAdapter(state.stream);
      const remoteSurface = document.createElement("div"); remoteSurface.className = "vnc-session-surface"; view.surface.element.append(remoteSurface);
      state.rfb = new RFB(remoteSurface, state.channel, { shared: true });
      state.rfb.scaleViewport = true; state.rfb.resizeSession = true; state.rfb.clipViewport = false; state.rfb.viewOnly = false;
      state.stopResize = view.surface.onResize(() => { if (state.rfb && !state.closing) state.rfb.scaleViewport = true; });
      state.rfb.addEventListener("connect", () => { if (active !== state) return; clearCredentials(state); view.setLoading(false); view.setStatus("connected"); view.surface.focus(); });
      state.rfb.addEventListener("credentialsrequired", async () => {
        if (active !== state) return;
        const credentials = await credentialPrompt(target.name, target.username || "");
        if (!credentials || active !== state) { await closeSession(true); return; }
        try {
          clearCredentials(state);
          state.clearCredentials = provideVNCCredentials(state.rfb, credentials.username, credentials.password);
          credentials.password = ""; credentials.username = "";
        } catch { credentials.password = ""; credentials.username = ""; await closeSession(true); }
      });
      state.rfb.addEventListener("securityfailure", event => {
        if (active !== state) return;
        state.authFailure = true;
        view.setError(`VNC authentication failed${event.detail?.status ? `: ${event.detail.status}` : ""}.`);
      });
      state.rfb.addEventListener("disconnect", event => {
        if (active !== state) return;
        clearCredentials(state);
        view.setLoading(false);
        if (!event.detail?.clean && !state.authFailure) view.setError("VNC session disconnected unexpectedly.");
        else view.setStatus("disconnected");
      });
      state.channel.onerror = () => { if (active === state) view.setError("The VNC stream failed."); };
      state.channel.onclose = event => { if (active === state && event.code !== 1000) view.setError("The VNC stream closed unexpectedly."); };
      state.rfb.focus();
    } catch (cause) {
      clearCredentials(state);
      if (active === state) { view.setError(cause.message || "Could not connect to VNC server"); view.setStatus("error"); }
      if (state.session) await call("vnc.session.close", { id: state.session.id }).catch(() => {});
      state.channel?.close(); state.stream?.close();
    }
  }

  runpilot.navigation.register({
    id: "remote-vnc", title: "VNC", icon: "monitor",
    headerActions(root) {
      if (active) return;
      const button = document.createElement("button"); button.type = "button"; button.className = "button primary small"; button.textContent = "Add target"; button.addEventListener("click", () => addTargetDialog());
      headerAction = button; root.append(button);
    },
    render(root) { page = root; if (active) root.append(active.view.element); else void loadTargets(); },
  });
  return () => { disposed = true; void closeSession(false); };
}
