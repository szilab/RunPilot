const PLUGIN = "remote.rdp";
let guacamoleLoadPromise;
export function guacamoleAssetURL(moduleURL = import.meta.url) {
  return new URL("./vendor/guacamole/guacamole-common-js-1.6.0.min.js", moduleURL).href;
}
export function loadGuacamole() {
  if (guacamoleLoadPromise) return guacamoleLoadPromise;
  guacamoleLoadPromise = new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = guacamoleAssetURL();
    script.async = true;
    script.onload = () => window.Guacamole?.Client && window.Guacamole?.Tunnel
      ? resolve(window.Guacamole) : reject(new Error("The packaged Guacamole client did not initialize."));
    script.onerror = () => reject(new Error("Could not load the RDP plugin's packaged Guacamole client."));
    document.head.append(script);
  }).catch(error => { guacamoleLoadPromise = null; throw error; });
  return guacamoleLoadPromise;
}
export function normalizeClipboardText(text, mode = "preserve") {
  if (mode === "unix") return String(text).replace(/\r\n?/g, "\n");
  if (mode === "windows") return String(text).replace(/\r\n|\r|\n/g, "\r\n");
  return String(text);
}
const MAX_CLIPBOARD_BYTES = 1024 * 1024;
export function receiveClipboard(client, enabled, text) {
  if (!enabled || typeof text !== "string" || new TextEncoder().encode(text).byteLength > MAX_CLIPBOARD_BYTES) return false;
  // guacd applies the configured normalize-clipboard mode at the RDP boundary.
  client.latestRemoteClipboard = text;
  return true;
}
export function attachClipboardReader(Guacamole, client, stream, mimetype, options, isActive = () => true, onReceived = () => {}) {
  if (!options?.clipboard || options.copy === false || !/^text\/plain(?:\s*;\s*charset=[^;]+)?$/i.test(String(mimetype || ""))) {
    try { stream.sendEnd(); } catch {}
    return null;
  }
  const reader = new Guacamole.StringReader(stream);
  let incoming = "", byteCount = 0;
  reader.ontext = chunk => {
    if (!isActive()) return;
    byteCount += new TextEncoder().encode(chunk).byteLength;
    if (byteCount > MAX_CLIPBOARD_BYTES) { incoming = ""; try { stream.sendEnd(); } catch {} return; }
    incoming += chunk;
  };
  reader.onend = () => {
    if (!isActive() || byteCount > MAX_CLIPBOARD_BYTES) return;
    if (receiveClipboard(client, true, incoming)) onReceived(incoming);
  };
  return reader;
}
export function sendClipboardText(Guacamole, client, text, enabled = true) {
  if (!enabled || typeof text !== "string" || new TextEncoder().encode(text).byteLength > MAX_CLIPBOARD_BYTES) return false;
  const stream = client.createOutputStream("text/plain");
  const writer = new Guacamole.StringWriter(stream);
  writer.sendText(text); writer.sendEnd();
  return true;
}
function clearClipboardState(state) {
  try {
    if (state.clipboardReader) { state.clipboardReader.ontext = null; state.clipboardReader.onend = null; }
    state.clipboardWriter?.sendEnd?.();
  } catch {}
  state.clipboardReader = null; state.clipboardWriter = null;
}
const DEFAULT_OPTIONS = Object.freeze({
  host: "", port: 3389, username: "", domain: "", securityMode: "automatic",
  resizeMethod: "display-update", dpiMode: "auto", dpi: 96, colorDepth: 0,
  certificatePolicy: "validate", certificateFingerprint: "", serverLayout: "",
  clipboard: true, copy: true, paste: true, clipboardNormalization: "preserve",
  performanceProfile: "balanced", timeoutSeconds: 10, timeZone: "",
});
const LAYOUTS = [["", "Server default / US English"], ["en-us-qwerty", "English US"], ["en-gb-qwerty", "English UK"], ["de-de-qwertz", "German"], ["de-ch-qwertz", "German Swiss"], ["fr-fr-azerty", "French"], ["fr-be-azerty", "French Belgian"], ["fr-ch-qwertz", "French Swiss"], ["hu-hu-qwertz", "Hungarian"], ["it-it-qwerty", "Italian"], ["es-es-qwerty", "Spanish"], ["es-latam-qwerty", "Latin American"], ["sv-se-qwerty", "Swedish"], ["no-no-qwerty", "Norwegian"], ["tr-tr-qwerty", "Turkish Q"], ["pt-br-qwerty", "Brazilian PT"], ["ja-jp-qwerty", "Japanese"], ["failsafe", "Failsafe Unicode"]];
const splitUsername = value => { const text = String(value || "").trim(), separator = text.indexOf("\\"); return separator > 0 ? { domain: text.slice(0, separator), username: text.slice(separator + 1) } : { domain: "", username: text }; };
const formatUsername = (username, domain) => domain ? `${domain}\\${username || ""}` : username || "";
const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, character => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[character]));

