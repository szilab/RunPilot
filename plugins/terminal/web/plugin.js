const PLUGIN = "terminal";

export function activate(runpilot) {
  let page = null;
  let terminal = null;
  let session = null;
  let opening = false;
  let earlyOutput = [];
  let observers = [];
  let resizeObserver = null;
  const state = { error: "", closed: true };
  const ui = runpilot.ui;
  const escape = ui.escape;

  async function call(method, params = {}) {
    const result = await runpilot.ws.call(PLUGIN, method, params);
    if (result?.error) throw new Error(result.error.message || "Terminal request failed");
    return result;
  }
  function cleanup() {
    resizeObserver?.disconnect(); resizeObserver = null;
    terminal?.dispose(); terminal = null;
  }
  function render() {
    if (!page?.isConnected) return;
    const existingScreen = terminal?.element?.closest(".terminal-plugin-screen");
    page.innerHTML = `<section class="terminal-plugin">
      <div class="section-head"><div><h2>Terminal (plugin)</h2><p class="meta">Experimental interactive shell</p></div>
      <div class="row-actions"><button class="button primary small" data-action="open" ${session ? "disabled" : ""}>Open session</button><button class="button secondary small" data-action="close" ${session ? "" : "disabled"}>Close session</button></div></div>
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
    try {
      const result = await call("terminal.open", { rows: 24, columns: 80 });
      session = result.session;
      opening = false;
      state.closed = false;
      render();
      for (const event of earlyOutput) {
        if (event.id !== session.id) continue;
        const bytes = Uint8Array.from(atob(event.data || ""), c => c.charCodeAt(0));
        terminal?.write(bytes);
      }
      earlyOutput = [];
      terminal?.focus();
    } catch (error) { opening = false; earlyOutput = []; state.error = error.message; render(); }
  }
  async function close() {
    if (!session) return;
    const id = session.id;
    cleanup();
    session = null; state.closed = true;
    render();
    try { await call("terminal.close", { id, force: false }); }
    catch (error) { state.error = error.message; render(); }
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
      terminal?.write(`\r\n[session ${event.reason || event.state}]\r\n`);
      cleanup(); session = null; state.closed = true; render();
    }),
  ];
  return () => { observers.forEach(off => off()); observers = []; if (session) void call("terminal.close", { id: session.id, force: true }); cleanup(); };
}
