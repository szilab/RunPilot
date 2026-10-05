const $ = (id) => document.getElementById(id);
let token = RunPilotAuthStorage.migrate(sessionStorage, localStorage);
let currentPage = "overview";
let processes = [];
let jobs = [];
let taskFilter = "all";
let storage = [], storageLocation = null, storagePath = "";
let softwareProviders = [], softwareProviderID = "", softwarePackages = [], softwareUpdates = [], softwareSearchResults = [], softwareBuckets = [], softwareTab = "installed", softwareBusy = false, softwareBusyLabel = "", softwareLoading = false, softwareLoadingKey = "", softwareLoadedKey = "", softwareLoadSequence = 0, softwareRootDrafts = {};
let storageClipboard = null, editingTextPath = null, storageShowHidden = false, storagePathCapabilities = null;
let overview = null;
let systemInfo = null;
let dockerRuntime = null, dockerProjects = [], dockerVolumes = [], dockerNetworks = [], dockerBusy = new Set(), dockerPendingContainerStates = new Map(), dockerProjectErrors = new Map(), dockerEditing = null, dockerAttachTerminal = null;
let pluginStatuses = [], pluginBusy = new Set();
let pluginDiscoveryErrors = [], pluginRestartRequired = false;
let pluginCatalog = [], pluginCatalogError = "", pluginCatalogLoaded = false, pluginCatalogLoading = false;
let pluginExtensions = new Map();
const pluginNavigation = new Map(), pluginOverview = new Map(), pluginSettings = new Map();
const pluginThemeListeners = new Set();
let applicationSocket = null, applicationSocketPromise = null, applicationSequence = 0;
const applicationPending = new Map(), applicationListeners = new Map(), applicationStreams = new Map();
let logTimer = null, toastTimer = null;
let logSource = null;
let refreshTimer = null;
let refreshing = false;
let connectionTimer = null, connectionOnline = false, connectionChecked = false, connectionWasLost = false, reloadingAfterReconnect = false;
const themeStorageKey = "runpilot.theme";
const sidebarStorageKey = "runpilot.sidebar-collapsed";

const pageMeta = {
  overview: ["Overview", null],
  settings: ["Settings", null],
};

function applicationWebSocketURL(ticket) { const url=new URL("api/v1/ws",document.baseURI); url.protocol=url.protocol === "https:" ? "wss:" : "ws:"; url.searchParams.set("ticket",ticket); return url; }
async function connectApplicationSocket() {
  if (applicationSocket?.readyState === WebSocket.OPEN) return applicationSocket;
  if (applicationSocketPromise) return applicationSocketPromise;
  applicationSocketPromise=(async()=>{
    const ticket=await api("api/v1/ws/ticket",{method:"POST"});
    const socket=new RunPilotSecureWebSocket(applicationWebSocketURL(ticket.ticket),systemInfo?.websocketPayloadMode||"disabled");
    await new Promise((resolve,reject)=>{ const timer=setTimeout(()=>reject(new Error("application WebSocket timed out")),10000); socket.onopen=()=>{clearTimeout(timer);resolve();}; socket.onerror=()=>{clearTimeout(timer);reject(new Error("application WebSocket connection failed"));}; });
    socket.onmessage=event=>{ if(typeof event.data!=="string"){let bytes;try{bytes=new Uint8Array(event.data);}catch{return;}if(bytes.length<7||bytes[0]!==82||bytes[1]!==80||bytes[2]!==83||bytes[3]!==49||bytes[4]!==2)return;const idLength=bytes[5],id=new TextDecoder().decode(bytes.slice(6,6+idLength)),stream=applicationStreams.get(id);if(stream?.opened)stream.ondata?.(bytes.slice(6+idLength));return;}let message;try{message=JSON.parse(event.data);}catch{return;}if(message.type==="stream.attached"){const stream=applicationStreams.get(message.streamId);if(stream&&!stream.opened){stream.opened=true;clearTimeout(stream.timer);stream.resolve(stream.handle);}return;}if(message.type==="stream.error"||message.type==="stream.closed"){const stream=applicationStreams.get(message.streamId);if(stream){applicationStreams.delete(message.streamId);clearTimeout(stream.timer);const error=new Error(message.code||"stream closed");if(!stream.opened)stream.reject(error);else stream.onclose?.({code:message.code||"closed",reason:message.code||"stream closed"});}return;}if(message.id){const pending=applicationPending.get(message.id);if(!pending)return;applicationPending.delete(message.id);clearTimeout(pending.timer);if(message.error){const error=new Error(message.error.message||"Plugin request failed");error.code=message.error.code;pending.reject(error);}else pending.resolve(message.result);return;}if(message.plugin&&message.event)for(const listener of applicationListeners.get(`${message.plugin}:${message.event}`)||[])listener(message.data);};
    socket.onclose=event=>{ if(applicationSocket===socket) { applicationSocket=null; applicationSocketPromise=null; for(const [id,pending] of applicationPending){clearTimeout(pending.timer);pending.reject(new Error("application WebSocket disconnected"));applicationPending.delete(id);} for(const [id,stream] of applicationStreams){applicationStreams.delete(id);clearTimeout(stream.timer);if(!stream.opened)stream.reject(new Error("application WebSocket disconnected"));else stream.onclose?.(event);} setTimeout(()=>connectApplicationSocket().catch(()=>{}),1000); } };
    applicationSocket=socket; applicationSocketPromise=null; return socket;
  })();
  try{return await applicationSocketPromise;}catch(error){applicationSocketPromise=null;throw error;}
}
const pluginWS=Object.freeze({
  call: async (plugin,method,params={})=>{ const socket=await connectApplicationSocket(); const id=String(++applicationSequence); return new Promise((resolve,reject)=>{const timer=setTimeout(()=>{applicationPending.delete(id);reject(new Error("plugin request timed out"));},10000); applicationPending.set(id,{resolve,reject,timer}); socket.send(JSON.stringify({id,plugin,method,params}));}); },
  on: (plugin,event,listener)=>{ const key=`${plugin}:${event}`, listeners=applicationListeners.get(key)||new Set(); listeners.add(listener); applicationListeners.set(key,listeners); return ()=>{listeners.delete(listener);if(!listeners.size)applicationListeners.delete(key);}; },
  openStream: async (plugin,streamId)=>{ if(typeof plugin!=="string"||!plugin||typeof streamId!=="string"||!streamId||new TextEncoder().encode(streamId).length>255)throw new TypeError("plugin and stream ID are required");if(applicationStreams.has(streamId))throw new Error("stream is already attached");if(applicationStreams.size>=64)throw new Error("application stream limit reached");const socket=await connectApplicationSocket();return new Promise((resolve,reject)=>{const stream={plugin,opened:false,ondata:null,onclose:null,resolve,reject,timer:null,handle:null};const handle={get ondata(){return stream.ondata;},set ondata(value){stream.ondata=value;},get onclose(){return stream.onclose;},set onclose(value){stream.onclose=value;},send(value){if(!stream.opened||socket.readyState!==WebSocket.OPEN)throw new Error("stream is not open");const bytes=value instanceof Uint8Array?value:value instanceof ArrayBuffer?new Uint8Array(value):ArrayBuffer.isView(value)?new Uint8Array(value.buffer,value.byteOffset,value.byteLength):null;if(!bytes)throw new TypeError("stream data must be binary");if(bytes.length>32768)throw new RangeError("stream frames are limited to 32768 bytes");const idBytes=new TextEncoder().encode(streamId),frame=new Uint8Array(6+idBytes.length+bytes.length);frame.set([82,80,83,49,1,idBytes.length]);frame.set(idBytes,6);frame.set(bytes,6+idBytes.length);socket.send(frame);},close(){if(!applicationStreams.has(streamId))return;applicationStreams.delete(streamId);clearTimeout(stream.timer);if(socket.readyState===WebSocket.OPEN)socket.send(JSON.stringify({type:"stream.close",streamId}));stream.onclose?.({code:1000,reason:"closed"});}};stream.handle=handle;stream.timer=setTimeout(()=>{if(applicationStreams.delete(streamId))reject(new Error("stream attachment timed out"));},10000);applicationStreams.set(streamId,stream);socket.send(JSON.stringify({type:"stream.attach",plugin,streamId}));}); },
});
function pluginTheme() {
  const style = getComputedStyle(document.documentElement);
  const token = name => style.getPropertyValue(`--rp-${name}`).trim();
  const colors = {
    surface: token("surface"), surfaceElevated: token("surface-elevated"),
    text: token("text"), textStrong: token("text-strong"), textMuted: token("text-muted"),
    border: token("border"), accent: token("accent"), selection: token("selection"),
  };
  for (const color of ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white", "bright-black", "bright-red", "bright-green", "bright-yellow", "bright-blue", "bright-magenta", "bright-cyan", "bright-white"]) {
    colors[`terminal${color.split("-").map(word => word[0].toUpperCase() + word.slice(1)).join("")}`] = token(`terminal-${color}`);
  }
  return Object.freeze({ scheme: document.documentElement.dataset.scheme || "system", resolvedScheme: document.documentElement.dataset.theme || "light", fontFamily: token("font-mono"), colors: Object.freeze(colors) });
}
function notifyPluginThemeListeners() {
  const theme = pluginTheme();
  for (const listener of pluginThemeListeners) {
    try { listener(theme); } catch (error) { console.error("plugin theme listener", error); }
  }
}
function createInteractiveSessionView({container,title="Interactive session",onBack,onDisconnect,onFullscreenChange}={}) {
  if(!container||typeof container.replaceChildren!=="function") throw new TypeError("interactive session container is required");
  const element=document.createElement("section");element.className="rp-interactive-session";
  const toolbar=document.createElement("div");toolbar.className="rp-interactive-toolbar";
  const left=document.createElement("div");left.className="rp-interactive-heading";
  const back=document.createElement("button");back.type="button";back.className="button secondary small";back.textContent="Back";back.addEventListener("click",()=>onBack?.());
  const heading=document.createElement("div");heading.className="rp-interactive-title";
  const titleNode=document.createElement("strong");titleNode.textContent=String(title);
  const status=document.createElement("span");status.className="rp-interactive-status";status.textContent="Connecting";status.setAttribute("role","status");status.setAttribute("aria-live","polite");
  heading.append(titleNode,status);
  const actions=document.createElement("div");actions.className="rp-interactive-actions";
  const fullscreen=document.createElement("button");fullscreen.type="button";fullscreen.className="button secondary small";fullscreen.textContent="Fullscreen";fullscreen.setAttribute("aria-pressed","false");
  const disconnect=document.createElement("button");disconnect.type="button";disconnect.className="button danger small";disconnect.textContent="Disconnect";disconnect.addEventListener("click",()=>onDisconnect?.());
  actions.append(fullscreen,disconnect);left.append(back,heading);toolbar.append(left,actions);
  const surfaceElement=document.createElement("div");surfaceElement.className="rp-interactive-surface";surfaceElement.tabIndex=0;surfaceElement.setAttribute("role","application");surfaceElement.setAttribute("aria-label",`${title} interactive surface`);
  const loading=document.createElement("div");loading.className="rp-interactive-loading";loading.setAttribute("role","status");loading.textContent="Connecting…";
  const error=document.createElement("div");error.className="rp-interactive-error";error.setAttribute("role","alert");error.hidden=true;
  surfaceElement.append(loading,error);element.append(toolbar,surfaceElement);container.replaceChildren(element);
  let disposed=false,frame=0,resizeTimer=0,lastSize=null,fullscreenState=null;
  const resizeListeners=new Set(),fullscreenListeners=new Set();
  const measure=()=>({width:Math.max(0,Math.round(surfaceElement.clientWidth)),height:Math.max(0,Math.round(surfaceElement.clientHeight))});
  const fitViewport=()=>{if(disposed)return;if(isFullscreen()){element.style.height="";return;}const main=element.closest("main"),bottom=main?parseFloat(getComputedStyle(main).paddingBottom)||0:0,height=Math.max(300,Math.floor(window.innerHeight-element.getBoundingClientRect().top-bottom));if(element.style.height!==`${height}px`)element.style.height=`${height}px`;};
  const reportSize=()=>{frame=0;if(disposed)return;const size=measure();if(size.width<1||size.height<1)return;if(lastSize&&lastSize.width===size.width&&lastSize.height===size.height)return;lastSize=size;for(const listener of resizeListeners)listener({...size});};
  const scheduleSize=()=>{if(disposed)return;if(frame)cancelAnimationFrame(frame);frame=requestAnimationFrame(()=>{frame=0;fitViewport();clearTimeout(resizeTimer);resizeTimer=setTimeout(reportSize,60);});};
  const isFullscreen=()=>document.fullscreenElement===element||element.contains(document.fullscreenElement);
  const reportFullscreen=()=>{const next=isFullscreen();if(fullscreenState===next)return;fullscreenState=next;fullscreen.setAttribute("aria-pressed",String(next));fullscreen.textContent=next?"Exit fullscreen":"Fullscreen";for(const listener of fullscreenListeners)listener(next);onFullscreenChange?.(next);scheduleSize();if(!disposed)surfaceElement.focus({preventScroll:true});};
  const resizeObserver=typeof ResizeObserver==="undefined"?null:new ResizeObserver(scheduleSize);
  resizeObserver?.observe(surfaceElement);resizeObserver?.observe(toolbar);resizeObserver?.observe(element);
  window.addEventListener("resize",scheduleSize);document.addEventListener("fullscreenchange",reportFullscreen);
  surfaceElement.addEventListener("pointerdown",()=>surfaceElement.focus({preventScroll:true}));
  fullscreen.addEventListener("click",async()=>{try{if(isFullscreen()){await document.exitFullscreen();}else await element.requestFullscreen();}catch(cause){setError(`Fullscreen unavailable: ${cause?.message||"request failed"}`);}});
  function setStatus(state,label){const names={connecting:"Connecting",connected:"Connected",disconnected:"Disconnected",error:"Error"};status.dataset.state=names[state]?state:"connecting";status.textContent=label||names[state]||names.connecting;}
  function setLoading(value,label="Connecting…"){loading.textContent=label;loading.hidden=!value;}
  function setError(message=""){error.textContent=String(message||"");error.hidden=!message;setLoading(false);if(message)setStatus("error");}
  const surface={
    element:surfaceElement,
    getSize:measure,
    onResize(listener){if(typeof listener!=="function")throw new TypeError("resize listener must be a function");resizeListeners.add(listener);scheduleSize();return()=>resizeListeners.delete(listener);},
    fitScale(content){const width=Number(content?.width)||0,height=Number(content?.height)||0,size=measure();return width>0&&height>0&&size.width>0&&size.height>0?Math.min(size.width/width,size.height/height):null;},
    enterFullscreen:async()=>{if(!isFullscreen())await element.requestFullscreen();},
    exitFullscreen:async()=>{if(isFullscreen())await document.exitFullscreen();},
    get isFullscreen(){return isFullscreen();},
    onFullscreenChange(listener){if(typeof listener!=="function")throw new TypeError("fullscreen listener must be a function");fullscreenListeners.add(listener);return()=>fullscreenListeners.delete(listener);},
    focus(){surfaceElement.focus({preventScroll:true});},
    dispose(){view.dispose();},
  };
  const view={element,surface,actions,setStatus,setLoading,setError,dispose(){if(disposed)return;disposed=true;if(frame)cancelAnimationFrame(frame);clearTimeout(resizeTimer);resizeObserver?.disconnect();window.removeEventListener("resize",scheduleSize);document.removeEventListener("fullscreenchange",reportFullscreen);resizeListeners.clear();fullscreenListeners.clear();element.remove();}};
  scheduleSize();return view;
}
function pluginUI() {
  return Object.freeze({
    escape: escapeHtml,
    toast,
    createInteractiveSessionView,
    theme: Object.freeze({
      get: pluginTheme,
      subscribe: listener => {
        if (typeof listener !== "function") throw new TypeError("theme listener must be a function");
        pluginThemeListeners.add(listener);
        return () => pluginThemeListeners.delete(listener);
      },
    }),
    Card: ({title="",body="",className=""}={}) => {
      const card = document.createElement("article");
      card.className = `metric ${className}`;
      card.innerHTML = `<span>${escapeHtml(title)}</span>${body}`;
      return card;
    },
    SectionTitle: title => {
      const head = document.createElement("div");
      head.className = "section-head";
      head.innerHTML = `<h2>${escapeHtml(title)}</h2>`;
      return head;
    },
    EmptyState: ({title="Nothing here",message=""}={}) => {
      const root = document.createElement("div");
      root.className = "empty";
      root.innerHTML = `<h2>${escapeHtml(title)}</h2><p>${escapeHtml(message)}</p>`;
      return root;
    },
  });
}
function pluginNavigationIcon(icon) {
  if (icon && typeof icon === "object" && typeof icon.src === "string") return `<img class="nav-icon nav-icon-image" src="${escapeHtml(icon.src)}" alt="" aria-hidden="true">`;
  if (icon === "monitor") return '<svg class="nav-icon" viewBox="0 0 24 24" aria-hidden="true"><rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4"/></svg>';
  return `<span class="nav-icon" aria-hidden="true">${escapeHtml(icon || "•")}</span>`;
}
function registerPluginNavigation(extension, entry) {
  if (!entry || typeof entry.id !== "string" || !entry.id || typeof entry.render !== "function" || (entry.headerActions !== undefined && typeof entry.headerActions !== "function") || pluginNavigation.has(entry.id) || $(entry.id + "Page")) throw new Error("invalid or duplicate plugin navigation entry");
  const button = document.createElement("button"); button.className = "nav"; button.type = "button"; button.dataset.page = entry.id; button.title = entry.title || entry.id; button.innerHTML = `${pluginNavigationIcon(entry.icon)}<span class="nav-label">${escapeHtml(entry.title || entry.id)}</span>`;
  document.querySelector("nav").append(button);
  const page = document.createElement("section"); page.id = entry.id + "Page"; page.className = "page"; document.querySelector("main").append(page);
  pluginNavigation.set(entry.id, { ...entry, button, page, extension });
  button.addEventListener("click", () => setPage(entry.id));
}
function registerPluginOverview(extension, entry) { if(!entry||typeof entry.id!=="string"||!entry.id||typeof entry.render!=="function"||pluginOverview.has(entry.id)) throw new Error("invalid or duplicate overview card"); pluginOverview.set(entry.id,{...entry,extension}); }
function registerPluginSettings(extension, entry) { if(!entry||typeof entry.id!=="string"||!entry.id||typeof entry.render!=="function"||pluginSettings.has(entry.id)) throw new Error("invalid or duplicate settings section"); pluginSettings.set(entry.id,{...entry,extension}); }

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Authorization", `Bearer ${token}`);
  if (options.body && !(options.body instanceof FormData) && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const res = await fetch(path, {...options, headers});
  if (res.status === 401) {
    const error = new Error("Unauthorized"); error.serverReachable = true;
    throw error;
  }
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try { message = (await res.json()).error || message; } catch {}
    const error = new Error(message); error.serverReachable = true;
    throw error;
  }
  if (res.status === 204) return null;
  const ct = res.headers.get("content-type") || "";
  return ct.includes("application/json") ? res.json() : res.text();
}