export function createGuacamoleStreamTunnel(Guacamole, stream, connectionID) {
  const tunnel = new Guacamole.Tunnel();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let pending = new Uint8Array(), stopped = false;
  const maxElement = 1 << 20, maxArgs = 512, maxInstruction = 2 << 20;
  function fail(message) {
    if (stopped) return;
    stopped = true;
    tunnel.setState(Guacamole.Tunnel.State.CLOSED);
    tunnel.onerror?.(new Guacamole.Status(Guacamole.Status.Code.SERVER_ERROR, message));
    stream.close();
  }
  function parseLength(data, start) {
    let offset = start, value = 0, digits = 0;
    while (offset < data.length && data[offset] >= 48 && data[offset] <= 57) {
      if (digits++ >= 8) throw new Error("Invalid Guacamole instruction length");
      value = value * 10 + data[offset++] - 48;
      if (value > maxElement) throw new Error("Guacamole instruction element exceeds its limit");
    }
    if (offset === data.length) return null;
    if (!digits || data[offset] !== 46) throw new Error("Invalid Guacamole instruction length");
    return { length: value, next: offset + 1 };
  }
  function consume(bytes) {
    if (stopped) return;
    const merged = new Uint8Array(pending.length + bytes.length);
    merged.set(pending); merged.set(bytes, pending.length);
    let offset = 0;
    try {
      while (offset < merged.length) {
        const start = offset, elements = [];
        let total = 0, complete = false;
        while (!complete) {
          const parsed = parseLength(merged, offset);
          if (!parsed) { pending = merged.slice(start); return; }
          if (merged.length < parsed.next + parsed.length + 1) { pending = merged.slice(start); return; }
          const end = parsed.next + parsed.length, separator = merged[end];
          if (separator !== 44 && separator !== 59) throw new Error("Invalid Guacamole instruction separator");
          elements.push(decoder.decode(merged.subarray(parsed.next, end)));
          if (elements.length > maxArgs || (total += parsed.length) > maxInstruction) throw new Error("Guacamole instruction exceeds its limit");
          offset = end + 1;
          complete = separator === 59;
        }
        if (!elements.length) throw new Error("Guacamole instruction is empty");
        const opcode = elements.shift(), args = elements;
        if (opcode === "" && args[0] === "ping") {
          tunnel.sendMessage("", ...args);
        } else {
          tunnel.oninstruction?.(opcode, args);
        }
      }
      pending = new Uint8Array();
    } catch (error) { fail(error.message || "Invalid Guacamole instruction"); }
  }
  tunnel.connect = () => {
    if (stopped) return;
    tunnel.setState(Guacamole.Tunnel.State.CONNECTING);
    tunnel.setState(Guacamole.Tunnel.State.OPEN);
    queueMicrotask(() => { if (!stopped) tunnel.oninstruction?.("ready", [connectionID]); });
  };
  tunnel.sendMessage = (...args) => {
    if (stopped || tunnel.state === Guacamole.Tunnel.State.CLOSED || !args.length) return;
    try {
      const instruction = Guacamole.Parser.toInstruction(args);
      const bytes = new TextEncoder().encode(instruction);
      for (let offset = 0; offset < bytes.length; offset += 32768) stream.send(bytes.subarray(offset, offset + 32768));
    } catch (error) { fail(error.message || "Could not send Guacamole instruction"); }
  };
  tunnel.disconnect = () => {
    if (stopped) return;
    try { tunnel.sendMessage("disconnect"); } catch {}
    stopped = true;
    stream.close();
    tunnel.setState(Guacamole.Tunnel.State.CLOSED);
  };
  stream.ondata = consume;
  stream.onclose = () => {
    if (stopped) return;
    stopped = true;
    tunnel.setState(Guacamole.Tunnel.State.CLOSED);
    tunnel.onerror?.(new Guacamole.Status(Guacamole.Status.Code.UPSTREAM_UNAVAILABLE, "The RDP stream closed."));
  };
  return tunnel;
}

