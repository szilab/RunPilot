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
  if (!window.Terminal) await loadScript(new URL("xterm.js", base).href);
  if (!window.FitAddon) await loadScript(new URL("addon-fit.js", base).href);
}

export async function activate(runpilot) {
  await loadTerminalAssets();
  let page = null;
  let sessions = [];
  let activeID = "";
  let opening = false;
  let earlyOutput = [];
  let observers = [];
  const state = { error: "" };
  const ui = runpilot.ui;
  const escape = ui.escape;

  async function call(method, params = {}) {
    const result = await runpilot.ws.call(PLUGIN, method, params);
    if (result?.error) throw new Error(result.error.message || "Terminal request failed");
    return result;
  }
  function active() { return sessions.find(item => item.session.id === activeID); }
  function cleanupTerminal(item) {
    item?.resizeObserver?.disconnect();
    item?.terminal?.dispose();
    if (item) { item.resizeObserver = null; item.terminal = null; }
  }
  function render() {
    if (!page?.isConnected) return;
    page.innerHTML = `<section class="terminal-plugin">
      <div class="terminal-plugin-toolbar"><div class="terminal-plugin-tabs" role="tablist" aria-label="Terminal sessions"></div>
      <div class="row-actions"><button class="button primary small" data-action="open" ${opening ? "disabled" : ""}>${opening ? "Opening…" : "New session"}</button><button class="button secondary small" data-action="close" ${active() ? "" : "disabled"}>Close session</button></div></div>
      ${state.error ? `<div class="notice" role="alert">${escape(state.error)}</div>` : ""}
      <div class="terminal-plugin-panes"></div>
    </section>`;
    const tabs = page.querySelector(".terminal-plugin-tabs");
    const panes = page.querySelector(".terminal-plugin-panes");
    for (const item of sessions) {
      const id = item.session.id;
      const tab = document.createElement("div");
      tab.className = `terminal-plugin-tab${id === activeID ? " active" : ""}`;
      tab.setAttribute("role", "tab"); tab.setAttribute("aria-selected", id === activeID ? "true" : "false");
      const label = document.createElement("button"); label.type = "button";
      label.textContent = item.session.command || `Shell ${sessions.indexOf(item) + 1}`;
      label.addEventListener("click", () => { activeID = id; render(); item.terminal?.focus(); fit(item); });
      const closeTab = document.createElement("button"); closeTab.type = "button"; closeTab.className = "terminal-plugin-tab-close"; closeTab.textContent = "×";
      closeTab.setAttribute("aria-label", "Close terminal session"); closeTab.addEventListener("click", () => close(id));
      tab.append(label, closeTab); tabs.append(tab);
      const pane = document.createElement("div"); pane.className = "terminal-plugin-screen";
      pane.hidden = id !== activeID; pane.setAttribute("aria-label", "Terminal output"); panes.append(pane);
      if (!item.terminal) attachTerminal(item, pane);
      else pane.append(item.terminal.element);
      if (id === activeID) queueMicrotask(() => { fit(item); item.terminal?.focus(); });
    }
    page.querySelector('[data-action="open"]').addEventListener("click", open);
    page.querySelector('[data-action="close"]').addEventListener("click", () => close(activeID));
  }
  async function open() {
    if (opening) return;
    opening = true; state.error = ""; earlyOutput = []; render();
    try {
      const result = await call("terminal.open", { rows: 24, columns: 80 });
      const item = { session: result.session, terminal: null, resizeObserver: null };
      sessions.push(item); activeID = item.session.id;
      opening = false; render();
      for (const event of earlyOutput) if (event.id === activeID) writeOutput(item, event.data);
      earlyOutput = [];
      checkSessionStatus(item);
    } catch (error) { opening = false; earlyOutput = []; state.error = error.message; render(); }
  }
  async function close(id) {
    const item = sessions.find(entry => entry.session.id === id);
    if (!item) return;
    state.error = "";
    try {
      await call("terminal.close", { id, force: false });
      clearTimeout(item.statusTimer); cleanupTerminal(item);
      sessions = sessions.filter(entry => entry !== item);
      if (activeID === id) activeID = sessions[0]?.session.id || "";
      render();
    } catch (error) { state.error = error.message; render(); checkSessionStatus(item); }
  }
  function writeOutput(item, data) {
    const bytes = Uint8Array.from(atob(data || ""), c => c.charCodeAt(0));
    item.terminal?.write(bytes);
  }
  function checkSessionStatus(item, delay = 1000) {
    clearTimeout(item.statusTimer);
    item.statusTimer = setTimeout(async () => {
      if (!sessions.includes(item)) return;
      try {
        const status = await call("terminal.status", { id: item.session.id });
        if (status.state === "exited") {
          item.terminal?.write(`\r\n[session ${status.reason || "exited"}${status.exitCode == null ? "" : ", exit " + status.exitCode}]\r\n`);
          sessions = sessions.filter(entry => entry !== item); cleanupTerminal(item);
          if (activeID === item.session.id) activeID = sessions[0]?.session.id || "";
          render(); return;
        }
      } catch (error) {
        item.terminal?.write(`\r\n[session status temporarily unavailable: ${error.message}]\r\n`);
        checkSessionStatus(item, 5000); return;
      }
      checkSessionStatus(item);
    }, delay);
  }
  function fit(item) {
    if (!item?.terminal || activeID !== item.session.id) return;
    try {
      item.terminal.fitAddon.fit();
      void call("terminal.resize", { id: item.session.id, rows: item.terminal.rows, columns: item.terminal.cols }).catch(error => { state.error = error.message; render(); });
    } catch (_) { /* hidden navigation pages have no measurable size */ }
  }
  function attachTerminal(item, host) {
    item.terminal = new window.Terminal({ convertEol: true, cursorBlink: true, fontSize: 13, scrollback: 2000, allowTransparency: true, theme: { background: "transparent" } });
    item.terminal.fitAddon = new window.FitAddon.FitAddon();
    item.terminal.loadAddon(item.terminal.fitAddon);
    item.terminal.open(host);
    item.terminal.onData(data => {
      const bytes = new TextEncoder().encode(data);
      let binary = ""; bytes.forEach(value => { binary += String.fromCharCode(value); });
      void call("terminal.write", { id: item.session.id, data: btoa(binary) }).catch(error => { state.error = error.message; render(); });
    });
    item.resizeObserver = new ResizeObserver(() => fit(item));
    item.resizeObserver.observe(host);
  }
  runpilot.navigation.register({
    id: "terminal-plugin", title: "Terminal", icon: ">_",
    render: root => {
      page = root;
      render();
      if (!sessions.length && !opening) void open();
    },
  });
  observers = [
    runpilot.ws.on(PLUGIN, "process.session.output", event => {
      const item = sessions.find(entry => entry.session.id === event?.id);
      if (item) writeOutput(item, event.data);
      else if (opening && earlyOutput.length < 64) earlyOutput.push(event);
    }),
    runpilot.ws.on(PLUGIN, "process.session.error", event => {
      const item = sessions.find(entry => entry.session.id === event?.id);
      item?.terminal?.write(`\r\n[session error: ${event.message || "I/O failed"}]\r\n`);
    }),
    runpilot.ws.on(PLUGIN, "process.session.exit", event => {
      const item = sessions.find(entry => entry.session.id === event?.id);
      if (!item) return;
      item.terminal?.write(`\r\n[session ${event.reason || event.state}${event.exitCode == null ? "" : ", exit " + event.exitCode}]\r\n`);
      clearTimeout(item.statusTimer); cleanupTerminal(item);
      sessions = sessions.filter(entry => entry !== item);
      if (activeID === item.session.id) activeID = sessions[0]?.session.id || "";
      render();
    }),
  ];
  return () => {
    observers.forEach(off => off()); observers = [];
    sessions.forEach(item => { clearTimeout(item.statusTimer); void call("terminal.close", { id: item.session.id, force: true }); cleanupTerminal(item); });
    sessions = []; page = null;
  };
}