function setConnected(ok) {
  if (!ok && (connectionOnline || connectionChecked)) connectionWasLost = true;
  connectionChecked = true;
  const restored = ok && connectionWasLost;
  connectionOnline = ok;
  $("connectionDot").classList.toggle("ok", ok);
  const mode = systemInfo?.websocketPayloadMode || "disabled";
  const encryptionStatus = mode === "required" ? "Encrypted" : "Normal";
  $("connectionWarning").classList.toggle("hidden", location.protocol !== "http:");
  $("connectionText").textContent = ok ? `Connected · ${encryptionStatus}` : "Offline";
  $("connectionDot").title = ok ? encryptionStatus : "RunPilot is offline";
  $("connectionText").title = ok ? encryptionStatus : "RunPilot is offline";
  if (restored && !reloadingAfterReconnect) {
    reloadingAfterReconnect = true;
    window.location.reload();
  }
}

function applyTheme(theme) {
  const scheme = ["system","light","dark"].includes(theme) ? theme : "system";
  const isDark = scheme === "system" ? matchMedia("(prefers-color-scheme: dark)").matches : scheme === "dark";
  document.documentElement.dataset.style = "runpilot-default";
  document.documentElement.dataset.theme = isDark ? "dark" : "light";
  document.documentElement.dataset.scheme = scheme;
  localStorage.setItem(themeStorageKey, scheme);
  const toggle = $("themeToggle");
  toggle.setAttribute("aria-pressed", String(isDark));
  toggle.title = `Color scheme: ${scheme}. Click to change.`;
  toggle.innerHTML = `${isDark ? "☾" : "☀"} <span>${scheme === "system" ? "System theme" : `${scheme[0].toUpperCase()+scheme.slice(1)} theme`}</span>`;
  notifyPluginThemeListeners();
}

function initializeTheme() {
  const saved = localStorage.getItem(themeStorageKey);
  applyTheme(saved || "system");
  $("themeToggle").addEventListener("click", () => {
    const current=document.documentElement.dataset.scheme||"system";
    applyTheme(current === "system" ? "light" : current === "light" ? "dark" : "system");
  });
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => { if(document.documentElement.dataset.scheme === "system") applyTheme("system"); });
}

function setSidebarCollapsed(collapsed) {
  const shell=document.querySelector(".shell"), changing=shell.classList.contains("sidebar-collapsed")!==collapsed;
  shell.classList.toggle("sidebar-collapsed", collapsed);
  $("sidebarToggle").setAttribute("aria-expanded", String(!collapsed));
  const label = collapsed ? "Expand sidebar" : "Collapse sidebar";
  $("sidebarToggle").title = label;
  $("sidebarToggle").setAttribute("aria-label", label);
  localStorage.setItem(sidebarStorageKey, String(collapsed));
}

function initializeSidebar() {
  setSidebarCollapsed(localStorage.getItem(sidebarStorageKey) === "true");
  $("sidebarToggle").addEventListener("click", () => setSidebarCollapsed(!document.querySelector(".shell").classList.contains("sidebar-collapsed")));
}

function toast(message, level = "info") {
  const el = $("toast");
  clearTimeout(toastTimer);
  el.replaceChildren();
  const content=document.createElement("div"), title=document.createElement("strong"), detail=document.createElement("span"), close=document.createElement("button");
  title.textContent=level === "error" ? "Error" : "Notice"; detail.textContent=message;
  content.append(title,detail);
  close.type="button"; close.className="toast-close"; close.textContent="×"; close.setAttribute("aria-label","Dismiss notification"); close.addEventListener("click",()=>el.classList.add("hidden"));
  el.append(content,close); el.classList.toggle("toast-error",level === "error"); el.setAttribute("role",level === "error" ? "alert" : "status"); el.classList.remove("hidden");
  toastTimer=setTimeout(()=>el.classList.add("hidden"),level === "error" ? 10000 : 4000);
}
function toastError(message) { toast(message,"error"); }

function splitArgs(text) {
  const out = [];
  let cur = "", quote = null, escape = false;
  for (const ch of text.trim()) {
    if (escape) { cur += ch; escape = false; continue; }
    if (ch === "\\") { escape = true; continue; }
    if (quote) {
      if (ch === quote) quote = null; else cur += ch;
    } else if (ch === '"' || ch === "'") quote = ch;
    else if (/\s/.test(ch)) { if (cur) { out.push(cur); cur = ""; } }
    else cur += ch;
  }
  if (cur) out.push(cur);
  return out;
}

function formatArgs(args = []) {
  return args.map(a => /\s/.test(a) ? `"${a.replaceAll('"', '\\"')}"` : a).join(" ");
}

function setCommandError(prefix, message = "") {
  const error = $(`${prefix}CommandError`);
  error.textContent = message;
  error.classList.toggle("hidden", !message);
}

function updateEnvironmentEmpty(prefix) {
  const hasRows = $(`${prefix}EnvRows`).children.length > 0;
  $(`${prefix}EnvEmpty`).classList.toggle("hidden", hasRows);
}

function addEnvironmentRow(prefix, name = "", value = "", focusName = false) {
  const row = document.createElement("div");
  row.className = "environment-row";
  row.innerHTML = `<input class="environment-name" aria-label="Variable name" placeholder="NAME">
    <input class="environment-value" aria-label="Variable value" placeholder="Value">
    <button class="button secondary small environment-remove" type="button" title="Remove variable" aria-label="Remove variable">Remove</button>`;
  row.querySelector(".environment-name").value = name;
  row.querySelector(".environment-value").value = value;
  row.querySelector(".environment-remove").addEventListener("click", () => {
    row.remove();
    updateEnvironmentEmpty(prefix);
  });
  $(`${prefix}EnvRows`).append(row);
  updateEnvironmentEmpty(prefix);
  if (focusName) row.querySelector(".environment-name").focus();
}

function populateCommandEditor(prefix, command = {}) {
  $(`${prefix}Path`).value = command.path || "";
  $(`${prefix}Args`).value = formatArgs(command.args || []);
  $(`${prefix}Cwd`).value = command.workingDirectory || "";
  $(`${prefix}Interpreter`).value = command.interpreter || "auto";
  updateCommandEditorMode(prefix);
  $(`${prefix}EnvRows`).replaceChildren();
  Object.keys(command.environment || {}).sort((a, b) => a.localeCompare(b)).forEach(name => {
    addEnvironmentRow(prefix, name, command.environment[name]);
  });
  updateEnvironmentEmpty(prefix);
  setCommandError(prefix);
	if (systemInfo?.capabilities) configurePlatformAwareFields(systemInfo.capabilities);
}

function updateCommandEditorMode(prefix) {
  const interpreter = $(`${prefix}Interpreter`), args = $(`${prefix}Args`);
  if (!interpreter || !args) return;
  const inlineShell = interpreter.value === "sh-inline";
  args.disabled = inlineShell;
  args.placeholder = inlineShell ? "Not used for inline shell commands" : "Optional arguments";
  args.setAttribute("aria-disabled", String(inlineShell));
}

function trimInlineCommandQuotes(value) {
  const command = value.trim();
  if (command.length >= 2 && ((command.startsWith('"') && command.endsWith('"')) || (command.startsWith("'") && command.endsWith("'")))) {
    return command.slice(1, -1);
  }
  return command;
}

function commandFromEditor(prefix) {
  const environment = {};
  const names = new Map();
  for (const row of $(`${prefix}EnvRows`).querySelectorAll(".environment-row")) {
    const name = row.querySelector(".environment-name").value.trim();
    const value = row.querySelector(".environment-value").value;
    if (!name && !value) continue;
    if (!name) {
      setCommandError(prefix, "Environment variable names are required.");
      return null;
    }
    if (name.includes("=")) {
      setCommandError(prefix, `Environment variable "${name}" must not contain =.`);
      return null;
    }
    const canonicalName = name.toUpperCase();
    if (names.has(canonicalName)) {
      setCommandError(prefix, `Environment variables "${names.get(canonicalName)}" and "${name}" conflict on case-insensitive platforms.`);
      return null;
    }
    names.set(canonicalName, name);
    environment[name] = value;
  }
  setCommandError(prefix);
  const inlineShell = $(`${prefix}Interpreter`).value === "sh-inline";
  return {
    path: inlineShell ? trimInlineCommandQuotes($(`${prefix}Path`).value) : $(`${prefix}Path`).value.trim(),
    args: inlineShell ? [] : splitArgs($(`${prefix}Args`).value),
    workingDirectory: $(`${prefix}Cwd`).value.trim(),
    interpreter: $(`${prefix}Interpreter`).value,
    environment,
  };
}