function defaultGuacdSettings() { return { host: "127.0.0.1", port: 4822, tls: false, connectTimeoutSeconds: 5 }; }

export async function activate(runpilot) {
  const ui = runpilot.ui, call = (method, params = {}) => runpilot.ws.call(PLUGIN, method, params);
  let page = null, targets = [], notice = "", activeSession = null, settingsForm = null, disposed = false;
  const settingsState = { loaded: false, loading: false, busy: false, error: "", status: "", value: defaultGuacdSettings() };

  function addDiagnostic(state, stage) {
    if (!state || typeof stage !== "string" || !stage || stage.length > 256) return;
    state.diagnostics.push(`${new Date().toLocaleTimeString()} ${stage}`);
    if (state.diagnostics.length > 100) state.diagnostics.splice(0, state.diagnostics.length - 100);
    if (state.diagnosticsOutput?.isConnected) state.diagnosticsOutput.textContent = state.diagnostics.join("\n");
  }

  async function refreshSessionDiagnostics(state) {
    if (!state.diagnosticsOutput?.isConnected || !state.session) return;
    try {
      const result = await call("rdp.session.diagnostics", { diagnosticId: state.diagnosticId });
      if (state.diagnosticsOutput?.isConnected && result.log) state.diagnosticsOutput.textContent = result.log;
    } catch {
      if (state.diagnosticsOutput?.isConnected && !state.diagnosticsOutput.textContent) state.diagnosticsOutput.textContent = state.diagnostics.join("\n");
    }
  }

  function showDiagnostics(state) {
    const dialog = document.createElement("dialog"); dialog.className = "dialog rdp-diagnostics-dialog";
    dialog.innerHTML = `<div class="dialog-head"><div><h2>${escapeHTML(state.target.name)} diagnostics</h2><p class="rdp-diagnostics-state"></p></div><button type="button" class="icon-btn" data-close aria-label="Close">×</button></div><pre class="rdp-diagnostics-log"></pre><div class="dialog-actions"><button type="button" class="button secondary" data-close>Close</button></div>`;
    const output = dialog.querySelector(".rdp-diagnostics-log");
    state.diagnosticsOutput = output;
    dialog.querySelector(".rdp-diagnostics-state").textContent = state.session ? `Session ${state.session.id} · ${state.connected ? "connected" : "opening"}` : "Session open is still being negotiated";
    output.textContent = state.diagnostics.join("\n") || "Waiting for diagnostic stages…";
    dialog.querySelectorAll("[data-close]").forEach(node => node.addEventListener("click", () => dialog.close()));
    dialog.addEventListener("close", () => { state.diagnosticsOutput = null; dialog.remove(); }, { once: true });
    document.body.append(dialog); dialog.showModal();
    if (state.session) void refreshSessionDiagnostics(state);
  }

  const stopDiagnosticEvents = runpilot.ws.on(PLUGIN, "rdp.session.diagnostic", event => {
    if (activeSession && event?.diagnosticId === activeSession.diagnosticId) addDiagnostic(activeSession, event.stage);
  });
  const button = (parent, label, action, style = "secondary") => {
    const element = document.createElement("button"); element.type = "button"; element.className = `button ${style} small`; element.textContent = label; element.addEventListener("click", action); parent.append(element); return element;
  };

  async function loadTargets() {
    try { const result = await call("rdp.targets.list"); targets = result.targets || []; notice = ""; }
    catch (error) { notice = error.message; }
    render();
  }

  function render() {
    if (!page?.isConnected || activeSession) return;
    page.innerHTML = `<section class="rdp-plugin"><div class="rdp-plugin-head"><h2>RDP targets</h2><div class="rdp-plugin-actions"><button class="button primary small" data-add>Add target</button></div></div>${notice ? `<div class="rdp-plugin-notice" role="alert">${escapeHTML(notice)}</div>` : ""}<div class="rdp-target-list">${targets.map(target => {
      const endpoint = `${target.options.host || ""}${target.options.port && target.options.port !== 3389 ? `:${target.options.port}` : ""}`;
      const identity = formatUsername(target.options.username, target.options.domain) || "Interactive sign-in";
      return `<article class="docker-card remote-card rdp-target-card"><div class="docker-card-head"><div class="rdp-target-facts"><h2>${escapeHTML(target.name)}</h2></div></div><div class="rdp-target-meta-row"><div class="rdp-target-facts"><span title="${escapeHTML(endpoint)}">${escapeHTML(endpoint)}</span><small class="rdp-target-identity" title="${escapeHTML(identity)}">${escapeHTML(identity)}</small></div><div class="rdp-target-actions"><button class="button danger small" data-delete="${escapeHTML(target.id)}">Delete</button><button class="button secondary small" data-edit="${escapeHTML(target.id)}">Edit</button><button class="button primary small" data-connect="${escapeHTML(target.id)}">Connect</button></div></div></article>`;
    }).join("") || '<div class="empty rdp-target-empty"><h2>No RDP targets</h2></div>'}</div></section>`;
    page.querySelector("[data-add]").addEventListener("click", () => editTarget());
    page.querySelectorAll("[data-connect]").forEach(button => button.addEventListener("click", () => connectPrompt(targets.find(target => target.id === button.dataset.connect))));
    page.querySelectorAll("[data-edit]").forEach(button => button.addEventListener("click", () => editTarget(targets.find(target => target.id === button.dataset.edit))));
    page.querySelectorAll("[data-delete]").forEach(button => button.addEventListener("click", () => deleteTarget(button.dataset.delete)));
  }

  function dialog(title, description, content, onSubmit, submitLabel = "Save") {
    const element = document.createElement("dialog"); element.className = "dialog";
    element.innerHTML = `<form method="dialog"><div class="dialog-head"><div><h2>${escapeHTML(title)}</h2>${description ? `<p>${escapeHTML(description)}</p>` : ""}</div><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div>${content}<div class="form-error hidden" role="alert"></div><div class="dialog-actions"><button class="button secondary" type="button" data-close>Cancel</button><button class="button primary" value="default">${escapeHTML(submitLabel)}</button></div></form>`;
    document.body.append(element);
    const form = element.querySelector("form"), errorNode = element.querySelector(".form-error");
    element.querySelectorAll("[data-close]").forEach(node => node.addEventListener("click", () => element.close()));
    form.addEventListener("submit", async event => {
      event.preventDefault(); errorNode.classList.add("hidden");
      try { await onSubmit(form); element.close(); }
      catch (error) { errorNode.textContent = error.message; errorNode.classList.remove("hidden"); }
    });
    element.addEventListener("close", () => element.remove(), { once: true });
    element.showModal();
    return { element, form };
  }

  function selectField(label, name, choices, selected, className = "") {
    return `<label${className ? ` class="${className}"` : ""}>${escapeHTML(label)}<select name="${name}">${choices.map(([value, text]) => `<option value="${escapeHTML(value)}"${value === selected ? " selected" : ""}>${escapeHTML(text)}</option>`).join("")}</select></label>`;
  }

  function editTarget(existing) {
    const options = { ...DEFAULT_OPTIONS, ...(existing?.options || {}) };
    const clipboardEnabled = options.clipboard !== false && options.copy !== false && options.paste !== false;
    const certificates = [["validate", "Validate certificate"], ["tofu", "Trust on first use"], ["ignore", "Ignore certificate"]];
    if (options.certificatePolicy === "fingerprint") certificates.push(["fingerprint", "Pinned fingerprint"]);
    const performance = [["balanced", "Balanced"], ["quality", "Quality"], ["low-bandwidth", "Low bandwidth"]];
    if (options.performanceProfile === "custom") performance.push(["custom", "Custom"]);
    const content = `<div class="form-grid"><label class="span-2">Name<input name="name" required maxlength="100" value="${escapeHTML(existing?.name || "")}"></label><section class="remote-xpra-settings span-2"><div class="section-head"><div><h3>RDP / Guacamole</h3><p class="muted">Credentials are requested for each connection and never saved.</p></div></div><div class="form-grid remote-xpra-grid"><label>Host<input name="host" required placeholder="rdp.example" inputmode="url" value="${escapeHTML(options.host)}"></label><label>Port<input name="port" type="number" min="1" max="65535" value="${escapeHTML(options.port)}"></label><label class="span-2">Username<input name="username" autocomplete="username" placeholder="DOMAIN\\username" value="${escapeHTML(formatUsername(options.username, options.domain))}"></label></div><details class="remote-xpra-advanced"><summary>Advanced settings</summary><div class="form-grid remote-xpra-grid">${selectField("Server keyboard layout", "serverLayout", LAYOUTS, options.serverLayout, "span-2")}${selectField("Security", "securityMode", [["automatic", "Automatic"], ["nla", "NLA"], ["nla-ext", "NLA Extended"], ["tls", "TLS"], ["rdp", "Legacy RDP"]], options.securityMode)}${selectField("Resize", "resizeMethod", [["display-update", "Dynamic / Display Update"], ["reconnect", "Reconnect on resize"], ["fixed", "Fixed"]], options.resizeMethod)}${selectField("Certificate", "certificatePolicy", certificates, options.certificatePolicy)}<label>Connection timeout<input name="timeoutSeconds" type="number" min="1" max="120" value="${escapeHTML(options.timeoutSeconds)}"></label>${selectField("Clipboard line endings", "clipboardNormalization", [["preserve", "Preserve"], ["unix", "Unix (LF)"], ["windows", "Windows (CRLF)"]], options.clipboardNormalization)}<label class="check"><input name="clipboard" type="checkbox" ${clipboardEnabled ? "checked" : ""}> Enable clipboard in both directions</label>${selectField("Performance", "performanceProfile", performance, options.performanceProfile)}</div></details></section></div>`;
    const { form } = dialog(existing ? "Edit RDP target" : "Add RDP target", "Connect to a remote desktop through guacd.", content, async form => {
      const value = name => form.elements.namedItem(name).value;
      const identity = splitUsername(value("username"));
      const clipboardEnabled = form.elements.clipboard.checked;
      const target = { id: existing?.id || "", name: value("name").trim(), options: { ...(existing?.options || {}), host: value("host").trim(), port: Number(value("port")), username: identity.username, domain: identity.domain, securityMode: value("securityMode"), serverLayout: value("serverLayout"), resizeMethod: value("resizeMethod"), certificatePolicy: value("certificatePolicy"), timeoutSeconds: Number(value("timeoutSeconds")), clipboardNormalization: value("clipboardNormalization"), clipboard: clipboardEnabled, copy: clipboardEnabled, paste: clipboardEnabled, performanceProfile: value("performanceProfile") } };
      await call("rdp.targets.save", { target });
      await loadTargets();
    }, "Save target");
    form?.elements.name?.focus();
  }

  async function deleteTarget(id) {
    try { await call("rdp.targets.delete", { id }); await loadTargets(); }
    catch (error) { notice = error.message; render(); }
  }

  function settingsCandidate() {
    const fields = settingsForm.elements;
    return { host: fields.host.value.trim(), port: Number(fields.port.value), tls: fields.tls.checked, connectTimeoutSeconds: Number(fields.connectTimeoutSeconds.value) };
  }

  function renderSettingsForm() {
    if (!settingsForm) return;
    const value = settingsState.value, disabled = !settingsState.loaded || settingsState.busy ? "disabled" : "";
    const advancedOpen = settingsForm.querySelector(".rdp-settings-advanced")?.open ? "open" : "";
    settingsForm.innerHTML = `<div class="plugin-setting-info"><strong>RDP connection settings</strong><div class="meta">guacd endpoint used by RDP sessions. guacd itself has no authentication.</div></div>${settingsState.error ? `<div class="form-error" role="alert">${escapeHTML(settingsState.error)}</div>` : ""}${settingsState.status ? `<div class="notice" role="status">${escapeHTML(settingsState.status)}</div>` : ""}<div class="form-grid"><label>Host<input name="host" required value="${escapeHTML(value.host)}" ${disabled}></label><label>Port<input name="port" type="number" min="1" max="65535" value="${escapeHTML(value.port)}" ${disabled}></label></div><div class="plugin-settings-footer"><details class="rdp-settings-advanced" ${advancedOpen}><summary>Advanced settings</summary><div class="form-grid"><label>Connect timeout (seconds)<input name="connectTimeoutSeconds" type="number" min="1" max="30" value="${escapeHTML(value.connectTimeoutSeconds)}" ${disabled}></label><label class="check span-2 rdp-tls-toggle"><input name="tls" type="checkbox" role="switch" ${value.tls ? "checked" : ""} ${disabled}><span>Use TLS for GUACD endpoint</span></label></div></details><div class="plugin-settings-actions rdp-settings-actions"><button class="button secondary small" type="button" data-test ${disabled}>Test connection</button><button class="button primary small" type="submit" ${disabled}>${settingsState.busy ? "Working…" : "Save"}</button></div></div>`;
    settingsForm.setAttribute("aria-busy", settingsState.loading || settingsState.busy ? "true" : "false");
    settingsForm.querySelector("[data-test]").addEventListener("click", testSettings);
  }

  async function loadSettings() {
    settingsState.loading = true; settingsState.error = ""; renderSettingsForm();
    try { const result = await call("rdp.settings.get"); settingsState.value = { ...defaultGuacdSettings(), ...(result.settings || {}) }; settingsState.loaded = true; }
    catch (error) { settingsState.error = error.message; }
    finally { settingsState.loading = false; renderSettingsForm(); }
  }

  async function testSettings() {
    if (!settingsForm || settingsState.busy) return;
    settingsState.value = settingsCandidate(); settingsState.busy = true; settingsState.error = ""; settingsState.status = "Testing connection…"; renderSettingsForm();
    try { await call("rdp.settings.test", settingsState.value); settingsState.status = "guacd is reachable."; }
    catch (error) { settingsState.status = ""; settingsState.error = error.message; }
    finally { settingsState.busy = false; renderSettingsForm(); }
  }

  async function saveSettings(event) {
    event.preventDefault();
    if (!settingsState.loaded || settingsState.busy) return;
    settingsState.value = settingsCandidate(); settingsState.busy = true; settingsState.error = ""; settingsState.status = ""; renderSettingsForm();
    try { const result = await call("rdp.settings.set", settingsState.value); settingsState.value = { ...defaultGuacdSettings(), ...(result.settings || settingsState.value) }; ui.toast("RDP connection settings saved"); }
    catch (error) { settingsState.error = error.message; }
    finally { settingsState.busy = false; renderSettingsForm(); }
  }

  function connectPrompt(target) {
    if (!target) return;
    const content = `<div class="form-grid"><label class="span-2">Username<input name="username" autocomplete="username" placeholder="DOMAIN\\username" value="${escapeHTML(formatUsername(target.options.username, target.options.domain))}"></label><label class="span-2">Password (optional)<input name="password" type="password" autocomplete="current-password"></label></div>`;
    const { form } = dialog(`Connect to ${target.name}`, "Credentials are optional and stay in this browser session only. Leave the password blank for the remote server's interactive login screen.", content, form => {
      const secret = { ...splitUsername(form.elements.username.value), password: form.elements.password.value };
      form.elements.password.value = "";
      void openSession(target, secret).finally(() => { secret.password = ""; });
    }, "Connect");
    form?.elements.password?.focus();
  }

  function guacError(status) {
    const code = Number(status?.code);
    const label = ({514:"The RDP host timed out",515:"The RDP host rejected the connection",516:"The RDP target was not found",519:"guacd could not reach the RDP host",520:"The RDP host is unavailable",769:"guacd authentication failed",771:"guacd denied the connection",776:"The RDP client timed out",781:"The RDP stream was too slow"})[code];
    return label || `RDP connection failed${Number.isFinite(code) ? ` (${code})` : ""}.`;
  }

  async function openSession(target, credentials) {
    const view = ui.createInteractiveSessionView({
      container: page,
      title: target.name,
      onBack: () => closeSession(true),
      onDisconnect: () => closeSession(true),
    });
    view.setStatus("connecting"); view.setLoading(true, "Opening RDP session…");
    const diagnosticId = globalThis.crypto?.randomUUID?.().replaceAll("-", "") || `${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`;
    const state = { target, view, client: null, stream: null, session: null, diagnosticId, diagnostics: [], diagnosticsOutput: null, resizeOff: null, closing: false, connected: false, lastResize: null };
    activeSession = state;
    const diagnose = document.createElement("button"); diagnose.type = "button"; diagnose.className = "button secondary small"; diagnose.textContent = "Diagnose"; diagnose.title = "View credential-safe session diagnostics"; diagnose.addEventListener("click", () => showDiagnostics(state));
    view.actions.insertBefore(diagnose, view.actions.lastElementChild);
    const copyButton = button(view.actions, "Copy remote clipboard", () => copyRemoteClipboard(state));
    const pasteButton = button(view.actions, "Paste local clipboard", () => pasteLocalClipboard(state));
    copyButton.hidden = !target.options.clipboard || target.options.copy === false;
    pasteButton.hidden = !target.options.clipboard || target.options.paste === false;
    addDiagnostic(state, "RDP session startup requested");
    try {
      view.setLoading(true, "Measuring desktop surface…");
      const size = view.surface.getSize();
      const initial = size.width > 0 && size.height > 0 ? size : { width: 1024, height: 768 };
      state.lastResize = initial;
      view.setLoading(true, "Opening RDP session…");
      addDiagnostic(state, `surface measured ${initial.width}x${initial.height}`);
      const result = await call("rdp.session.open", { targetId: target.id, diagnosticId, credentials, client: { width: initial.width, height: initial.height, dpi: window.devicePixelRatio > 1 ? 120 : 96, timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || "", clientName: "RunPilot", imageMimetypes: ["image/png", "image/jpeg"], audioMimetypes: [], videoMimetypes: [] } });
      const session = result.session;
      if (!session?.streamId || !session.connectionId) throw new Error("The RDP backend returned incomplete session metadata.");
      state.session = session;
      addDiagnostic(state, "backend negotiation returned ready");
      void refreshSessionDiagnostics(state);
      view.setLoading(true, "Attaching RDP stream…");
      const stream = await runpilot.ws.openStream(PLUGIN, session.streamId);
      state.stream = stream;
      addDiagnostic(state, "application WebSocket stream attached");
      view.setLoading(true, "Starting desktop…");
      const Guacamole = await loadGuacamole();
      if (state.closing || activeSession !== state) { stream.close(); return; }
      state.guacamole = Guacamole;
      const tunnel = createGuacamoleStreamTunnel(Guacamole, stream, session.connectionId);
      const client = new Guacamole.Client(tunnel); state.client = client;
      addDiagnostic(state, "Guacamole client initialized");
      const display = client.getDisplay();
      view.surface.element.classList.add("rdp-session-surface");
      view.surface.element.append(display.getElement());
      const mouse = new Guacamole.Mouse(display.getElement());
      mouse.onEach(["mousedown", "mousemove", "mouseup"], event => client.sendMouseState(event.state));
      const keyboard = new Guacamole.Keyboard(view.surface.element);
      keyboard.onkeydown = keysym => client.sendKeyEvent(1, keysym);
      keyboard.onkeyup = keysym => client.sendKeyEvent(0, keysym);
      const fitDisplay = () => {
        const scale = view.surface.fitScale({ width: display.getWidth(), height: display.getHeight() });
        if (scale !== null) display.scale(scale);
      };
      display.onresize = fitDisplay;
      client.onclipboard = (clipboardStream, mimetype) => {
        const reader = attachClipboardReader(Guacamole, client, clipboardStream, mimetype, target.options, () => !state.closing && activeSession === state, () => { state.clipboardReader = null; });
        if (!reader) return;
        state.clipboardReader = reader;
      };
      const sendSurfaceSize = next => {
        if (!state.connected || target.options.resizeMethod === "fixed" || !next?.width || !next?.height) return;
        if (state.lastResize?.width === next.width && state.lastResize?.height === next.height) return;
        state.lastResize = { width: next.width, height: next.height };
        addDiagnostic(state, `requested desktop size ${next.width}x${next.height}`);
        client.sendSize(next.width, next.height);
      };
      state.resizeOff = view.surface.onResize(next => {
        fitDisplay();
        sendSurfaceSize(next);
      });
      client.onstatechange = clientState => {
        if (activeSession !== state || state.closing) return;
        if (clientState === Guacamole.Client.State.CONNECTED) {
          state.connected = true; addDiagnostic(state, "RDP desktop connected"); view.setLoading(false); view.setError(""); view.setStatus("connected"); view.surface.focus(); fitDisplay(); sendSurfaceSize(view.surface.getSize());
        } else if (clientState === Guacamole.Client.State.DISCONNECTED) {
          state.connected = false;
          clearClipboardState(state);
          if (!state.closing) { addDiagnostic(state, "RDP desktop disconnected"); view.setLoading(false); view.setStatus("disconnected"); view.setError("The RDP session disconnected."); }
        }
      };
      client.onerror = status => { clearClipboardState(state); if (activeSession === state && !state.closing) { addDiagnostic(state, `Guacamole client error (code=${Number(status?.code) || 0})`); view.setError(guacError(status)); } };
      stream.onclose = () => { clearClipboardState(state); if (activeSession === state && !state.closing) { addDiagnostic(state, "application WebSocket stream closed"); state.connected = false; view.setStatus("disconnected"); view.setError("The RDP stream closed."); } };
      client.connect("");
    } catch (error) {
      addDiagnostic(state, `session startup failed (${error.code || "failed"})`);
      if (activeSession === state) { view.setError(error.message); view.setStatus("error"); }
      if (state.stream) state.stream.close();
      if (state.session) await call("rdp.session.close", { id: state.session.id }).catch(() => {});
      if (credentials) credentials.password = "";
    }
  }

  async function copyRemoteClipboard(state) {
    const text = state?.client?.latestRemoteClipboard;
    if (typeof text !== "string" || state.closing) { ui.toast("No remote clipboard text is available yet"); return; }
    try {
      if (!navigator.clipboard?.writeText) throw new Error("Clipboard access is unavailable");
      await navigator.clipboard.writeText(text); ui.toast("Remote clipboard copied locally");
    } catch {
      showClipboardFallback("Copy remote clipboard", text, false);
    }
  }

  async function pasteLocalClipboard(state) {
    if (!state || state.closing || !state.target.options.clipboard || state.target.options.paste === false) return;
    let text;
    try {
      if (!navigator.clipboard?.readText) throw new Error("Clipboard access is unavailable");
      text = await navigator.clipboard.readText();
    } catch {
      text = await showClipboardFallback("Paste local clipboard", "", true);
    }
    if (typeof text !== "string" || state.closing || activeSession !== state || !text) return;
    if (new TextEncoder().encode(text).byteLength > MAX_CLIPBOARD_BYTES) { ui.toast("Clipboard text exceeds the 1 MiB limit"); return; }
    try {
      if (!sendClipboardText(state.guacamole, state.client, text, true)) { ui.toast("Clipboard text exceeds the 1 MiB limit"); return; }
    } catch { ui.toast("Could not send clipboard text to the RDP session"); }
  }

  function showClipboardFallback(title, text, editable) {
    return new Promise(resolve => {
      const dialog = document.createElement("dialog"); dialog.className = "dialog rdp-clipboard-dialog";
      dialog.innerHTML = `<div class="dialog-head"><h2>${escapeHTML(title)}</h2><button type="button" class="icon-btn" data-close aria-label="Close">×</button></div><p>${editable ? "Paste text here, then select Send." : "Select and copy this text manually."}</p><textarea rows="8" maxlength="1048576" aria-label="Clipboard text"></textarea><div class="dialog-actions"><button class="button secondary" data-close type="button">Cancel</button>${editable ? '<button class="button primary" data-send type="button">Send</button>' : ""}</div>`;
      const area = dialog.querySelector("textarea"); area.value = text; area.readOnly = !editable;
      let result;
      dialog.querySelectorAll("[data-close]").forEach(node => node.addEventListener("click", () => dialog.close()));
      dialog.querySelector("[data-send]")?.addEventListener("click", () => { result = area.value; dialog.close(); });
      dialog.addEventListener("close", () => { dialog.remove(); resolve(result); }, { once: true });
      document.body.append(dialog); dialog.showModal(); area.focus(); area.select();
    });
  }

  async function closeSession(returnToTargets) {
    const state = activeSession;
    if (!state || state.closing) return;
    state.closing = true; activeSession = null;
    state.resizeOff?.();
    clearClipboardState(state);
    state.diagnosticsOutput?.closest("dialog")?.close();
    try { state.client?.disconnect(); } catch {}
    state.stream?.close();
    if (state.session) await call("rdp.session.close", { id: state.session.id }).catch(() => {});
    state.view.dispose();
    if (returnToTargets && page?.isConnected) { notice = ""; render(); }
  }

  runpilot.settings.register({
    id: "remote.rdp",
    render: () => {
      settingsForm = document.createElement("form");
      settingsForm.className = "plugin-settings-form rdp-plugin-settings";
      settingsForm.addEventListener("submit", saveSettings);
      renderSettingsForm();
      if (!settingsState.loaded && !settingsState.loading) void loadSettings();
      return settingsForm;
    },
  });
  runpilot.navigation.register({
    id: "remote-rdp", title: "RDP", icon: "monitor",
    render(root) { page = root; if (activeSession) root.append(activeSession.view.element); else void loadTargets(); },
  });
  return () => { disposed = true; stopDiagnosticEvents(); void closeSession(false); };
}
