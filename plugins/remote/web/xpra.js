import { setupSessionToolbar, toolbarButton } from "./session-toolbar.js";
import { openLogDialog } from "./diagnostics-dialog.js";

const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, character => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;", "'":"&#39;"}[character]));
const MODES = [["application", "Application windows"], ["desktop", "Full desktop"]];
const ATTACH_TIMEOUT = 30000, ATTACH_INTERVAL = 400;

// clientURL opens the packaged Xpra HTML5 client. Its connection goes through
// the bridge stream, so server, port and path only satisfy the client's form.
export function clientURL(session, moduleURL = import.meta.url) {
  const url = new URL("./vendor/xpra-html5/index.html", moduleURL);
  const params = {
    submit: "true", server: "localhost", port: "443", ssl: "false", path: "/",
    reconnect: "false", floating_menu: "false", sound: "false", printing: "false",
    file_transfer: "false", clipboard: session?.clipboard === false ? "false" : "true",
  };
  for (const [key, value] of Object.entries(params)) url.searchParams.set(key, value);
  return url.href;
}

function field(label, name, value, type = "text", attrs = "") {
  return `<label class="vnc-field"><span>${label}</span><input name="${name}" type="${type}" value="${escapeHTML(value)}" ${attrs}></label>`;
}

export function createXpraFeature(runpilot, call, onActivity = () => {}) {
  let page = null, targets = [], status = null, message = "", active = null, disposed = false;
  const stopExitEvents = runpilot.ws.on("remote", "xpra.session.exited", event => {
    if (active && event?.id === active.session?.id) failSession(active, event.message || "Xpra stopped.");
  });

  function targetDialog(existing = null) {
    const dialog = document.createElement("dialog");
    dialog.className = "dialog vnc-target-dialog";
    const modes = MODES.map(([value, label]) => `<option value="${value}"${(existing?.mode || "application") === value ? " selected" : ""}>${label}</option>`).join("");
    dialog.innerHTML = `<form method="dialog" class="vnc-form"><div class="dialog-head"><h2>${existing ? "Edit Xpra target" : "Add Xpra target"}</h2><button class="icon-btn" type="button" data-cancel aria-label="Cancel">×</button></div><p>Runs on this RunPilot host in a private Xpra session.</p><input type="hidden" name="id" value="${escapeHTML(existing?.id || "")}">${field("Name", "name", existing?.name || "", "text", 'maxlength="100" required autocomplete="off"')}<label class="vnc-field"><span>Shows</span><select name="mode">${modes}</select></label>${field("Command", "command", existing?.command || "", "text", 'maxlength="2048" required autocomplete="off" spellcheck="false" placeholder="xterm"')}<details class="rp-disclosure vnc-target-advanced"><summary>Advanced settings</summary><div class="vnc-advanced-fields">${field("Working directory (optional)", "workingDirectory", existing?.workingDirectory || "", "text", 'maxlength="1024" autocomplete="off" spellcheck="false" placeholder="/home/user"')}${field("DPI (0 = automatic)", "dpi", existing?.dpi || 0, "number", 'min="0" max="480"')}<label class="check"><input name="clipboard" type="checkbox" role="switch" ${existing?.clipboard === false ? "" : "checked"}><span>Enable clipboard in both directions</span></label></div></details><p class="vnc-form-error" role="alert" hidden></p><div class="dialog-actions"><button class="button secondary" type="button" data-cancel>Cancel</button><button class="button primary" type="submit">Save target</button></div></form>`;
    const form = dialog.querySelector("form"), error = dialog.querySelector(".vnc-form-error");
    dialog.querySelectorAll("[data-cancel]").forEach(button => button.addEventListener("click", () => dialog.close()));
    form.addEventListener("submit", async event => {
      event.preventDefault(); const submit = form.querySelector('[type="submit"]'); submit.disabled = true; error.hidden = true;
      const data = new FormData(form);
      const target = { id: data.get("id"), name: data.get("name"), mode: data.get("mode"), command: data.get("command"), workingDirectory: String(data.get("workingDirectory") || "").trim(), dpi: Number(data.get("dpi")) || 0, clipboard: form.elements.clipboard.checked };
      try { await call("xpra.targets.save", { target }); dialog.close(); await loadTargets(); }
      catch (cause) { error.textContent = cause.message || "Could not save Xpra target"; error.hidden = false; submit.disabled = false; }
    });
    dialog.addEventListener("close", () => dialog.remove(), { once: true });
    document.body.append(dialog); dialog.showModal(); form.elements.name.focus();
  }

  async function deleteTarget(target) {
    if (!await (runpilot.ui.confirm ? runpilot.ui.confirm({title:"Delete Xpra target?",message:`Remove “${target.name}” from RunPilot?`,confirmLabel:"Delete target",danger:true}) : confirm(`Delete Xpra target “${target.name}”?`))) return;
    try { await call("xpra.targets.delete", { id: target.id }); await loadTargets(); }
    catch (cause) { message = cause.message || "Could not delete target"; renderList(); }
  }

  function renderList() {
    if (!page?.isConnected || active) return;
    page.replaceChildren();
    const section = document.createElement("section"); section.className = "vnc-plugin xpra-plugin";
    const notices = [message, status && !status.available ? status.message : ""].filter(Boolean);
    for (const text of notices) { const notice = document.createElement("p"); notice.className = "vnc-plugin-notice rdp-plugin-notice"; notice.setAttribute("role", "alert"); notice.textContent = text; section.append(notice); }
    message = "";
    const list = document.createElement("div"); list.className = "vnc-target-list";
    if (!targets.length) { const empty = runpilot.ui.EmptyState({title:"No Xpra targets yet",message:"Add an application or desktop to run on this host through Xpra."}); empty.classList.add("vnc-empty"); list.append(empty); }
    for (const target of targets) {
      const mode = MODES.find(([value]) => value === target.mode)?.[1] || "Application windows";
      const card = document.createElement("article"); card.className = "rp-card vnc-target-card xpra-target-card";
      card.innerHTML = `<div class="vnc-target-card-head"><span class="vnc-target-glyph xpra-target-glyph" aria-hidden="true"></span><div class="vnc-target-facts"><h2>${escapeHTML(target.name)}</h2><small class="vnc-target-identity">${escapeHTML(mode)}</small></div></div><div class="vnc-target-meta-row"><div class="vnc-target-facts"><span title="${escapeHTML(target.command)}">${escapeHTML(target.command)}</span></div></div>`;
      const actions = document.createElement("div"); actions.className = "vnc-target-actions";
      const button = (label, style, handler, disabled = false) => { const control = document.createElement("button"); control.type = "button"; control.className = `button ${style}`; control.textContent = label; control.disabled = disabled; control.addEventListener("click", handler); actions.append(control); };
      button("Connect", "primary small", () => void openSession(target), status?.available === false);
      button("Edit", "secondary small", () => targetDialog(target)); button("Delete", "danger small", () => void deleteTarget(target));
      card.querySelector(".vnc-target-meta-row").append(actions); list.append(card);
    }
    section.append(list); page.append(section);
  }

  async function loadTargets() {
    if (disposed) return;
    try {
      const [listed, checked] = await Promise.all([call("xpra.targets.list"), status ? Promise.resolve(status) : call("xpra.status")]);
      targets = Array.isArray(listed.targets) ? listed.targets : []; status = checked;
    } catch (cause) { targets = []; message = cause.message || "Could not load Xpra targets"; }
    renderList();
  }

  async function showDiagnostics(state) {
    const dialog = openLogDialog({ title: `${state.target.name} diagnostics`, stateText: state.session ? `Session ${state.session.id}` : "Session is starting", text: "Loading Xpra output…" });
    if (!state.session) { dialog.output.textContent = "The Xpra server has not started yet."; return; }
    try {
      const result = await call("xpra.session.diagnostics", { id: state.session.id });
      dialog.setState(`Session ${state.session.id} · ${result.sessionState || "unknown"}`);
      dialog.output.textContent = result.log || "Xpra has not written any output yet.";
    } catch (cause) { dialog.output.textContent = cause.message || "Diagnostics are unavailable."; }
  }

  function failSession(state, text) {
    if (active !== state || state.closing) return;
    state.frame?.remove(); state.frame = null;
    state.view.setLoading(false); state.view.setStatus("error"); state.view.setError(text);
  }

  function streamClosed(state) {
    if (active !== state || state.closing) return;
    state.frame?.remove(); state.frame = null;
    state.view.setLoading(false); state.view.setStatus("disconnected"); state.view.setError("The Xpra session disconnected.");
  }

  async function closeSession(returnToList = true) {
    const state = active; if (!state || state.closing) return;
    state.closing = true; active = null; onActivity(false);
    state.frame?.remove();
    state.stream?.close();
    if (state.session) await call("xpra.session.close", { id: state.session.id }).catch(() => {});
    state.view.dispose();
    if (returnToList && page?.isConnected) renderList();
  }

  async function waitForServer(state) {
    const deadline = Date.now() + ATTACH_TIMEOUT;
    for (;;) {
      const attached = await call("xpra.session.attach", { id: state.session.id });
      if (attached.ready) return attached.streamId;
      if (active !== state) return null;
      if (Date.now() > deadline) throw new Error("Xpra did not start within 30 seconds. Open the diagnostics for its output.");
      await new Promise(resolve => setTimeout(resolve, ATTACH_INTERVAL));
      if (active !== state) return null;
    }
  }

  async function openSession(target) {
    if (active || !page) return;
    const view = runpilot.ui.createInteractiveSessionView({ container: page, title: target.name, onBack: () => void closeSession(true), onDisconnect: () => void closeSession(true) });
    const state = { view, target, session: null, stream: null, frame: null, closing: false, pageView: false };
    const diagnose = toolbarButton("diagnose", "Session diagnostics", () => void showDiagnostics(state));
    setupSessionToolbar(view, { backLabel: "Back to Xpra targets", extras: [diagnose], onPageViewChange: value => { state.pageView = value; } });
    active = state; onActivity(true);
    view.setStatus("connecting"); view.setLoading(true, `Starting ${target.name}…`);
    try {
      state.session = (await call("xpra.session.open", { targetId: target.id })).session;
      if (active !== state) { await call("xpra.session.close", { id: state.session.id }).catch(() => {}); return; }
      const streamId = await waitForServer(state);
      if (!streamId || active !== state) return;
      view.setLoading(true, "Connecting to Xpra…");
      state.stream = await runpilot.ws.openStream("remote", streamId);
      if (active !== state) { state.stream.close(); return; }
      // Hold early bytes until the client page takes over the stream.
      const pending = [];
      state.stream.ondata = chunk => pending.push(chunk);
      state.stream.onclose = () => streamClosed(state);
      const frame = document.createElement("iframe");
      frame.className = "xpra-session-frame";
      frame.title = `${target.name} (Xpra)`;
      frame.setAttribute("allow", "clipboard-read; clipboard-write");
      frame.runpilotXpra = {
        stream: state.stream, pending,
        onReady: () => { if (active !== state) return; view.setLoading(false); view.setError(""); view.setStatus("connected"); frame.focus(); },
        onClose: () => streamClosed(state),
      };
      frame.src = clientURL(state.session);
      view.surface.element.classList.add("xpra-session-surface");
      view.surface.element.append(frame);
      view.surface.element.addEventListener("focus", () => frame.focus());
      state.frame = frame;
    } catch (cause) {
      if (active === state) failSession(state, cause.message || "Could not start the Xpra session.");
    }
  }

  return {
    get active() { return !!active; },
    get pageView() { return !!active?.pageView; },
    get element() { return active?.view.element || null; },
    addTarget: () => targetDialog(),
    render(root) { page = root; if (active) { if (!active.pageView) root.append(active.view.element); } else { root.replaceChildren(runpilot.ui.EmptyState({title:"Loading Xpra targets…",message:"Retrieving your saved applications."})); void loadTargets(); } },
    dispose() { disposed = true; stopExitEvents?.(); void closeSession(false); },
  };
}