document.querySelectorAll(".environment-editor").forEach(editor => {
  const prefix = editor.dataset.commandPrefix;
  editor.querySelector(".env-add").addEventListener("click", () => addEnvironmentRow(prefix, "", "", true));
});
document.querySelectorAll("select[id$='Interpreter']").forEach(select => {
  const prefix = select.id.slice(0, -"Interpreter".length);
  select.addEventListener("change", () => updateCommandEditorMode(prefix));
});

function fmtDate(value) {
  if (!value) return "—";
  return new Date(value).toLocaleString();
}

function fmtBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes, unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

function fmtPercent(value) {
  return Number.isFinite(value) ? `${value.toFixed(1)}%` : "—";
}

function percent(value, total = 100) {
  if (!Number.isFinite(value) || !Number.isFinite(total) || total <= 0) return 0;
  return Math.max(0, Math.min(100, (value / total) * 100));
}

function meter(value, tone = "blue") {
  return `<div class="meter ${tone}" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${value.toFixed(1)}"><span style="width:${value}%"></span></div>`;
}

function utilizationMetric(label, current, average, tone, available = true) {
  if (!available) return `<div class="metric utilization-metric"><span>${label}</span><strong>Not available</strong>${meter(0, tone)}</div>`;
  return `<div class="metric utilization-metric">
    <span>${label}</span>
    <div class="utilization-reading"><span>Current</span><strong>${fmtPercent(current)}</strong></div>
    ${meter(percent(current), tone)}
    <div class="utilization-reading"><span>1 min AVG</span><strong>${fmtPercent(average)}</strong></div>
    ${meter(percent(average), tone)}
  </div>`;
}

function scheduleText(s) {
  if (!s) return "—";
  if (s.type === "interval") {
    const min = Math.round((s.intervalSeconds || 0) / 60);
    return `Every ${min} min`;
  }
  if (s.type === "daily") return `Daily ${s.timeOfDay}`;
  return `Cron ${s.cron}`;
}

function statusBadge(status) {
  const cls = status || "idle";
  return `<span class="status ${escapeHtml(cls)}">${escapeHtml(cls)}</span>`;
}

function runDuration(run) {
  const end = run.finishedAt ? new Date(run.finishedAt) : new Date();
  return `${Math.max(0, Math.round((end - new Date(run.startedAt)) / 1000))}s`;
}

function runStatus(run) { return run.finishedAt ? (run.success ? "Success" : "Failed") : "Running"; }

async function openRunHistory(id, name) {
  $("runHistoryTitle").textContent = `${name} history`;
  $("runHistoryList").innerHTML = `<div class="empty compact"><p>Loading history…</p></div>`;
  $("runHistoryLogOutput").textContent = "No run selected.";
  $("runHistoryDialog").showModal();
  try {
    const runs = await api(`api/v1/runs?targetId=${encodeURIComponent(id)}&limit=20`);
    $("runHistoryList").innerHTML = runs.length ? runs.map(run => `<article class="row run-history-row" role="button" tabindex="0" aria-label="Open log from ${escapeHtml(fmtDate(run.startedAt))}" onclick="openHistoryRunLog('${escapeHtml(run.id)}','${escapeHtml(name)}')" onkeydown="if(event.key==='Enter'||event.key===' '){event.preventDefault();openHistoryRunLog('${escapeHtml(run.id)}','${escapeHtml(name)}')}"><div class="row-head"><div><strong>${escapeHtml(fmtDate(run.startedAt))}</strong><div class="meta">${escapeHtml(runStatus(run))} · ${escapeHtml(runDuration(run))}${run.exitCode != null ? ` · exit ${escapeHtml(run.exitCode)}` : ""}</div></div>${statusBadge(runStatus(run).toLowerCase())}</div>${run.message ? `<div class="meta">${escapeHtml(run.message)}</div>` : ""}</article>`).join("") : `<div class="empty compact"><h2>No completed runs</h2><p>Run history will appear here after this task executes.</p></div>`;
  } catch (error) { $("runHistoryList").innerHTML = `<div class="empty compact"><p>${escapeHtml(error.message)}</p></div>`; }
}

async function openHistoryRunLog(id, name) {
  $("runHistoryLogOutput").textContent = "Loading…";
  try {
    const text = await api(`api/v1/runs/${encodeURIComponent(id)}/log?lines=800`);
    $("runHistoryLogOutput").textContent = text || "(no output)";
  } catch (error) {
    $("runHistoryLogOutput").textContent = error.message;
  }
}

function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, ch => ({
    "&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"
  }[ch]));
}

async function refresh() {
  if (refreshing || !token) return;
  refreshing = true;
  try {
    const [pluginSnapshot, frontendSnapshot] = await Promise.all([
      currentPage === "settings" ? api("api/v1/plugins") : Promise.resolve(null),
      api("api/v1/plugins/runtime")
    ]);
    if (pluginSnapshot) { pluginStatuses = pluginSnapshot.plugins || []; pluginDiscoveryErrors=pluginSnapshot.discoveryErrors || []; pluginRestartRequired=!!pluginSnapshot.restartRequired; renderPluginSettings(); if (!pluginCatalogLoaded && !pluginCatalogLoading) refreshPluginCatalog(); }
    if (frontendSnapshot) await loadPluginExtensions(frontendSnapshot);
    renderOverview();
    setConnected(true);
  } catch (e) {
    softwareLoading = false; softwareLoadingKey = "";
    setConnected(!!e.serverReachable);
    if (e.message === "Unauthorized") $("loginDialog").showModal();
    else toast(e.message);
  } finally {
    refreshing = false;
  }
}

async function loadPluginExtensions(extensions) {
  const active = new Set((extensions || []).map(extension => extension.id));
  for (const [id, loaded] of pluginExtensions) {
    if (!active.has(id)) {
      if (typeof loaded.root === "function") loaded.root(); else loaded.root?.remove();
      for (const [key, entry] of pluginNavigation) if (entry.extension === id) { entry.button.remove(); entry.page.remove(); pluginNavigation.delete(key); if (currentPage === key) { $("pluginHeaderActions").replaceChildren(); $("pluginHeaderActions").classList.add("hidden"); } }
      for (const [key, entry] of pluginOverview) if (entry.extension === id) pluginOverview.delete(key);
      for (const [key, entry] of pluginSettings) if (entry.extension === id) pluginSettings.delete(key);
      pluginExtensions.delete(id);
    }
  }
  for (const extension of extensions || []) {
    if (pluginExtensions.has(extension.id)) continue;
    const assetRevision = Date.now().toString();
    if (extension.stylesheet) {
      const stylesheet = document.createElement("link");
      stylesheet.rel = "stylesheet";
      const stylesheetURL = new URL(extension.stylesheet, window.location.href);
      stylesheetURL.searchParams.set("_rp", assetRevision);
      stylesheet.href = stylesheetURL.href;
      stylesheet.dataset.runpilotPlugin = extension.id;
      document.head.append(stylesheet);
    }
    const moduleURL = new URL(extension.module, window.location.href);
    moduleURL.searchParams.set("_rp", assetRevision);
    const module = await import(moduleURL.href);
    if (typeof module.activate !== "function") throw new Error(`Plugin ${extension.id} does not export activate()`);
    const runpilot = Object.freeze({
      ui: pluginUI(),
      navigation: Object.freeze({ register: entry => registerPluginNavigation(extension.id, entry) }),
      overview: Object.freeze({ register: entry => registerPluginOverview(extension.id, entry), render: renderOverview }),
      settings: Object.freeze({ register: entry => registerPluginSettings(extension.id, entry) }),
      ws: pluginWS,
    });
    pluginExtensions.set(extension.id, { module, root: await module.activate(runpilot) });
  }
}

function renderPluginSettings() {
  const root = $("pluginSettings"); if (!root) return;
  const installed = new Map(pluginStatuses.map(status => [status.manifest.id, status]));
  const catalog = new Map(pluginCatalog.map(entry => [entry.id, entry]));
  const ids = [...new Set([...installed.keys(), ...catalog.keys()])].sort();
  root.innerHTML = `${pluginRestartRequired ? '<p class="meta">Restart required to apply package and activation changes.</p>' : ""}${pluginDiscoveryErrors.map(error=>`<p class="meta">${escapeHtml(error)}</p>`).join("")}<p class="meta">${escapeHtml(pluginCatalogLoading ? "Checking catalog…" : pluginCatalogError || "Install packages, then enable them. Activation changes require a restart.")}</p>` + ids.map(id => {
    const status=installed.get(id), entry=catalog.get(id), manifest=status?.manifest || entry || {}, busy=pluginBusy.has(id);
    const enabled=!!status?.enabled, compatible=entry?.latestCompatible;
    const update=!!status?.source && compatible && entry?.updateAvailable;
    const state=status ? (status.message ? `${status.state === "incompatible" ? "Incompatible" : "Failed"}: ${status.message}` : `Installed${status.loaded ? "" : ` ${manifest.version || ""}`} · ${enabled ? "Enabled" : "Disabled"}${status.loaded ? ` · Loaded ${status.loadedVersion || ""}` : ""}`) : compatible ? `Available ${compatible}` : "Incompatible";
    const stateClass=status ? (status.message ? status.state === "incompatible" ? "is-incompatible" : "is-failed" : enabled ? "is-enabled" : "is-disabled") : compatible ? "is-available" : "is-incompatible";
    const buttons = status
      ? `<button class="button secondary small" type="button" ${busy || (!enabled && status.state === "incompatible") ? "disabled" : ""} onclick="togglePlugin('${escapeHtml(id)}',${!enabled})">${enabled ? "Disable" : "Enable"}</button>${update ? `<button class="button primary small" type="button" ${busy ? "disabled" : ""} onclick="managePlugin('${escapeHtml(id)}','install','${escapeHtml(compatible)}')">Update to ${escapeHtml(compatible)}</button>` : ""}<button class="button danger small" type="button" ${busy ? "disabled" : ""} onclick="managePlugin('${escapeHtml(id)}','uninstall')">Uninstall</button>`
      : `<button class="button primary small" type="button" ${busy || !compatible ? "disabled" : ""} onclick="managePlugin('${escapeHtml(id)}','install','${escapeHtml(compatible || "")}')">Install${compatible ? " " + escapeHtml(compatible) : ""}</button>`;
    return `<article class="docker-card plugin-setting-card" data-plugin-id="${escapeHtml(id)}"><div class="plugin-setting-info"><strong>${escapeHtml(manifest.name || id)}</strong><div class="meta">${escapeHtml(manifest.description || "")}</div></div><div class="plugin-setting-actions">${buttons}</div><span class="status plugin-setting-state ${stateClass}"${entry?.incompatibility ? ` title="${escapeHtml(entry.incompatibility)}"` : ""}>${escapeHtml(busy ? "Updating…" : state)}${update ? " · Update available" : ""}${status?.restartRequired ? " · Restart required" : ""}</span></article>`;
  }).join("");
  const cards = new Map([...root.querySelectorAll(".plugin-setting-card")].map(card => [card.dataset.pluginId, card]));
  for (const entry of pluginSettings.values()) {
    if (!installed.get(entry.extension)?.enabled) continue;
    const card = cards.get(entry.extension);
    if (!card) continue;
    try {
      const content = entry.render();
      if (!content) continue;
      const section = document.createElement("section");
      section.className = "plugin-settings-section";
      section.append(content);
      card.append(section);
    } catch(error) { console.error(`plugin settings ${entry.id}`,error); }
  }
}
async function requestRunPilotRestart() {
  if (!confirm("Restart RunPilot now? The page will reconnect when RunPilot is ready.")) return;
  const button=$("restartApplicationButton"); button.disabled=true;
  try { await api("api/v1/restart",{method:"POST"}); toast("RunPilot is restarting. This page will reconnect shortly."); }
  catch(error) { toastError(error.message); button.disabled=false; }
}
async function togglePlugin(id, enabled) {
  if (pluginBusy.has(id)) return; pluginBusy.add(id); renderPluginSettings();
  try { const response=await api(`api/v1/plugins/${encodeURIComponent(id)}`,{method:"PUT",body:JSON.stringify({enabled})}); pluginStatuses=response.plugins||pluginStatuses; pluginRestartRequired=!!response.restartRequired; }
  catch(error) { toastError(error.message); }
  finally { pluginBusy.delete(id); renderPluginSettings(); }
}

async function refreshPluginCatalog() {
  if (pluginCatalogLoading) return;
  pluginCatalogLoading=true; renderPluginSettings();
  try { const response=await api("api/v1/plugins/catalog"); pluginCatalog=response.plugins || []; pluginCatalogError=""; }
  catch(error) { pluginCatalogError=`Catalog unavailable: ${error.message}`; }
  finally { pluginCatalogLoaded=true; pluginCatalogLoading=false; renderPluginSettings(); }
}
async function managePlugin(id, action, version="") {
  if (pluginBusy.has(id)) return;
  if (action === "uninstall" && !confirm("Uninstall this package? Plugin data will be retained. Restart to finish deactivation.")) return;
  pluginBusy.add(id); renderPluginSettings();
  try {
    const response=await api(`api/v1/plugins/${encodeURIComponent(id)}${action === "install" ? "/install" : ""}`, {method:action === "install" ? "POST" : "DELETE", ...(action === "install" ? {body:JSON.stringify({version})} : {})});
    pluginStatuses=response.plugins || []; pluginRestartRequired=!!response.restartRequired; toast("Package changed. Restart required."); await refreshPluginCatalog();
  } catch(error) { toastError(error.message); }
  finally { pluginBusy.delete(id); renderPluginSettings(); }
}

