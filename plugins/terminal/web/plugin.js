const PLUGIN = "terminal";

function loadScript(source) {
  return new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = source;
    script.onload = resolve;
    script.onerror = () => reject(new Error("Could not load packaged terminal display assets."));
    document.head.append(script);
  });
}

async function loadTerminalAssets() {
  const base = new URL("./vendor/", import.meta.url);
  if (!document.querySelector('link[data-terminal-plugin-xterm]')) {
    const style = document.createElement("link");
    style.rel = "stylesheet";
    style.href = new URL("xterm.css", base).href;
    style.dataset.terminalPluginXterm = "true";
    await new Promise((resolve, reject) => {
      style.onload = resolve;
      style.onerror = () => reject(new Error("Could not load packaged terminal styles."));
      document.head.append(style);
    });
  }
  // Reuse globals already loaded by the host when available; the package still
  // carries both scripts and loads them itself when the host does not provide
  // them, so the plugin does not rely on private legacy page setup.
  if (!window.Terminal) await loadScript(new URL("xterm.js", base).href);
  if (!window.FitAddon) await loadScript(new URL("addon-fit.js", base).href);
}

export async function activate(runpilot) {
  await loadTerminalAssets();
  let page = null;
  let terminal = null;
  let session = null;
  let opening = false;
  let closing = false;
  let statusWarning = false;
  let earlyOutput = [];
  let observers = [];
  let resizeObserver = null;
  let statusTimer = null;
  const state = { error: "", closed: true };
  const ui = runpilot.ui;
  const escape = ui.escape;

  async function call(method, params = {}) {
    const result = await runpilot.ws.call(PLUGIN, method, params);
    if (result?.error) throw new Error(result.error.message || "Terminal request failed");
    return result;
  }
  function cleanup() {
    clearTimeout(statusTimer); statusTimer = null;
    resizeObserver?.disconnect(); resizeObserver = null;
    terminal?.dispose(); terminal = null;
  }
  function render() {
    if (!page?.isConnected) return;
    const existingScreen = terminal?.element?.closest(".terminal-plugin-screen");
    page.innerHTML = `<section class="terminal-plugin">
      <div class="section-head"><div><h2>Terminal (plugin)</h2><p class="meta">Experimental interactive shell</p></div>
      <div class="row-actions"><button class="button primary small" data-action="open" ${session || opening ? "disabled" : ""}>${opening ? "Opening…" : "Open session"}</button><button class="button secondary small" data-action="close" ${session && !closing ? "" : "disabled"}>${closing ? "Closing…" : "Close session"}</button></div></div>
      ${state.error ? `<div class="notice" role="alert">${escape(state.error)}</div>` : ""}
      <div class="terminal-plugin-screen" aria-label="Terminal output"></div>
    </section>`;
    const newScreen = page.querySelector(".terminal-plugin-screen");
    if (existingScreen && session) { newScreen.replaceWith(existingScreen); fit(); }
    else if (terminal) cleanup();
    page.querySelector('[data-action="open"]').addEventListener("click", open);
    page.querySelector('[data-action="close"]').addEventListener("click", close);
    if (session && !terminal) attachTerminal();
  }
  async function open() {
    state.error = "";
    opening = true;
    earlyOutput = [];
    render();
    try {
      const result = await call("terminal.open", { rows: 24, columns: 80 });
      session = result.session;
      opening = false;
      closing = false; statusWarning = false;
      state.closed = false;
      render();
      for (const event of earlyOutput) {
        if (event.id !== session.id) continue;
        const bytes = Uint8Array.from(atob(event.data || ""), c => c.charCodeAt(0));
        terminal?.write(bytes);
      }
      earlyOutput = [];
      checkSessionStatus(session.id);
      terminal?.focus();
    } catch (error) { opening = false; earlyOutput = []; state.error = error.message; render(); }
  }
  async function close() {
    if (!session || closing) return;
    const id = session.id;
    closing = true;
    render();
    try {
      await call("terminal.close", { id, force: false });
      cleanup(); session = null; state.closed = true; closing = false;
      render();
    } catch (error) {
      closing = false; state.error = error.message; render(); checkSessionStatus(id);
    }
  }
  function checkSessionStatus(id, delay = 1000) {
    clearTimeout(statusTimer);
    statusTimer = setTimeout(async () => {
      if (session?.id !== id || closing) return;
      try {
        const status = await call("terminal.status", { id });
        statusWarning = false;
        if (status.state === "exited") {
          terminal?.write(`\r\n[session ${status.reason || "exited"}${status.exitCode == null ? "" : ", exit " + status.exitCode}]\r\n`);
          session = null; cleanup(); render(); return;
        }
      } catch (error) {
        if (!statusWarning) terminal?.write(`\r\n[session status temporarily unavailable: ${error.message}]\r\n`);
        statusWarning = true;
        checkSessionStatus(id, 5000); return;
      }
      checkSessionStatus(id);
    }, delay);
  }
  function fit() {
    if (!terminal || !session) return;
    try {
      terminal.fitAddon.fit();
      void call("terminal.resize", { id: session.id, rows: terminal.rows, columns: terminal.cols }).catch(error => { state.error = error.message; render(); });
    } catch (_) { /* hidden navigation pages have no measurable size */ }
  }
  function attachTerminal() {
    const host = page.querySelector(".terminal-plugin-screen");
    if (!host || !window.Terminal || !window.FitAddon) {
      state.error = "The terminal display component is unavailable.";
      return;
    }
    terminal = new window.Terminal({ convertEol: true, cursorBlink: true, fontSize: 13, scrollback: 2000 });
    terminal.fitAddon = new window.FitAddon.FitAddon();
    terminal.loadAddon(terminal.fitAddon);
    terminal.open(host);
    terminal.onData(data => {
      if (!session) return;
      const bytes = new TextEncoder().encode(data);
      let binary = ""; bytes.forEach(value => { binary += String.fromCharCode(value); });
      void call("terminal.write", { id: session.id, data: btoa(binary) }).catch(error => { state.error = error.message; render(); });
    });
    resizeObserver = new ResizeObserver(fit);
    resizeObserver.observe(host);
    fit();
  }
  runpilot.navigation.register({
    id: "terminal-plugin", title: "Terminal (plugin)", icon: ">_",
    render: root => { cleanup(); page = root; render(); },
  });
  // Subscribe for the whole activation lifetime so fast process output cannot
  // arrive between session creation and drawing the terminal page.
  observers = [
    runpilot.ws.on(PLUGIN, "process.session.output", event => {
      if (!session && opening) {
        if (earlyOutput.length < 64) earlyOutput.push(event);
        else state.error = "Terminal output arrived before the session opened and exceeded the temporary buffer.";
        return;
      }
      if (event?.id !== session?.id || !terminal) return;
      const bytes = Uint8Array.from(atob(event.data || ""), c => c.charCodeAt(0));
      terminal.write(bytes);
    }),
    runpilot.ws.on(PLUGIN, "process.session.error", event => {
      if (event?.id === session?.id) terminal?.write(`\r\n[session error: ${event.message || "I/O failed"}]\r\n`);
    }),
      runpilot.ws.on(PLUGIN, "process.session.exit", event => {
        if (event?.id !== session?.id) return;
        terminal?.write(`\r\n[session ${event.reason || event.state}${event.exitCode == null ? "" : ", exit " + event.exitCode}]\r\n`);
        cleanup(); session = null; state.closed = true; render();
    }),
  ];
  return () => { observers.forEach(off => off()); observers = []; if (session) void call("terminal.close", { id: session.id, force: true }); cleanup(); };
}