function renderOverview() {
  $("overviewMetrics").replaceChildren();
  for (const entry of pluginOverview.values()) { try { const node=entry.render(); if(node) $("overviewMetrics").append(node); } catch(error) { console.error(`plugin overview ${entry.id}`,error); } }
  if (!$("overviewMetrics").childElementCount) $("overviewMetrics").append(pluginUI().EmptyState({ title: "No overview widgets yet", message: "Enabled plugins can add widgets here." }));
}

function startAutoRefresh() {
  if (refreshTimer) return;
  refreshTimer = setInterval(() => {
    if (!document.hidden) refresh();
  }, 5000);
}

async function probeConnection() {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 3500);
  try {
    await fetch("api/v1/system", {headers:{Authorization:`Bearer ${token}`}, signal:controller.signal});
    setConnected(true);
  } catch {
    setConnected(false);
  } finally {
    clearTimeout(timeout);
  }
}

function startConnectionMonitor() {
  if (connectionTimer) return;
  connectionTimer = setInterval(() => { if (!document.hidden) probeConnection(); }, 5000);
  probeConnection();
}

function renderContinuousTask(v) {
  const d = v.definition, s = v.status;
  const running = ["running","starting","stopping"].includes(s.state);
  return `<article class="row">
      <div class="row-head">
        <div><h3>${escapeHtml(d.name)}</h3><div class="meta">Continuous · ${escapeHtml(d.command.path)}</div></div>
        ${statusBadge(s.state)}
      </div>
      <div class="row-details">
        <div class="kv"><span>Started</span><span>${fmtDate(s.startedAt)}</span></div>
        <div class="kv"><span>Restart</span><span>${escapeHtml(d.restart?.mode || "on-failure")}</span></div>
        <div class="kv"><span>Autostart</span><span>${d.autostart ? "Yes" : "No"}</span></div>
      </div>
      <div class="row-actions">
        ${running ? `<button class="button secondary small" onclick="processAction('${d.id}','stop')">Stop</button><button class="button secondary small" onclick="processAction('${d.id}','restart')">Restart</button>` : `<button class="button primary small" onclick="processAction('${d.id}','start')">Start</button>`}
        <button class="button secondary small" onclick="openProcessLog('${d.id}','${escapeHtml(d.name)}')">Log</button><button class="button secondary small" onclick="editProcess('${d.id}')">Edit</button><button class="button danger small" onclick="deleteProcess('${d.id}')">Delete</button>
      </div></article>`;
}

function renderTasks() {
  const items = [
    ...processes.map(v => ({kind:"continuous", name:v.definition.name, markup:renderContinuousTask(v)})),
    ...jobs.filter(v => v.definition.type === "command").map(v => ({kind:"scheduled", name:v.definition.name, markup:renderJobRow(v)})),
    ...jobs.filter(v => v.definition.type === "backup").map(v => ({kind:"backup", name:v.definition.name, markup:renderBackupRow(v)})),
  ].filter(item => taskFilter === "all" || item.kind === taskFilter).sort((a,b) => a.name.localeCompare(b.name));
  $("taskGrid").innerHTML = items.map(item => item.markup).join("");
  $("taskEmpty").classList.toggle("hidden", items.length > 0);
}

function renderJobRow(v) {
  const d = v.definition, s = v.status;
  const state = s.running ? "running" : (s.lastSuccess === false ? "failure" : "idle");
  return `<article class="row">
    <div class="row-head"><div><h3>${escapeHtml(d.name)}</h3><div class="meta">Scheduled · ${escapeHtml(scheduleText(d.schedule))}</div></div>${statusBadge(state)}</div>
    <div class="row-details">
      <div class="kv"><span>Schedule</span><span>${escapeHtml(scheduleText(d.schedule))}</span></div>
      <div class="kv"><span>Enabled</span><span>${d.enabled ? "Yes" : "No"}</span></div>
      <div class="kv"><span>Last run</span><span>${fmtDate(s.lastRunAt)}</span></div>
      <div class="kv"><span>Last result</span><span>${s.lastSuccess == null ? "—" : (s.lastSuccess ? "Success" : "Failed")}</span></div>
    </div>
    <div class="row-actions">
      <button class="button primary small" onclick="runJob('${d.id}')">Run now</button>
      ${s.currentRunId ? `<button class="button secondary small" onclick="openRunLog('${s.currentRunId}','${escapeHtml(d.name)}')">Live log</button>` : ""}
      <button class="button secondary small" onclick="openRunHistory('${d.id}','${escapeHtml(d.name)}')">History</button>
      <button class="button secondary small" onclick="editJob('${d.id}')">Edit</button>
      <button class="button danger small" onclick="deleteJob('${d.id}')">Delete</button>
    </div>
  </article>`;
}

function renderBackupRow(v) {
  const d = v.definition, s = v.status, b = d.backup || {};
  const provider = b.engine || "robocopy", cfg = b[provider === "rdiff-backup" ? "rdiffBackup" : provider] || b;
  const source = provider === "restic" ? (cfg.sources || []).join(", ") : cfg.source;
  const target = provider === "restic" ? cfg.repository : cfg.destination;
  const state = s.running ? "running" : (s.lastSuccess === false ? "failure" : "idle");
  return `<article class="row">
    <div class="row-head"><div><h3>${escapeHtml(d.name)}</h3><div class="meta">Backup · ${escapeHtml(provider)} · ${escapeHtml(scheduleText(d.schedule))}</div></div>${statusBadge(state)}</div>
    <div class="row-details">
      <div class="kv"><span>Provider</span><span>${escapeHtml(provider)}</span></div>
      <div class="kv"><span>Schedule</span><span>${escapeHtml(scheduleText(d.schedule))}</span></div>
      <div class="kv"><span>Last run</span><span>${fmtDate(s.lastRunAt)}</span></div>
      <div class="kv"><span>Last result</span><span>${s.lastSuccess == null ? "—" : (s.lastSuccess ? "Success" : "Failed")}</span></div>
    </div>
    <div class="row-actions">
      <button class="button primary small" onclick="runJob('${d.id}')">Run backup</button>
      ${s.currentRunId ? `<button class="button secondary small" onclick="openRunLog('${s.currentRunId}','${escapeHtml(d.name)}')">Live log</button>` : ""}
      <button class="button secondary small" onclick="openRunHistory('${d.id}','${escapeHtml(d.name)}')">History</button>
      <button class="button secondary small" onclick="editBackup('${d.id}')">Edit</button>
      <button class="button danger small" onclick="deleteJob('${d.id}')">Delete</button>
    </div>
  </article>`;
}

async function downloadStorage(id,p){try{const t=await api(`api/v1/storage/${id}/download-ticket`,{method:"POST",body:JSON.stringify({path:p})});window.location.assign(t.url)}catch(e){toast(e.message)}}
async function deleteStorageObject(id,p,type){if(!confirm(`Delete ${type === "directory" ? "folder and all contents" : "file"}? This cannot be undone through RunPilot.`))return;try{await api(`api/v1/storage/${id}/delete`,{method:"POST",body:JSON.stringify({path:p})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}
async function renameStorage(id,p){const n=prompt("New name:");if(!n)return;try{await api(`api/v1/storage/${id}/rename`,{method:"POST",body:JSON.stringify({path:p,newName:n})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}
async function moveStorage(id,p){const d=prompt("Destination folder path (provider-relative):",storagePath);if(d===null)return;try{await api(`api/v1/storage/${id}/move`,{method:"POST",body:JSON.stringify({sourcePath:p,destinationDirectory:d})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}
async function copyStorage(id,p){const d=prompt("Célmappa útvonala:",storagePath);if(d===null)return;try{await api(`api/v1/storage/${id}/copy`,{method:"POST",body:JSON.stringify({sourcePath:p,destinationDirectory:d})});browseStorage(id,storagePath)}catch(e){toast(e.message)}}

function renderStorage() {
  const select = $("storageProvider"), previous = storageLocation || select.value;
  select.replaceChildren();
  storage.forEach(d => { const option = document.createElement("option"); option.value = d.id; option.textContent = d.name; select.append(option); });
  const provider = storage[0];
  $("storageEmpty").classList.toggle("hidden", !!provider);
  $("storageBrowser").classList.toggle("hidden", !provider);
  if (provider) { select.value = storage.some(d => d.id === previous) ? previous : provider.id; if (!storageLocation) browseStorage(select.value, ""); }
}
function isEditableText(name) { return !/\.[^./\\]+$/.test(name) || /\.(txt|log|yaml|yml|json|ini|cfg|conf|toml|xml|csv|md|bat|cmd|ps1|go|js|html|css|ts|tsx|jsx|py|rb|java|c|h|cpp|cs|rs|sh|sql)$/i.test(name); }
function button(label, title, action, disabled = false) { const b=document.createElement("button"); b.className="row-icon"+(title==="Letöltés"?" download-icon":""); b.type="button"; b.textContent=label; b.title=title; b.disabled=disabled; b.addEventListener("click", event=>{event.stopPropagation();action()}); return b; }
function actionSlot(control = null) { if (control) return control; const slot=document.createElement("span");slot.className="row-icon-slot";slot.setAttribute("aria-hidden","true");return slot; }
function entryIcon(entry) { if (entry.type === "directory" || entry.type === "filesystem-root") return "folder"; const ext=(entry.name.split(".").pop()||"").toLowerCase(); if (["txt","log","yaml","yml","json","ini","cfg","conf","toml","xml","csv","md","go","js","ts","py","sh","sql"].includes(ext)||!/\.[^./\\]+$/.test(entry.name)) return "text"; if(["jpg","jpeg","png","gif","webp","svg"].includes(ext)) return "image"; if(["mp3","wav","flac","ogg"].includes(ext)) return "audio"; if(["mp4","mkv","avi","mov","webm"].includes(ext)) return "video"; return "file"; }
function renderBreadcrumbs(id, value) { const root=$("storageBreadcrumbs"); root.replaceChildren(); const home=document.createElement("button");home.className="breadcrumb-link";home.textContent="Ez a gép";home.addEventListener("click",()=>browseStorage(id,""));root.append(home); let built=""; for(const segment of value ? value.split("/") : []) { const sep=document.createElement("span");sep.textContent="/";root.append(sep);built=built?`${built}/${segment}`:segment;const link=document.createElement("button");link.className="breadcrumb-link";link.textContent=segment;const target=built;link.addEventListener("click",()=>browseStorage(id,target));root.append(link); } }
function setStorageEntryLoading(path, loading) { const row=[...document.querySelectorAll(".storage-entry")].find(item=>item.dataset.storagePath===path); if (!row) return; row.setAttribute("aria-busy",String(loading)); row.querySelector(".entry-icon")?.classList.toggle("loading",loading); }
async function browseStorage(id, p = "", loadingEntry = "") {
  if (id === "docker-volumes" && loadingEntry) setStorageEntryLoading(loadingEntry, true);
  try {
    const provider=storage.find(x=>x.id===id); if (!provider || !["ready","read-only"].includes(provider.state?.status)) { toast(provider?.state?.reason || "Storage provider is unavailable"); return; }
    const stateNotice=$("storageStateNotice");
    const listing=await api(`api/v1/storage/${id}/entries?`+new URLSearchParams({path:p,showHidden:storageShowHidden})); storageLocation=id; storagePath=listing.path;
    const pathState=listing.state||provider.state||{}, readOnly=pathState.status === "read-only";
    stateNotice.classList.toggle("hidden",!readOnly); stateNotice.innerHTML=readOnly?`<strong>Storage is read-only</strong><span>${escapeHtml(pathState.reason || "This provider is currently read-only.")}</span>`:"";
    storagePathCapabilities=listing.capabilities||provider.capabilities||{};
    const toolbarCaps=storagePathCapabilities; $("storageUpload").parentElement.classList.toggle("hidden",!toolbarCaps.upload); $("storageNewFile").classList.toggle("hidden",!toolbarCaps.textEdit); $("storageCreate").classList.toggle("hidden",!toolbarCaps.createDirectory);
    $("storageProvider").value=id; renderBreadcrumbs(id,listing.path);
    const root=$("storageEntries");root.replaceChildren(); const header=document.createElement("div");header.className="storage-list-head";header.innerHTML="<span>Name</span><span>Size</span><span>Modified</span><span>Actions</span>";root.append(header);
    if (listing.parentPath != null) {
      const parent=document.createElement("article");parent.className="row storage-entry storage-row openable";
      const head=document.createElement("div");head.className="storage-name";const icon=document.createElement("span");icon.className="entry-icon folder";icon.setAttribute("aria-hidden","true");const title=document.createElement("h3");title.textContent="..";const meta=document.createElement("div");meta.className="meta";meta.textContent="parent folder";head.append(icon,title,meta);
      const size=document.createElement("div");size.className="storage-cell";size.textContent="—";const modified=document.createElement("div");modified.className="storage-cell";modified.textContent="—";const actions=document.createElement("div");actions.className="row-actions";parent.append(head,size,modified,actions);parent.addEventListener("click",()=>browseStorage(id,listing.parentPath));root.append(parent);
    }
    listing.entries.forEach(entry=>{
      const row=document.createElement("article");row.className="row storage-entry storage-row";row.dataset.storagePath=entry.path;
      const head=document.createElement("div");head.className="storage-name";const icon=document.createElement("span");icon.className=`entry-icon ${entryIcon(entry)}`;icon.setAttribute("aria-hidden","true");const title=document.createElement("h3");title.textContent=entry.name;head.append(icon);
      head.append(title);const meta=document.createElement("div");meta.className="meta";meta.textContent=entry.type;head.append(meta);
      const details=document.createElement("div");details.className="row-details";details.innerHTML=`<div class="kv"><span>Méret</span><span>${fmtBytes(entry.size)}</span></div><div class="kv"><span>Módosítva</span><span>${fmtDate(entry.modifiedAt)}</span></div>`;
      const actions=document.createElement("div");actions.className="row-actions";
      const caps=storagePathCapabilities||storage.find(x=>x.id===id)?.capabilities||{}, mutable=entry.type!=="filesystem-root", editable=entry.type==="file" && isEditableText(entry.name) && caps.textEdit;
      actions.append(actionSlot(entry.type==="file" && caps.download ? button("↓","Letöltés",()=>downloadStorage(id,entry.path)) : null));
      actions.append(actionSlot(editable ? button("✎","Szerkesztés",()=>openTextEditor(id,entry.path,entry.name)) : null));
      actions.append(actionSlot(mutable && caps.rename ? button("↺","Átnevezés",()=>renameStorage(id,entry.path)) : null));
      actions.append(actionSlot(mutable && caps.copy ? button("⧉","Másolás",()=>setStorageClipboard("copy",id,entry)) : null));
      actions.append(actionSlot(mutable && caps.move ? button("✂","Kivágás",()=>setStorageClipboard("move",id,entry)) : null));
      actions.append(caps[storageClipboard?.operation] && storageClipboard?.providerId===id ? button("📌","Beillesztés",()=>pasteStorage(id)) : actionSlot());
      actions.append(actionSlot(mutable && caps.delete ? button("🗑","Törlés",()=>deleteStorageObject(id,entry.path,entry.type)) : null));
      const size=document.createElement("div");size.className="storage-cell";size.textContent=entry.type==="file"?fmtBytes(entry.size):"—";const modified=document.createElement("div");modified.className="storage-cell";modified.textContent=fmtDate(entry.modifiedAt);row.append(head,size,modified,actions);
      if(entry.type!=="file") { row.classList.add("openable");row.addEventListener("click",()=>browseStorage(id,entry.path,entry.path)); }
      else { row.classList.add("openable");row.addEventListener("click",()=>isEditableText(entry.name)?openTextEditor(id,entry.path,entry.name):downloadStorage(id,entry.path)); }
      root.append(row);
    });
  } catch(e) { toast(e.message); }
  finally { if (id === "docker-volumes" && loadingEntry) setStorageEntryLoading(loadingEntry, false); }
}
function setStorageClipboard(operation, providerId, entry) { storageClipboard={providerId,operation,path:entry.path,name:entry.name}; toast(operation==="copy" ? `Másolásra kijelölve: ${entry.name}` : `Áthelyezésre kijelölve: ${entry.name}`); browseStorage(storageLocation,storagePath); }
async function pasteStorage(id) { if(!storageClipboard||storageClipboard.providerId!==id)return; try { const endpoint=storageClipboard.operation==="copy"?"copy":"move"; await api(`api/v1/storage/${id}/${endpoint}`,{method:"POST",body:JSON.stringify({sourcePath:storageClipboard.path,destinationDirectory:storagePath})}); const message=storageClipboard.operation==="copy"?"Másolva":"Áthelyezve"; if(storageClipboard.operation==="move")storageClipboard=null;toast(message);browseStorage(id,storagePath); }catch(e){toast(e.message)} }
function syntaxSpan(kind, value) { return `<span class="syntax-${kind}">${escapeHtml(value)}</span>`; }
function highlightText(content, name) {
  const ext=(name.split(".").pop()||"").toLowerCase(), yaml=["yaml","yml"].includes(ext), env=ext==="env"; const structured=["json","yaml","yml","toml","ini","xml","html","css"].includes(ext);
  return content.split("\n").map(line=>{
    if (/^\s*(#|\/\/)/.test(line)) return syntaxSpan("comment",line);
    const chunks=line.split(/("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')/g);
    return chunks.map((part,index)=>{
      if (index%2) return syntaxSpan("string",part);
      let safe=escapeHtml(part);
      if (structured) safe=safe.replace(/\b(true|false|null|yes|no)\b/gi,'<span class="syntax-keyword">$1</span>').replace(/\b(-?\d+(?:\.\d+)?)\b/g,'<span class="syntax-number">$1</span>');
      else safe=safe.replace(/\b(func|function|return|if|else|for|while|package|import|const|var|let|class|public|private|def|true|false|null|nil)\b/g,'<span class="syntax-keyword">$1</span>');
      if (yaml) safe=safe.replace(/^(\s*(?:-\s+)?)([A-Za-z0-9_.-]+)(\s*:)/,'$1<span class="syntax-key">$2</span>$3');
      if (env) safe=safe.replace(/^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)(=)/,'$1<span class="syntax-key">$2</span>$3');
      return safe;
    }).join("");
  }).join("\n")+"\n";
}
function updateTextHighlight() { const code=$("textHighlight").querySelector("code"); code.innerHTML=highlightText($("textEditorContent").value,editingTextPath?.path||""); }
function updateDockerFileHighlight() { const code=$("dockerFileHighlight").querySelector("code"); code.innerHTML=highlightText($("dockerFileContent").value,dockerEditing?.kind==="env"?".env":"compose.yaml"); }
async function openTextEditor(id,path,name) { try { const data=await api(`api/v1/storage/${id}/text?`+new URLSearchParams({path})); editingTextPath={id,path};const canEdit=!!(storagePathCapabilities||storage.find(x=>x.id===id)?.capabilities||{}).textEdit;$("textEditorTitle").textContent=`${canEdit ? "Edit" : "View"}: ${name}`;$("textEditorPath").textContent=path;$("textEditorContent").value=data.content;$("textEditorContent").readOnly=!canEdit;$("saveTextEditor").disabled=!canEdit;updateTextHighlight();$("textEditorDialog").showModal(); }catch(e){toast(e.message)} }
function openNewTextFile() { if (!storageLocation || !(storagePathCapabilities||{}).textEdit) return; const name=prompt("File name:"); if (!name) return; if (name === "." || name === ".." || /[\\/]/.test(name)) { toast("File name must be a single name."); return; } const path=storagePath ? `${storagePath}/${name}` : name; editingTextPath={id:storageLocation,path};$("textEditorTitle").textContent=`New file: ${name}`;$("textEditorPath").textContent=path;$("textEditorContent").value="";$("textEditorContent").readOnly=false;$("saveTextEditor").disabled=false;updateTextHighlight();$("textEditorDialog").showModal(); }
async function saveTextEditor() { if(!editingTextPath)return;try{await api(`api/v1/storage/${editingTextPath.id}/text`,{method:"PUT",body:JSON.stringify({path:editingTextPath.path,content:$("textEditorContent").value})});$("textEditorDialog").close();toast("Fájl mentve");browseStorage(storageLocation,storagePath)}catch(e){toast(e.message)} }

async function processAction(id, action) {
  try { await api(`api/v1/processes/${id}/${action}`, {method:"POST"}); toast(`Continuous task ${action} requested`); setTimeout(refresh, 250); }
  catch (e) { toast(e.message); }
}

async function runJob(id) {
  try { await api(`api/v1/jobs/${id}/run`, {method:"POST"}); toast("Task started"); setTimeout(refresh, 250); }
  catch (e) { toast(e.message); }
}

async function deleteProcess(id) {
  if (!confirm("Delete this continuous task?")) return;
  try { await api(`api/v1/processes/${id}`, {method:"DELETE"}); await refresh(); }
  catch (e) { toast(e.message); }
}

async function deleteJob(id) {
  if (!confirm("Delete this job?")) return;
  try { await api(`api/v1/jobs/${id}`, {method:"DELETE"}); await refresh(); }
  catch (e) { toast(e.message); }
}

function activeSoftwareProvider() { return softwareProviders.find(provider => provider.id === softwareProviderID) || softwareProviders[0]; }
function softwarePackageFacts(pkg) {
  const facts = [{label: pkg.installed ? "Installed" : "Version", value: pkg.version || "—"}];
  if (pkg.availableVersion) facts.push({label: "Latest", value: pkg.availableVersion});
  if (pkg.bucket) facts.push({label: "Source", value: pkg.bucket});
  return facts.map(fact => `<div><span>${escapeHtml(fact.label)}</span><strong>${escapeHtml(fact.value)}</strong></div>`).join("");
}
function renderSoftware() {
  const provider = activeSoftwareProvider();
  const card = $("softwareProviderCard"), packages = $("softwarePackages"), picker = $("softwareProviderSelect");
  picker.replaceChildren();
  softwareProviders.forEach(item => { const option = document.createElement("option"); option.value = item.id; option.textContent = `${item.name} (${item.type})`; picker.append(option); });
  if (!provider) {
    picker.parentElement.classList.add("hidden");
    $("softwareTabs").classList.add("hidden");
    $("softwareSearchBar").classList.add("hidden");
    $("softwareBucketBar").classList.add("hidden");
    card.innerHTML = `<div class="empty compact"><h2>No software providers available</h2><p>No supported software providers are configured for this operating system.</p></div>`;
    packages.replaceChildren();
    return;
  }
  picker.parentElement.classList.remove("hidden");
  picker.value = provider.id;
  const state = provider.state || "unavailable";
  const message = provider.message || "Preparing the managed Scoop runtime.";
  const hasRootDraft = Object.prototype.hasOwnProperty.call(softwareRootDrafts, provider.id);
  const rootValue = hasRootDraft ? softwareRootDrafts[provider.id] : provider.usingDefaultRoot ? "" : provider.root || "";
  card.innerHTML = `<article class="software-provider-summary">
    <div class="software-provider-identity"><div class="software-provider-title"><h2>${escapeHtml(provider.name)}</h2>${statusBadge(state)}</div></div>
    <label class="software-root-editor"><span>Root</span><input id="softwareRoot" value="${escapeHtml(rootValue)}" placeholder="${escapeHtml(provider.root || "Managed Scoop root")}" ${softwareBusy ? "disabled" : ""}><small>Leave empty to use the RunPilot data-directory default. Changing roots does not move or delete applications.</small></label>
    <div class="row-actions software-provider-actions"><button class="button secondary small" onclick="saveSoftwareRoot()" ${softwareBusy ? "disabled" : ""}>Save root</button><button class="button secondary small" onclick="softwareRefresh()" ${softwareBusy ? "disabled" : ""}>${state === "ready" ? "Refresh" : "Retry"}</button></div>
  </article>`;
  $("softwareTabs").classList.toggle("hidden", state !== "ready");
  $("softwareUpgradeAll").classList.toggle("hidden", softwareTab !== "updates" || !softwareUpdates.length);
  $("softwareUpgradeAll").disabled = softwareBusy;
  $("softwareSearchBar").classList.toggle("hidden", state !== "ready" || softwareTab !== "search");
  $("softwareBucketBar").classList.toggle("hidden", state !== "ready" || softwareTab !== "buckets");
  $("softwareAddBucket").disabled = softwareBusy || state !== "ready";
  $("softwareAddBucket").textContent = softwareBusy && softwareTab === "buckets" ? "Working…" : "Add bucket";
  document.querySelectorAll("[data-software-tab]").forEach(b => b.classList.toggle("active", b.dataset.softwareTab === softwareTab));
  if (state !== "ready") { packages.innerHTML = `<div class="empty compact"><h2>${state === "initializing" ? "Preparing RunPilot Software Management…" : "Software unavailable"}</h2><p>${escapeHtml(message)}</p></div>`; return; }
  if (softwareLoading) { packages.innerHTML = `<div class="software-loading" role="status"><span class="spinner" aria-hidden="true"></span><strong>Loading ${softwareTab === "buckets" ? "buckets" : "applications"}…</strong><span>Querying the selected provider.</span></div>`; return; }
  if (softwareTab === "buckets" && softwareBusy) { packages.innerHTML = `<div class="software-loading" role="status"><span class="spinner" aria-hidden="true"></span><strong>${escapeHtml(softwareBusyLabel || "Updating buckets")}…</strong><span>Waiting for Scoop to finish the bucket operation.</span></div>`; return; }
  if (softwareTab === "buckets") {
    packages.innerHTML = softwareBuckets.length ? softwareBuckets.map(bucket => `<article class="software-bucket-row"><div><h3>${escapeHtml(bucket.name)}</h3><div class="meta">${escapeHtml(bucket.source || "Scoop default source")}</div></div><div class="row-actions">${bucket.protected ? `<span class="software-protected" title="This bucket is required by Scoop">Required by Scoop</span>` : `<button class="button danger small" onclick="softwareRemoveBucket('${escapeHtml(bucket.name)}')" ${softwareBusy ? "disabled" : ""}>Remove</button>`}</div></article>`).join("") : `<div class="empty compact"><h2>No additional buckets</h2><p>Add a trusted Scoop bucket to make its applications available for search.</p></div>`;
    return;
  }
  const items = softwareTab === "updates" ? softwareUpdates : softwareTab === "search" ? softwareSearchResults : softwarePackages;
  packages.innerHTML = items.length ? items.map(p => `<article class="software-package-row"><div class="software-package-name"><h3>${escapeHtml(p.name)}</h3><div class="meta">${escapeHtml(p.description || p.bucket || "Scoop main bucket")}</div></div>${p.updateAvailable ? statusBadge("update") : p.installed ? statusBadge("installed") : ""}<div class="software-package-meta">${softwarePackageFacts(p)}</div><div class="row-actions">${p.protected ? `${p.installed && p.updateAvailable ? `<button class="button software-protected-action small" disabled>Upgrade</button>` : ""}<button class="button software-protected-action small" disabled>${p.installed ? "Uninstall" : "Install"}</button>` : p.installed ? `${p.updateAvailable ? `<button class="button primary small" onclick="softwareUpgrade('${escapeHtml(p.id)}')" ${softwareBusy ? "disabled" : ""}>Upgrade</button>` : ""}<button class="button danger small" onclick="softwareUninstall('${escapeHtml(p.id)}')" ${softwareBusy ? "disabled" : ""}>Uninstall</button>` : `<button class="button primary small" onclick="softwareInstall('${escapeHtml(p.id)}')" ${softwareBusy ? "disabled" : ""}>Install</button>`}</div></article>`).join("") : `<div class="empty compact"><h2>${softwareTab === "search" ? "Search the managed buckets" : softwareTab === "updates" ? "No updates found" : "No RunPilot-managed applications"}</h2><p>${softwareTab === "installed" ? "Applications installed in another Scoop root are deliberately not shown here." : ""}</p></div>`;
}
function changeSoftwareProvider(id) { if (id === softwareProviderID) return; softwareProviderID = id; softwarePackages = []; softwareUpdates = []; softwareSearchResults = []; softwareBuckets = []; softwareLoadedKey = ""; softwareLoadSequence++; softwareLoading = true; renderSoftware(); refresh(); }
async function softwareAction(path, options = {}, busyLabel = "") { softwareBusy = true; softwareBusyLabel = busyLabel; renderSoftware(); let completed = false; try { await api(path, options); completed = true; softwareLoadedKey = ""; toast("Software operation completed"); await refresh(); } catch (e) { toast(e.message); await refresh(); } finally { softwareBusy = false; softwareBusyLabel = ""; renderSoftware(); } return completed; }
function softwareInstall(id) { const p=activeSoftwareProvider(); if(p) softwareAction(`api/v1/software/providers/${p.id}/install`, {method:"POST", body:JSON.stringify({package:id})}); }
function softwareUpgrade(id) { const p=activeSoftwareProvider(); if(p) softwareAction(`api/v1/software/providers/${p.id}/packages/${encodeURIComponent(id)}/upgrade`, {method:"POST"}); }
function softwareUninstall(id) { const p=activeSoftwareProvider(); if(p && confirm(`Uninstall ${id} from the RunPilot-managed Scoop root?`)) softwareAction(`api/v1/software/providers/${p.id}/packages/${encodeURIComponent(id)}/uninstall`, {method:"POST"}); }
function softwareRefresh() { const p=activeSoftwareProvider(); if(p) softwareAction(`api/v1/software/providers/${p.id}/refresh`, {method:"POST"}); }
function softwareUpgradeAll() { const p=activeSoftwareProvider(); if(p && confirm("Upgrade all RunPilot-managed Scoop applications?")) softwareAction(`api/v1/software/providers/${p.id}/upgrade-all`, {method:"POST"}); }
function saveSoftwareRoot() { const p=activeSoftwareProvider(); if(!p)return; const root = $("softwareRoot").value.trim(); softwareRootDrafts[p.id] = root; softwareAction(`api/v1/software/providers/${p.id}`, {method:"PUT",body:JSON.stringify({scoop:{root}})}); }
function softwareAddBucket() { const p = activeSoftwareProvider(), name = $("softwareBucketName").value.trim(), source = $("softwareBucketSource").value.trim(); if (!p || !name) { toast("A bucket name is required."); return; } softwareAction(`api/v1/software/providers/${p.id}/buckets`, {method:"POST", body:JSON.stringify({name, source})}, "Adding bucket").then(completed => { if (completed) { $("softwareBucketName").value = ""; $("softwareBucketSource").value = ""; } }); }
function softwareRemoveBucket(name) { const p = activeSoftwareProvider(); if (p && confirm(`Remove Scoop bucket ${name}?`)) softwareAction(`api/v1/software/providers/${p.id}/buckets/${encodeURIComponent(name)}`, {method:"DELETE"}, "Removing bucket"); }
function softwareViewKey() { const p = activeSoftwareProvider(); if (!p || p.state !== "ready") return ""; const query = softwareTab === "search" ? $("softwareSearchInput").value.trim() : ""; return `${p.id}|${softwareTab}|${query}`; }
async function loadSoftwareView(force = false) {
  const p = activeSoftwareProvider(), query = $("softwareSearchInput").value.trim();
  if (!p || p.state !== "ready") return;
  if (softwareTab === "search" && !query) { softwareSearchResults = []; softwareLoading = false; renderSoftware(); return; }
  const key = softwareViewKey();
  if (!force && key === softwareLoadedKey) return;
  if (!force && softwareLoading && key === softwareLoadingKey) return;
  const sequence = ++softwareLoadSequence;
  softwareLoading = true; softwareLoadingKey = key; renderSoftware();
  try {
    const result = softwareTab === "installed" ? await api(`api/v1/software/providers/${p.id}/installed`) : softwareTab === "updates" ? await api(`api/v1/software/providers/${p.id}/updates`) : softwareTab === "buckets" ? await api(`api/v1/software/providers/${p.id}/buckets`) : await api(`api/v1/software/providers/${p.id}/search?` + new URLSearchParams({q:query}));
    if (sequence !== softwareLoadSequence) return;
    const items = Array.isArray(result) ? result : [];
    if (softwareTab === "installed") softwarePackages = items;
    else if (softwareTab === "updates") softwareUpdates = items;
    else if (softwareTab === "buckets") softwareBuckets = items;
    else softwareSearchResults = items;
    softwareLoadedKey = key;
  } catch (e) { if (sequence === softwareLoadSequence) toast(e.message); }
  finally { if (sequence === softwareLoadSequence) { softwareLoading = false; softwareLoadingKey = ""; renderSoftware(); } }
}
function softwareSearch() { loadSoftwareView(true); }

function renderDocker() {
  const notice = $("dockerNotice"), ready = dockerRuntime?.available;
  notice.classList.toggle("hidden", !!ready);
  notice.innerHTML = ready ? "" : `<strong>Docker unavailable</strong><span>${escapeHtml(dockerRuntime?.message || "Docker status has not been checked.")}${dockerRuntime?.identity ? ` Running as ${escapeHtml(dockerRuntime.identity)}.` : ""}</span>`;
  const projectColumns = [[], []];
  dockerProjects.forEach((project, index) => projectColumns[index % 2].push(dockerProjectCard(project, ready)));
  $("dockerProjectGrid").innerHTML = projectColumns.filter(column => column.length).map(column => `<div class="docker-project-column">${column.join("")}</div>`).join("");
  $("dockerVolumeList").innerHTML = dockerVolumes.map(volume => dockerVolumeRow(volume, ready)).join("");
  $("dockerNetworkList").innerHTML = dockerNetworks.map(network => dockerNetworkRow(network, ready)).join("");
  $("dockerProjectEmpty").classList.toggle("hidden", dockerProjects.length > 0 || !ready);
  $("dockerVolumeEmpty").classList.toggle("hidden", dockerVolumes.length > 0 || !ready);
  $("dockerNetworkEmpty").classList.toggle("hidden", dockerNetworks.length > 0 || !ready);
}
function applyDockerSnapshot(snapshot) {
  dockerRuntime = snapshot[0].runtime; dockerProjects = snapshot[0].projects || []; dockerVolumes = snapshot[1].volumes || []; dockerNetworks = snapshot[2].networks || [];
  for (const [key, pending] of dockerPendingContainerStates) {
    const container = dockerProjects.flatMap(project => project.containers || []).find(item => item.id === pending.id);
    const reachedExpectedState = pending.running == null ? !container : !!container && (container.state === "running") === pending.running;
    if (reachedExpectedState) { dockerPendingContainerStates.delete(key); dockerBusy.delete(key); }
  }
  renderDocker();
}
async function refreshDockerSnapshot() {
  try {
    const snapshot = await Promise.all([api("api/v1/docker/projects"), api("api/v1/docker/volumes"), api("api/v1/docker/networks")]);
    applyDockerSnapshot(snapshot);
    setConnected(true);
    return true;
  } catch (error) {
    setConnected(!!error.serverReachable);
    toast(error.message);
    return false;
  }
}
function dockerNetworkRow(network, ready) {
  const busy=dockerBusy.has(`network:${network.name}`), usage=!network.inUse ? "Unused" : network.runningUse ? "In use by running container" : "Used by stopped container";
  const protectedNetwork=["bridge","host","none"].includes(network.name);
  const composeManaged=!!network.composeProject;
  const detail=network.composeProject ? `Compose: ${network.composeProject}${network.composeNetwork ? ` / ${network.composeNetwork}` : ""}` : `${network.driver || "unknown driver"} · ${network.scope || "local"}`;
  const actions=protectedNetwork || composeManaged ? "" : `<button class="button danger small" onclick="deleteDockerNetwork('${escapeHtml(network.name)}')" ${!ready || network.inUse || busy ? "disabled" : ""}>Delete</button>`;
  return `<div class="docker-container docker-resource-row" title="${escapeHtml(detail)}"><span class="docker-dot ${network.inUse ? (network.runningUse ? "green" : "yellow") : "gray"}"></span><strong class="docker-container-name" title="${escapeHtml(network.name)}">${escapeHtml(network.name)}</strong><div class="docker-container-actions docker-resource-actions"><span class="docker-resource-action-slot" aria-hidden="true"></span>${actions}</div><span class="docker-resource-status" title="${escapeHtml(usage)}${(network.usedBy||[]).length ? ` · ${escapeHtml(network.usedBy.join(", "))}` : ""}">${escapeHtml(usage)}</span></div>`;
}
function dockerVolumeRow(volume, ready) {
  const busy = dockerBusy.has(`volume:${volume.name}`), usage = !volume.inUse ? "Unused" : volume.runningUse ? "In use by running container" : "Used by stopped container";
  const canDelete = ready && !volume.inUse && !busy;
  const deleteTitle = volume.inUse ? "Delete is unavailable while a container references this volume." : !ready ? "Docker is unavailable." : busy ? "Volume operation in progress." : "Delete volume";
  const detail = volume.composeProject ? `Compose: ${volume.composeProject}${volume.composeVolume ? ` / ${volume.composeVolume}` : ""}` : `${volume.driver || "unknown driver"} · ${volume.scope || "local"}`;
  const deleteAction = volume.inUse ? "" : `<button class="button danger small" title="${escapeHtml(deleteTitle)}" onclick="deleteDockerVolume('${escapeHtml(volume.name)}')" ${canDelete ? "" : "disabled"}>Delete</button>`;
  return `<div class="docker-container docker-resource-row" title="${escapeHtml(detail)}"><span class="docker-dot ${volume.inUse ? (volume.runningUse ? "green" : "yellow") : "gray"}"></span><strong class="docker-container-name" title="${escapeHtml(volume.name)}">${escapeHtml(volume.name)}</strong><div class="docker-container-actions docker-resource-actions"><button class="button secondary small docker-volume-storage" title="Open this volume in Storage" onclick="openDockerVolumeStorage('${escapeHtml(volume.name)}')">Storage</button>${deleteAction}</div><span class="docker-resource-status" title="${escapeHtml(usage)}${(volume.usedBy || []).length ? ` · ${escapeHtml(volume.usedBy.join(", "))}` : ""}">${escapeHtml(usage)}</span></div>`;
}
function openDockerVolumeStorage(name) { setPage("storage"); browseStorage("docker-volumes", name); }
function dockerProjectCard(project, ready) {
  const busy = dockerBusy.has(project.name), managed = !!project.managed, hasCompose = !!project.composeFileExists;
  const error = dockerProjectErrors.get(project.name);
  const active = ["running","partial","degraded"].includes(project.state);
  // `docker compose up -d` is idempotent and also applies configuration changes
  // to an already-running project, so it is valid in every project state.
  const canUp = ready && hasCompose && !busy;
  const canStart = ready && hasCompose && !busy && project.state === "stopped";
  const canStop = ready && !busy && active;
  const canDown = ready && hasCompose && !busy && project.state !== "down";
  const actions = managed ? `<div class="docker-actions">
    <div class="docker-action-group docker-action-lifecycle"><button class="button small" onclick="dockerAction('${project.name}','up')" ${canUp ? "" : "disabled"}>Up</button><button class="button small" onclick="dockerAction('${project.name}','start')" ${canStart ? "" : "disabled"}>Start</button><button class="button small" onclick="dockerAction('${project.name}','stop')" ${canStop ? "" : "disabled"}>Stop</button><button class="button small" onclick="dockerAction('${project.name}','down')" ${canDown ? "" : "disabled"}>Down</button></div>
    <div class="docker-action-group docker-action-files"><button class="button secondary small" onclick="openDockerFile('${project.name}','compose')" ${busy ? "disabled" : ""}>compose.yaml</button><button class="button secondary small" onclick="openDockerFile('${project.name}','env')" ${busy ? "disabled" : ""}>.env</button></div><div class="docker-action-group docker-action-destructive"><button class="button danger small" onclick="deleteDockerProject('${project.name}')" ${!ready || project.state !== "down" || busy ? "disabled" : ""}>Delete</button></div>
  </div>${busy ? `<div class="docker-busy" role="status"><span class="spinner" aria-hidden="true"></span>Running Compose command…</div>` : ""}` : "";
  const containers = (project.containers || []).map(c => dockerContainerRow(c, ready && managed)).join("");
  return `<article class="docker-card"><div class="docker-card-head"><h2>${escapeHtml(project.name)}</h2><span class="status ${escapeHtml(project.state === "running" ? "running" : project.state === "degraded" ? "failure" : project.state || "idle")}">${escapeHtml(project.state || "unknown")}</span></div>${error ? `<div class="docker-project-error" role="alert"><strong>Compose operation failed</strong><span>${escapeHtml(error)}</span></div>` : ""}${actions}${containers}${project.configPath && !managed ? `<div class="docker-path">${escapeHtml(project.configPath)}</div>` : ""}</article>`;
}
function dockerContainerRow(container, ready) {
  const busy=dockerBusy.has(`container:${container.id}`), running=container.state === "running";
  const canAttach=ready && running && !busy && !!systemInfo?.capabilities?.terminal;
  const controls=`<div class="docker-container-actions"><div class="docker-action-group docker-action-lifecycle"><button class="row-icon docker-container-icon" title="Start" aria-label="Start container" onclick="dockerContainerAction('${container.id}','start')" ${ready && !running && !busy ? "" : "disabled"}>▶</button><button class="row-icon docker-container-icon" title="Stop" aria-label="Stop container" onclick="dockerContainerAction('${container.id}','stop')" ${ready && running && !busy ? "" : "disabled"}>■</button></div><div class="docker-action-group docker-action-destructive"><button class="row-icon docker-container-icon" title="Delete stopped container" aria-label="Delete stopped container" onclick="dockerContainerAction('${container.id}','delete')" ${ready && !running && !busy ? "" : "disabled"}>🗑</button></div><div class="docker-action-group docker-action-support"><button class="row-icon docker-container-icon" title="Open terminal" aria-label="Open container terminal" onclick="dockerAttachContainer('${container.id}')" ${canAttach ? "" : "disabled"}>↪</button><button class="row-icon docker-container-icon" title="View logs" aria-label="View logs" onclick="openDockerContainerLog('${container.id}')" ${ready && !busy ? "" : "disabled"}>▤</button></div></div>`;
  return `<div class="docker-container"><span class="docker-dot ${escapeHtml(container.tone || "gray")}"></span><strong class="docker-container-name">${escapeHtml(container.service || container.name)}</strong>${controls}<span class="docker-resource-status">${escapeHtml(container.state || "unknown")}${container.health ? ` · ${escapeHtml(container.health)}` : ""}</span></div>`;
}
async function dockerAction(name, action) {
  dockerBusy.add(name); dockerProjectErrors.delete(name); renderDocker();
  try {
    await api(`api/v1/docker/projects/${encodeURIComponent(name)}/actions/${action}`, {method:"POST"});
    toast(`${name}: ${action} completed`);
  } catch(e) {
    dockerProjectErrors.set(name, e.message); renderDocker(); toast(e.message);
  } finally { dockerBusy.delete(name); await refresh(); }
}
async function dockerContainerAction(id, action) {
  const key=`container:${id}`;
  if(action==="delete"&&!confirm("Delete this stopped container?"))return;
  dockerBusy.add(key); renderDocker();
  try {
    await api(`api/v1/docker/containers/${encodeURIComponent(id)}/actions/${action}`,{method:"POST"});
    dockerPendingContainerStates.set(key,{id,running:action === "start" ? true : action === "stop" ? false : null});
    toast(`Container ${action} completed`);
    await new Promise(resolve => setTimeout(resolve, 400));
    await refreshDockerSnapshot();
  } catch(e) {
    dockerBusy.delete(key); dockerPendingContainerStates.delete(key); renderDocker(); toast(e.message);
  }
}
function openDockerContainerLog(id) { openLog("Container logs",()=>api(`api/v1/docker/containers/${encodeURIComponent(id)}/logs`)); }
async function dockerAttachContainer(id) {
  if (!systemInfo?.capabilities?.terminal) { toast("Interactive terminals are unavailable"); return; }
  disposeDockerAttachTerminal();
  const dialog = $("dockerAttachDialog"), pane = $("dockerAttachPane");
  dialog.showModal();
  pane.replaceChildren();
  const term = new Terminal({cursorBlink:true, scrollback:5000, fontFamily:'"Cascadia Code", Consolas, monospace', fontSize:14, theme:{background:'#0b1220'}});
  const fit = new FitAddon.FitAddon(); term.loadAddon(fit); term.open(pane); fit.fit();
  const session = {term, fit, socket:null, observer:null}; dockerAttachTerminal = session;
  term.onData(data => { if (session.socket?.readyState === WebSocket.OPEN) session.socket.send(new TextEncoder().encode(data)); });
  session.observer = new ResizeObserver(() => { fit.fit(); if (session.socket?.readyState === WebSocket.OPEN) session.socket.send(JSON.stringify({type:"resize",cols:term.cols,rows:term.rows})); });
  session.observer.observe(pane);
  try {
    const ticket = await api(`api/v1/docker/containers/${encodeURIComponent(id)}/attach-ticket`, {method:"POST", body:JSON.stringify({cols:term.cols, rows:term.rows})});
    if (dockerAttachTerminal !== session) return;
    const socket = new RunPilotSecureWebSocket(dockerAttachWebSocketURL(ticket.ticket), systemInfo?.websocketPayloadMode || "disabled"); session.socket = socket; socket.binaryType = "arraybuffer";
    socket.onmessage = event => { if (dockerAttachTerminal === session && event.data instanceof ArrayBuffer) term.write(new Uint8Array(event.data)); };
    socket.onerror = () => { if (dockerAttachTerminal === session) term.writeln("\r\nTerminal connection failed."); };
    socket.onclose = event => { if (dockerAttachTerminal === session && !event.wasClean) term.writeln(`\r\n${event.reason || "Terminal connection closed."}`); };
    socket.onopen = () => { socket.send(JSON.stringify({type:"resize",cols:term.cols,rows:term.rows})); term.focus(); };
  } catch (error) { term.writeln(`\r\n${error.message}`); toast(error.message); }
}
function disposeDockerAttachTerminal() { const session = dockerAttachTerminal; dockerAttachTerminal = null; session?.observer?.disconnect(); session?.socket?.close(); session?.term?.dispose(); }
async function deleteDockerProject(name) { if (!confirm(`Delete the RunPilot project directory for ${name}? This removes compose files and .env only; it never removes Docker images or volumes.`)) return; dockerBusy.add(name); dockerProjectErrors.delete(name); renderDocker(); try { await api(`api/v1/docker/projects/${encodeURIComponent(name)}`, {method:"DELETE"}); dockerProjectErrors.delete(name); toast("Compose project deleted"); } catch(e) { dockerProjectErrors.set(name, e.message); renderDocker(); toast(e.message); } finally { dockerBusy.delete(name); await refresh(); } }
async function deleteDockerVolume(name) { if (!confirm(`Permanently delete Docker volume ${name} and all of its data? This cannot be undone.`)) return; const key=`volume:${name}`; dockerBusy.add(key);renderDocker();try{await api(`api/v1/docker/volumes/${encodeURIComponent(name)}`,{method:"DELETE"});toast("Docker volume deleted");}catch(e){toast(e.message)}finally{dockerBusy.delete(key);await refresh();} }
async function deleteDockerNetwork(name) { if (!confirm(`Delete Docker network ${name}? This cannot be undone.`)) return; const key=`network:${name}`;dockerBusy.add(key);renderDocker();try{await api(`api/v1/docker/networks/${encodeURIComponent(name)}`,{method:"DELETE"});toast("Docker network deleted");}catch(e){toast(e.message)}finally{dockerBusy.delete(key);await refresh();} }
async function openDockerFile(name, kind) { try { const out = await api(`api/v1/docker/projects/${encodeURIComponent(name)}/files/${kind}`); dockerEditing = {name,kind}; $("dockerFileTitle").textContent = `${out.content ? "Edit" : "Create"} ${kind === "compose" ? "compose.yaml" : ".env"}`; $("dockerFilePath").textContent = `${name}/${kind === "compose" ? "compose.yaml" : ".env"}`; $("dockerFileContent").value = out.content || (kind === "compose" ? "services: {}\n" : ""); updateDockerFileHighlight(); $("dockerFileDialog").showModal(); } catch(e) { toast(e.message); } }
async function saveDockerFile() { if (!dockerEditing) return; try { await api(`api/v1/docker/projects/${encodeURIComponent(dockerEditing.name)}/files/${dockerEditing.kind}`, {method:"PUT", body:JSON.stringify({content:$("dockerFileContent").value})}); $("dockerFileDialog").close(); toast("File saved"); await refresh(); } catch(e) { toast(e.message); } }
function openDockerCreate() { $("dockerProjectName").value=""; $("dockerCreateError").classList.add("hidden"); $("dockerCreateDialog").showModal(); }
$("dockerCreateForm").addEventListener("submit", async e => { e.preventDefault(); const name=$("dockerProjectName").value.trim(); try { await api("api/v1/docker/projects", {method:"POST",body:JSON.stringify({name})}); $("dockerCreateDialog").close(); await refresh(); } catch(err) { $("dockerCreateError").textContent=err.message; $("dockerCreateError").classList.remove("hidden"); } });
function openDockerVolumeCreate() { $("dockerVolumeName").value="";$("dockerVolumeError").classList.add("hidden");$("dockerVolumeDialog").showModal(); }
$("dockerVolumeForm").addEventListener("submit",async e=>{e.preventDefault();try{await api("api/v1/docker/volumes",{method:"POST",body:JSON.stringify({name:$("dockerVolumeName").value.trim()})});$("dockerVolumeDialog").close();await refresh();}catch(err){$("dockerVolumeError").textContent=err.message;$("dockerVolumeError").classList.remove("hidden");}});
function openDockerNetworkCreate() { $("dockerNetworkName").value="";$("dockerNetworkError").classList.add("hidden");$("dockerNetworkDialog").showModal(); }
$("dockerNetworkForm").addEventListener("submit",async e=>{e.preventDefault();try{await api("api/v1/docker/networks",{method:"POST",body:JSON.stringify({name:$("dockerNetworkName").value.trim()})});$("dockerNetworkDialog").close();await refresh();}catch(err){$("dockerNetworkError").textContent=err.message;$("dockerNetworkError").classList.remove("hidden");}});
$("saveDockerFile").addEventListener("click", saveDockerFile); $("closeDockerFile").addEventListener("click",()=>$("dockerFileDialog").close()); $("cancelDockerFile").addEventListener("click",()=>$("dockerFileDialog").close());
$("closeDockerAttach").addEventListener("click",()=>$("dockerAttachDialog").close());
$("dockerAttachDialog").addEventListener("close",disposeDockerAttachTerminal);

async function loadSystemInfo() {
  systemInfo = await api("api/v1/system");
  $("systemVersion").textContent = systemInfo?.version ? `v${systemInfo.version}` : "";
  window.runPilotWebSocketPayloadMode = systemInfo?.websocketPayloadMode || "disabled";
  if (connectionOnline) setConnected(true);
  configurePlatformAwareFields(systemInfo.capabilities || {});
}

function configurePlatformAwareFields(capabilities) {
  const labels = {direct:"Direct executable", python:"Python", powershell:"PowerShell", cmd:"CMD / batch", sh:"POSIX shell (sh)", "sh-inline":"Inline shell command", bash:"Bash"};
  const interpreters = new Set(capabilities.commandInterpreters || []);
  document.querySelectorAll("select[id$='Interpreter']").forEach(select => {
    const selected = select.value || "auto";
    for (const option of select.options) {
      if (option.value === "auto" || option.value === "direct") continue;
      option.hidden = !interpreters.has(option.value);
      option.disabled = !interpreters.has(option.value);
      if (labels[option.value]) option.textContent = labels[option.value];
    }
    if (select.querySelector(`option[value="${selected}"]`)?.disabled) select.value = "auto";
  });
  const robocopy = $("backupProvider").querySelector('option[value="robocopy"]');
  if (robocopy) { robocopy.disabled = !capabilities.robocopy; robocopy.hidden = !capabilities.robocopy; }
  const vss = $("resticVss").closest("label");
  if (vss) vss.classList.toggle("hidden", !capabilities.windows);
}

function dockerAttachWebSocketURL(ticket) {
  const wsURL = new URL("api/v1/docker/attach", document.baseURI);
  wsURL.protocol = wsURL.protocol === "https:" ? "wss:" : "ws:";
  wsURL.searchParams.set("ticket", ticket);
  return wsURL;
}

function setPage(page) {
  const registered = pluginNavigation.get(page);
  if (!Object.hasOwn(pageMeta, page) && !registered) return;
  currentPage = page;
  document.querySelectorAll(".nav").forEach(n => n.classList.toggle("active", n.dataset.page === page));
  document.querySelectorAll(".page").forEach(p => p.classList.remove("active"));
  $(`${page}Page`).classList.add("active");
  const headerActions = $("pluginHeaderActions");
  headerActions.replaceChildren();
  headerActions.classList.add("hidden");
  if (registered?.headerActions) {
    registered.headerActions(headerActions);
    headerActions.classList.toggle("hidden", headerActions.childElementCount === 0);
  }
  const [title, action] = pageMeta[page] || [registered?.title || page, null];
  $("pageTitle").textContent = title;
  $("primaryAction").textContent = action || "";
  $("primaryAction").classList.toggle("hidden", !action);
	if (page === "settings") renderPluginSettings();
	if (registered) { registered.page.replaceChildren(); registered.render(registered.page); }
  refresh();
}

document.querySelectorAll(".nav").forEach(n => n.addEventListener("click", () => setPage(n.dataset.page)));
$("restartApplicationButton").addEventListener("click", requestRunPilotRestart);
document.querySelectorAll("[data-software-tab]").forEach(button => button.addEventListener("click", () => { softwareTab = button.dataset.softwareTab; renderSoftware(); loadSoftwareView(); }));
$("softwareSearchButton").addEventListener("click", softwareSearch);
$("softwareUpgradeAll").addEventListener("click", softwareUpgradeAll);
$("softwareAddBucket").addEventListener("click", softwareAddBucket);
$("softwareProviderSelect").addEventListener("change", event => changeSoftwareProvider(event.target.value));
$("softwareSearchInput").addEventListener("keydown", event => { if (event.key === "Enter") { event.preventDefault(); softwareSearch(); } });
$("softwareBucketSource").addEventListener("keydown", event => { if (event.key === "Enter") { event.preventDefault(); softwareAddBucket(); } });
$("dockerVolumeAction").addEventListener("click", openDockerVolumeCreate);
$("dockerNetworkAction").addEventListener("click", openDockerNetworkCreate);
document.querySelectorAll("[data-dismiss]").forEach(button => button.addEventListener("click", () => $(button.dataset.dismiss)?.close()));
document.querySelectorAll("[data-task-filter]").forEach(button => button.addEventListener("click", () => { taskFilter = button.dataset.taskFilter; document.querySelectorAll("[data-task-filter]").forEach(item => item.classList.toggle("active", item === button)); renderTasks(); }));
document.querySelectorAll("[data-task-kind]").forEach(button => button.addEventListener("click", () => { $("taskDialog").close(); if (button.dataset.taskKind === "continuous") openProcess(); else if (button.dataset.taskKind === "scheduled") openJob(); else openBackup(); }));
$("storageCreate").addEventListener("click",async()=>{if(!storageLocation)return;const name=prompt("Folder name:");if(!name)return;try{await api(`api/v1/storage/${storageLocation}/directories`,{method:"POST",body:JSON.stringify({parentPath:storagePath,name})});browseStorage(storageLocation,storagePath)}catch(e){toast(e.message)}});
$("storageNewFile").addEventListener("click",openNewTextFile);
$("storageUpload").addEventListener("change",async e=>{if(!storageLocation||!e.target.files.length)return;const form=new FormData();form.append("path",storagePath);for(const f of e.target.files)form.append("files",f);try{await api(`api/v1/storage/${storageLocation}/upload`,{method:"POST",body:form});browseStorage(storageLocation,storagePath)}catch(err){toast(err.message)}finally{e.target.value=""}});
$("storageProvider").addEventListener("change",e=>browseStorage(e.target.value,""));
$("storageShowHidden").addEventListener("change",e=>{storageShowHidden=e.target.checked;browseStorage(storageLocation,storagePath)});
$("saveTextEditor").addEventListener("click",saveTextEditor);$("closeTextEditor").addEventListener("click",()=>$("textEditorDialog").close());$("cancelTextEditor").addEventListener("click",()=>$("textEditorDialog").close());
$("textEditorContent").addEventListener("input",updateTextHighlight);
$("textEditorContent").addEventListener("scroll",e=>{ $("textHighlight").scrollTop=e.target.scrollTop; $("textHighlight").scrollLeft=e.target.scrollLeft; });
$("dockerFileContent").addEventListener("input",updateDockerFileHighlight);
$("dockerFileContent").addEventListener("scroll",e=>{ $("dockerFileHighlight").scrollTop=e.target.scrollTop; $("dockerFileHighlight").scrollLeft=e.target.scrollLeft; });

function openProcess(existing = null) {
  $("processForm").reset();
  $("processId").value = existing?.id || "";
  $("processName").value = existing?.name || "";
  populateCommandEditor("process", existing?.command);
  $("processRestart").value = existing?.restart?.mode || "on-failure";
  $("processAutostart").checked = !!existing?.autostart;
  $("processDialogTitle").textContent = existing ? "Edit continuous task" : "Add continuous task";
  $("processDialog").showModal();
}
function editProcess(id) { openProcess(processes.find(v => v.definition.id === id)?.definition); }

$("processForm").addEventListener("submit", async e => {
  e.preventDefault();
  const id = $("processId").value;
  const command = commandFromEditor("process");
  if (!command) return;
  const body = {
    name: $("processName").value.trim(),
    autostart: $("processAutostart").checked,
    command,
    restart: {
      mode: $("processRestart").value,
      initialDelaySeconds: 2,
      maxDelaySeconds: 60,
      maxRetries: 0,
    }
  };
  try {
    await api(id ? `api/v1/processes/${id}` : "api/v1/processes", {method: id ? "PUT" : "POST", body: JSON.stringify(body)});
    $("processDialog").close(); await refresh();
  } catch (err) { setCommandError("process", err.message); }
});

function setScheduleFields(prefix, type) {
  for (const key of ["Interval","Daily","Cron"]) {
    const el = $(`${prefix}${key}Wrap`);
    if (el) el.classList.toggle("hidden", key.toLowerCase() !== type);
  }
}
$("jobScheduleType").addEventListener("change", e => setScheduleFields("job", e.target.value));
$("backupScheduleType").addEventListener("change", e => setScheduleFields("backup", e.target.value));

function scheduleFrom(prefix) {
  const type = $(`${prefix}ScheduleType`).value;
  if (type === "interval") return {type, intervalSeconds: Number($(`${prefix}Interval`).value) * 60};
  if (type === "daily") return {type, timeOfDay: $(`${prefix}Daily`).value, timeZone: ""};
  return {type, cron: $(`${prefix}Cron`).value.trim(), timeZone: ""};
}
function fillSchedule(prefix, s = {}) {
  const type = s.type || (prefix === "backup" ? "daily" : "interval");
  $(`${prefix}ScheduleType`).value = type;
  if ($(`${prefix}Interval`)) $(`${prefix}Interval`).value = Math.max(1, Math.round((s.intervalSeconds || (prefix === "job" ? 1200 : 3600))/60));
  if ($(`${prefix}Daily`)) $(`${prefix}Daily`).value = s.timeOfDay || "03:00";
  if ($(`${prefix}Cron`)) $(`${prefix}Cron`).value = s.cron || "";
  setScheduleFields(prefix, type);
}

function openJob(existing = null) {
  $("jobForm").reset();
  $("jobId").value = existing?.id || "";
  $("jobName").value = existing?.name || "";
  populateCommandEditor("job", existing?.command);
  $("jobEnabled").checked = existing ? !!existing.enabled : true;
  fillSchedule("job", existing?.schedule || {});
  $("jobDialogTitle").textContent = existing ? "Edit scheduled task" : "Add scheduled task";
  $("jobDialog").showModal();
}
function editJob(id) { openJob(jobs.find(v => v.definition.id === id)?.definition); }

$("jobForm").addEventListener("submit", async e => {
  e.preventDefault();
  const id = $("jobId").value;
  const command = commandFromEditor("job");
  if (!command) return;
  const body = {
    name: $("jobName").value.trim(),
    enabled: $("jobEnabled").checked,
    type: "command",
    overlapPolicy: "skip",
    schedule: scheduleFrom("job"),
    command,
  };
  try {
    await api(id ? `api/v1/jobs/${id}` : "api/v1/jobs", {method: id ? "PUT" : "POST", body: JSON.stringify(body)});
    $("jobDialog").close(); await refresh();
  } catch (err) { setCommandError("job", err.message); }
});

function openBackup(existing = null) {
  $("backupForm").reset();
  const b = existing?.backup || {};
  $("backupId").value = existing?.id || "";
  $("backupName").value = existing?.name || "";
  const provider = b.engine || "robocopy", r = b.robocopy || b, restic = b.restic || {}, rdiff = b.rdiffBackup || {};
  $("backupProvider").value = provider;
  $("backupSource").value = r.source || ""; $("backupDestination").value = r.destination || ""; $("backupMode").value = r.mode || "copy"; $("backupRetries").value = r.retries ?? 2;
  $("resticRepository").value=restic.repository||""; $("resticSources").value=(restic.sources||[]).join("\n"); $("resticExcludes").value=(restic.excludes||[]).join("\n"); $("resticTags").value=(restic.tags||[]).join("\n"); $("resticVss").checked=!!restic.useVss; $("resticCheck").checked=!!restic.checkAfterBackup; $("resticPasswordFile").value=restic.passwordFile||""; $("resticExecutable").value=restic.executable||"";
  const rt=restic.retention||{}; $("resticRetentionEnabled").checked=!!restic.retention; $("resticKeepLast").value=rt.keepLast||""; $("resticKeepDaily").value=rt.keepDaily||""; $("resticKeepWeekly").value=rt.keepWeekly||""; $("resticKeepMonthly").value=rt.keepMonthly||""; $("resticKeepYearly").value=rt.keepYearly||""; $("resticKeepWithin").value=rt.keepWithin||""; $("resticPrune").checked=!!rt.prune;
  $("rdiffSource").value=rdiff.source||""; $("rdiffDestination").value=rdiff.destination||""; $("rdiffExcludes").value=(rdiff.excludes||[]).join("\n"); $("rdiffRetentionEnabled").checked=!!rdiff.retention; $("rdiffOlderThan").value=rdiff.retention?.olderThan||""; $("rdiffVerify").checked=!!rdiff.verifyAfterBackup; $("rdiffExecutable").value=rdiff.executable||"";
  $("backupEnabled").checked = existing ? !!existing.enabled : true;
  fillSchedule("backup", existing?.schedule || {type:"daily", timeOfDay:"03:00"});
  updateMirrorWarning();
  updateBackupProvider();
  $("backupDialogTitle").textContent = existing ? "Edit backup task" : "Add backup task";
  $("backupDialog").showModal();
}
function editBackup(id) { openBackup(jobs.find(v => v.definition.id === id)?.definition); }
function updateMirrorWarning() { $("mirrorWarning").classList.toggle("hidden", $("backupMode").value !== "mirror"); }
$("backupMode").addEventListener("change", updateMirrorWarning);
function lines(id) { return $(id).value.split("\n").map(v=>v.trim()).filter(Boolean); }
function updateBackupProvider() {
  const p=$("backupProvider").value, robo=p==="robocopy";
  ["backupMode","backupSource","backupDestination","backupRetries"].forEach(id=>$(id).closest("label").classList.toggle("hidden",!robo));
  $("resticFields").classList.toggle("hidden",p!=="restic"); $("rdiffFields").classList.toggle("hidden",p!=="rdiff-backup");
  $("mirrorWarning").classList.toggle("hidden",!robo || $("backupMode").value!=="mirror");
  $("resticRetentionFields").classList.toggle("hidden",!$("resticRetentionEnabled").checked); $("rdiffRetentionWrap").classList.toggle("hidden",!$("rdiffRetentionEnabled").checked);
}
$("backupProvider").addEventListener("change", updateBackupProvider); $("resticRetentionEnabled").addEventListener("change",updateBackupProvider); $("rdiffRetentionEnabled").addEventListener("change",updateBackupProvider);

$("backupForm").addEventListener("submit", async e => {
  e.preventDefault();
  const id = $("backupId").value;
  const body = {
    name: $("backupName").value.trim(),
    enabled: $("backupEnabled").checked,
    type: "backup",
    overlapPolicy: "skip",
    schedule: scheduleFrom("backup"),
    backup: { engine: $("backupProvider").value }
  };
  if (body.backup.engine === "robocopy") body.backup.robocopy={source:$("backupSource").value.trim(),destination:$("backupDestination").value.trim(),mode:$("backupMode").value,retries:Number($("backupRetries").value),retryWaitSeconds:5};
  if (body.backup.engine === "restic") { const retention=$("resticRetentionEnabled").checked?{keepLast:Number($("resticKeepLast").value)||0,keepDaily:Number($("resticKeepDaily").value)||0,keepWeekly:Number($("resticKeepWeekly").value)||0,keepMonthly:Number($("resticKeepMonthly").value)||0,keepYearly:Number($("resticKeepYearly").value)||0,keepWithin:$("resticKeepWithin").value.trim(),prune:$("resticPrune").checked}:null; body.backup.restic={repository:$("resticRepository").value.trim(),sources:lines("resticSources"),excludes:lines("resticExcludes"),tags:lines("resticTags"),useVss:$("resticVss").checked,checkAfterBackup:$("resticCheck").checked,passwordFile:$("resticPasswordFile").value.trim(),executable:$("resticExecutable").value.trim(),retention}; }
  if (body.backup.engine === "rdiff-backup") body.backup.rdiffBackup={source:$("rdiffSource").value.trim(),destination:$("rdiffDestination").value.trim(),excludes:lines("rdiffExcludes"),verifyAfterBackup:$("rdiffVerify").checked,executable:$("rdiffExecutable").value.trim(),retention:$("rdiffRetentionEnabled").checked?{olderThan:$("rdiffOlderThan").value.trim()}:null};
  try {
    await api(id ? `api/v1/jobs/${id}` : "api/v1/jobs", {method: id ? "PUT" : "POST", body: JSON.stringify(body)});
    $("backupDialog").close(); await refresh();
  } catch (err) { toast(err.message); }
});

async function openProcessLog(id, name) {
  openLog(name, async () => api(`api/v1/processes/${id}/log?lines=500`));
}
async function openRunLog(id, name) {
  openLog(name, async () => api(`api/v1/runs/${id}/log?lines=800`));
}
async function openLog(name, loader) {
  if (logTimer) clearInterval(logTimer);
  logSource = loader;
  $("logTitle").textContent = name;
  $("logSubtitle").textContent = "Latest captured stdout/stderr";
  $("logOutput").textContent = "Loading…";
  $("logDialog").showModal();
  const load = async () => {
    try { $("logOutput").textContent = await loader() || "(no output)"; }
    catch (e) { $("logOutput").textContent = e.message; }
  };
  await load();
  logTimer = setInterval(load, 2000);
}
$("closeLog").addEventListener("click", () => {
  if (logTimer) clearInterval(logTimer);
  logTimer = null; logSource = null; $("logDialog").close();
});
$("closeRunHistory").addEventListener("click", () => $("runHistoryDialog").close());

$("loginForm").addEventListener("submit", async e => {
  e.preventDefault();
  const candidate = $("tokenInput").value.trim();
  const old = token;
  token = candidate;
  try {
    await api("api/v1/system");
    RunPilotAuthStorage.save(sessionStorage, localStorage, token);
    $("loginError").classList.add("hidden");
    $("loginDialog").close();
    setConnected(true);
    await loadSystemInfo();
    await connectApplicationSocket();
    await refresh();
    startAutoRefresh();
    startConnectionMonitor();
  } catch {
    token = old;
    $("loginError").textContent = "The token was rejected.";
    $("loginError").classList.remove("hidden");
  }
});

(async function init() {
  initializeSidebar();
  initializeTheme();
  if (!token) {
    $("loginDialog").showModal();
    return;
  }
  setConnected(false);
  startConnectionMonitor();
  try { await connectApplicationSocket(); } catch (e) { toast(e.message); }
  await refresh();
  try { await loadSystemInfo(); } catch (e) { if (e.message !== "Unauthorized") toast(e.message); }
  startAutoRefresh();
})();

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) { refresh(); probeConnection(); }
});
