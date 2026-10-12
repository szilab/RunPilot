try { localStorage.removeItem("runpilot.token"); } catch {}
try { sessionStorage.removeItem("runpilot.token"); } catch {}
const $ = (id) => document.getElementById(id);
let authenticated = false;
let currentPage = "overview";
let storage = [], storageLocation = null, storagePath = "";
let softwareProviders = [], softwareProviderID = "", softwarePackages = [], softwareUpdates = [], softwareSearchResults = [], softwareBuckets = [], softwareTab = "installed", softwareBusy = false, softwareBusyLabel = "", softwareLoading = false, softwareLoadingKey = "", softwareLoadedKey = "", softwareLoadSequence = 0, softwareRootDrafts = {};
let storageClipboard = null, editingTextPath = null, storageShowHidden = false, storagePathCapabilities = null;
let overview = null;
let systemInfo = null;
let pluginStatuses = [], pluginBusy = new Set();
let lastPluginSnapshot = "";
let pluginDiscoveryErrors = [];
let pluginCatalog = [], pluginCatalogError = "", pluginCatalogLoaded = false, pluginCatalogLoading = false;
let pluginExtensions = new Map();
const pluginNavigation = new Map(), pluginSettings = new Map();
const overviewWidgets = new RunPilotOverviewWidgets($("overviewWidgets"));
const hostDashboard = new RunPilotDashboard.HostDashboard($("hostDashboard"), api, {onUpdate:updateHostSummary});
const pluginThemeListeners = new Set();
let applicationSocket = null, applicationSocketPromise = null, applicationSequence = 0;
const applicationPending = new Map(), applicationListeners = new Map(), applicationStreams = new Map();
let toastTimer = null;
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
  call: async (plugin,method,params={})=>{ const socket=await connectApplicationSocket(); const id=String(++applicationSequence); return new Promise((resolve,reject)=>{const timer=setTimeout(()=>{applicationPending.delete(id);reject(new Error("plugin request timed out"));},130000); applicationPending.set(id,{resolve,reject,timer}); socket.send(JSON.stringify({id,plugin,method,params}));}); },
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
  const surfaceElement=document.createElement("div");surfaceElement.className="rp-interactive-surface";surfaceElement.tabIndex=0;surfaceElement.setAttribute("role","application");surfaceElement.dataset.rpInteractive="";surfaceElement.setAttribute("aria-label",`${title} interactive surface`);
  const loading=document.createElement("div");loading.className="rp-interactive-loading";loading.setAttribute("role","status");loading.textContent="Connecting…";
  const error=document.createElement("div");error.className="rp-interactive-error";error.setAttribute("role","alert");error.hidden=true;
  surfaceElement.append(loading,error);element.append(toolbar,surfaceElement);
  const placeholder=document.createComment("interactive session position");container.replaceChildren(placeholder,element);
  let disposed=false,frame=0,resizeTimer=0,lastSize=null,fullscreenState=null;
  const resizeListeners=new Set(),fullscreenListeners=new Set();
  const measure=()=>({width:Math.max(0,Math.round(surfaceElement.clientWidth)),height:Math.max(0,Math.round(surfaceElement.clientHeight))});
  const fitViewport=()=>{if(disposed)return;if(isFullscreen()||element.classList.contains("rp-page-view")){element.style.height="";return;}const main=element.closest("main"),bottom=main?parseFloat(getComputedStyle(main).paddingBottom)||0:0,height=Math.max(300,Math.floor(window.innerHeight-element.getBoundingClientRect().top-bottom));if(element.style.height!==`${height}px`)element.style.height=`${height}px`;};
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
  const setPageView=active=>{const enabled=!!active;element.classList.toggle("rp-page-view",enabled);document.body.classList.toggle("rp-interactive-page-view",enabled);if(enabled&&element.parentElement!==document.body)document.body.append(element);else if(!enabled&&placeholder.parentNode)placeholder.parentNode.insertBefore(element,placeholder.nextSibling);scheduleSize();};
  const view={element,surface,actions,setStatus,setLoading,setError,setPageView,dispose(){if(disposed)return;disposed=true;document.body.classList.remove("rp-interactive-page-view");if(frame)cancelAnimationFrame(frame);clearTimeout(resizeTimer);resizeObserver?.disconnect();window.removeEventListener("resize",scheduleSize);document.removeEventListener("fullscreenchange",reportFullscreen);resizeListeners.clear();fullscreenListeners.clear();element.remove();placeholder.remove();}};
  scheduleSize();return view;
}
function pluginUI() {
  return Object.freeze({
    escape: escapeHtml,
    toast,
    createInteractiveSessionView,
    confirm: confirmAction,
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
      card.className = `rp-card metric ${className}`;
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
  const button = document.createElement("button"); button.className = "nav"; button.type = "button"; button.dataset.page = entry.id; button.title = entry.title || entry.id; button.setAttribute("aria-current", "false"); button.innerHTML = `${pluginNavigationIcon(entry.icon)}<span class="nav-label">${escapeHtml(entry.title || entry.id)}</span>`;
  document.querySelector("nav").append(button);
  const page = document.createElement("section"); page.id = entry.id + "Page"; page.className = "page"; document.querySelector("main").append(page);
  pluginNavigation.set(entry.id, { ...entry, button, page, extension });
  button.addEventListener("click", () => setPage(entry.id));
}
function registerPluginOverview(extension, entry) {
  if (entry && !entry.mount && typeof entry.render === "function") {
    const draw = body => { const node = entry.render(); body.replaceChildren(); if (node) body.append(node); };
    return overviewWidgets.register(extension, {...entry, title:entry.title || entry.id, mount:draw, refresh:draw});
  }
  return overviewWidgets.register(extension, entry);
}
function registerPluginSettings(extension, entry) { if(!entry||typeof entry.id!=="string"||!entry.id||typeof entry.render!=="function"||pluginSettings.has(entry.id)) throw new Error("invalid or duplicate settings section"); pluginSettings.set(entry.id,{...entry,extension}); }

function confirmAction({title = "Continue?", message = "", confirmLabel = "Continue", danger = false} = {}) {
  const previousFocus = document.activeElement;
  return new Promise(resolve => {
    const dialog = document.createElement("dialog");
    dialog.className = "dialog small confirmation-dialog";
    dialog.innerHTML = `<form method="dialog"><div class="dialog-head"><div><h2 id="confirmationTitle">${escapeHtml(title)}</h2><p id="confirmationMessage">${escapeHtml(message)}</p></div></div><div class="dialog-actions"><button class="button secondary" value="cancel" autofocus>Cancel</button><button class="button ${danger ? "danger" : "primary"}" value="confirm">${escapeHtml(confirmLabel)}</button></div></form>`;
    dialog.setAttribute("aria-labelledby", "confirmationTitle");
    dialog.setAttribute("aria-describedby", "confirmationMessage");
    dialog.addEventListener("close", () => {
      const confirmed = dialog.returnValue === "confirm";
      dialog.remove();
      if (previousFocus?.isConnected) previousFocus.focus({preventScroll:true});
      resolve(confirmed);
    }, {once:true});
    document.body.append(dialog);
    dialog.showModal();
  });
}

function selectSettingsTab(id, focus = false) {
  for (const tab of document.querySelectorAll(".settings-tab")) {
    const selected = tab.id === id;
    tab.setAttribute("aria-selected", String(selected));
    tab.tabIndex = selected ? 0 : -1;
    $(tab.getAttribute("aria-controls")).hidden = !selected;
    if (selected && focus) tab.focus();
  }
}
for (const tab of document.querySelectorAll(".settings-tab")) {
  tab.addEventListener("click", () => selectSettingsTab(tab.id));
  tab.addEventListener("keydown", event => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const id = event.key === "Home" ? "generalSettingsTab" : event.key === "End" ? "pluginSettingsTab" : tab.id === "generalSettingsTab" ? "pluginSettingsTab" : "generalSettingsTab";
    selectSettingsTab(id, true);
  });
}
function updatePageHeading() {
  document.body.dataset.currentPage = currentPage;
  const entry = pluginNavigation.get(currentPage);
  const now = new Date(), hour = now.getHours();
  const greeting = hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";
  $("pageTitle").textContent = currentPage === "overview" ? `${greeting}${systemInfo?.username ? `, ${systemInfo.username}` : ""}` : (pageMeta[currentPage]?.[0] || entry?.title || currentPage);
  $("pageEyebrow").textContent = currentPage === "overview" ? "YOUR WORKSPACE, AT A GLANCE" : "";
  $("pageEyebrow").hidden = currentPage !== "overview";
  $("pageDescription").hidden = currentPage === "overview" || currentPage === "settings";
  $("pageDescription").textContent = currentPage === "overview" || currentPage === "settings" ? "" : entry?.description || "Manage this workspace.";
  $("overviewClock").hidden = currentPage !== "overview";
  $("overviewClock").innerHTML = `<span>${escapeHtml(now.toLocaleDateString(undefined,{weekday:"short",month:"short",day:"numeric",year:"numeric"}))}</span><strong>${escapeHtml(now.toLocaleTimeString(undefined,{hour:"2-digit",minute:"2-digit"}))}</strong>`;
}

function updateHostSummary(data) {
  const host=data?.host||{};
  const metrics=RunPilotDashboard.readings(data), {size,uptime}=RunPilotDashboard;
  const info=[["Operating system",host.os||"Unavailable"],["Architecture",host.architecture||"Unavailable"],["Processor",host.cpuModel||"Unavailable"],["Logical CPUs",host.cpuCount||"Unavailable"],["Total memory",metrics.totalMemory?size(metrics.totalMemory):"Unavailable"],["Host uptime",uptime(data.hostUptimeSeconds)],["RunPilot",systemInfo?.version||"Unavailable"]];
  $("settingsHostFacts").innerHTML=info.map(([name,value])=>`<div><span>${escapeHtml(name)}</span><strong>${escapeHtml(value)}</strong></div>`).join("");
  if (currentPage === "overview") updatePageHeading();
}
async function ensureHostSummary() {
  if(hostDashboard.lastData){updateHostSummary(hostDashboard.lastData);return;}
  try{updateHostSummary(await api("api/v1/dashboard/status"));}catch(error){$("settingsHostFacts").textContent=`Host information unavailable: ${error.message}`;}
}

document.addEventListener("keydown",event=>{if(event.target.closest?.("[data-rp-interactive], [role=application]"))return;if(event.key==="Escape")document.querySelector(".account-menu").open=false;});
document.addEventListener("pointerdown",event=>{if(!event.target.closest(".account-menu"))document.querySelector(".account-menu").open=false;});

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  const csrf = document.cookie.split(";").map(value => value.trim()).find(value => value.startsWith("runpilot_csrf="))?.split("=").slice(1).join("=");
  if (csrf && options.method && !["GET", "HEAD", "OPTIONS"].includes(options.method.toUpperCase())) headers.set("X-RunPilot-CSRF", decodeURIComponent(csrf));
  if (options.body && !(options.body instanceof FormData) && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const res = await fetch(path, {...options, headers, credentials:"same-origin"});
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
  const mode = systemInfo?.websocketPayloadMode || "disabled";
  const encrypted = mode === "required";
  $("connectionDot").classList.toggle("ok", ok && encrypted);
  $("connectionDot").classList.toggle("unencrypted", ok && !encrypted);
  const encryptionStatus = encrypted ? "Encrypted" : "Unencrypted";
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
  const toggle = $("themeToggle");
  toggle.setAttribute("aria-pressed", String(isDark));
  const nextTheme = isDark ? "light" : "dark";
  toggle.title = `Switch to ${nextTheme} theme`;
  toggle.innerHTML = `<svg viewBox="0 0 24 24" aria-hidden="true">${isDark ? '<circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"/>' : '<path d="M20.5 14A8.5 8.5 0 0 1 10 3.5 8.5 8.5 0 1 0 20.5 14Z"/>'}</svg><span>${isDark ? "Light" : "Dark"} theme</span>`;
  toggle.setAttribute("aria-label", toggle.title);
  notifyPluginThemeListeners();
}

function initializeTheme() {
  const saved = localStorage.getItem(themeStorageKey);
  applyTheme(saved || "system");
  $("themeToggle").addEventListener("click", () => {
    const nextTheme = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    localStorage.setItem(themeStorageKey, nextTheme);
    applyTheme(nextTheme);
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

function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, ch => ({
    "&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"
  }[ch]));
}

async function refresh() {
  if (refreshing || !authenticated) return;
  refreshing = true;
  try {
    const [pluginSnapshot, frontendSnapshot] = await Promise.all([
      currentPage === "settings" ? api("api/v1/plugins") : Promise.resolve(null),
      api("api/v1/plugins/runtime")
    ]);
    if (pluginSnapshot && JSON.stringify(pluginSnapshot) !== lastPluginSnapshot) { lastPluginSnapshot = JSON.stringify(pluginSnapshot); pluginStatuses = pluginSnapshot.plugins || []; pluginDiscoveryErrors=pluginSnapshot.discoveryErrors || []; renderPluginSettings(); if (!pluginCatalogLoaded && !pluginCatalogLoading) refreshPluginCatalog(); }
    if (frontendSnapshot) await loadPluginExtensions(frontendSnapshot);
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
      try { if (typeof loaded.root === "function") loaded.root(); else if (typeof loaded.root?.dispose === "function") loaded.root.dispose(); else loaded.root?.remove?.(); }
      catch (error) { console.error(`plugin ${id} cleanup`, error); }
      for (const [key, entry] of pluginNavigation) if (entry.extension === id) { entry.button.remove(); entry.page.remove(); pluginNavigation.delete(key); if (currentPage === key) { $("pluginHeaderActions").replaceChildren(); $("pluginHeaderActions").classList.add("hidden"); } }
      overviewWidgets.unregisterOwner(id);
      for (const [key, entry] of pluginSettings) if (entry.extension === id) pluginSettings.delete(key);
      document.querySelectorAll('link[data-runpilot-plugin]').forEach(link => { if (link.dataset.runpilotPlugin === id) link.remove(); });
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
      navigation: Object.freeze({ register: entry => registerPluginNavigation(extension.id, entry), open: page => setPage(page) }),
      overview: Object.freeze({ register: entry => registerPluginOverview(extension.id, entry), refresh: () => overviewWidgets.refresh(), render: () => overviewWidgets.refresh() }),
      settings: Object.freeze({ register: entry => registerPluginSettings(extension.id, entry), close: () => { const dialog = document.querySelector(".plugin-settings-dialog[open]"); if (dialog?.dataset.pluginId === extension.id) dialog.close(); } }),
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
  root.innerHTML = `${pluginDiscoveryErrors.map(error=>`<p class="meta">${escapeHtml(error)}</p>`).join("")}${pluginCatalogLoading || pluginCatalogError ? `<p class="meta">${escapeHtml(pluginCatalogLoading ? "Checking catalog…" : pluginCatalogError)}</p>` : ""}` + ids.map(id => {
    const status=installed.get(id), entry=catalog.get(id), manifest=status?.manifest || entry || {}, busy=pluginBusy.has(id);
    const enabled=!!status?.enabled, compatible=entry?.latestCompatible;
    const update=!!status?.source && compatible && entry?.updateAvailable;
    const version=status ? manifest.version : compatible || entry?.latestVersion || entry?.version;
    const stateLabel=status ? `Installed${status.message ? `: ${status.message}` : ""}` : "Not installed";
    const settingsEntries=[...pluginSettings.values()].filter(item=>item.extension===id);
    const buttons = status
      ? `<button class="plugin-enable-switch" role="switch" aria-checked="${enabled}" aria-label="${enabled ? "Disable" : "Enable"} ${escapeHtml(manifest.name || id)}" type="button" ${busy || (!enabled && status.state === "incompatible") ? "disabled" : ""} onclick="togglePlugin('${escapeHtml(id)}',${!enabled})"><span class="switch-track" aria-hidden="true"></span><span>${enabled ? "Enabled" : "Disabled"}</span></button>${update ? `<button class="button primary small" type="button" ${busy ? "disabled" : ""} onclick="managePlugin('${escapeHtml(id)}','install','${escapeHtml(compatible)}')">Update to ${escapeHtml(compatible)}</button>` : ""}${settingsEntries.length ? `<button class="button secondary small" type="button" onclick="openPluginSettings('${escapeHtml(id)}')">Settings</button>` : ""}<button class="button danger small" type="button" ${busy ? "disabled" : ""} onclick="managePlugin('${escapeHtml(id)}','uninstall')">Uninstall</button>`
      : `<button class="button primary small" type="button" ${busy || !compatible ? "disabled" : ""} onclick="managePlugin('${escapeHtml(id)}','install','${escapeHtml(compatible || "")}')">Install${compatible ? " " + escapeHtml(compatible) : ""}</button>`;
    const nav = [...pluginNavigation.values()].find(item => item.extension === id);
    const glyph = nav ? pluginNavigationIcon(nav.icon) : RunPilotDashboard.icon("plugins");
    return `<article class="plugin-setting-card" data-plugin-id="${escapeHtml(id)}"><div class="plugin-setting-heading"><span class="feature-icon">${glyph}</span><div class="plugin-setting-copy"><div class="plugin-setting-title-row"><strong title="${escapeHtml(manifest.name || id)}">${escapeHtml(manifest.name || id)}</strong><span class="status plugin-setting-version ${status ? "is-installed" : "is-not-installed"}" title="${escapeHtml(stateLabel)}${entry?.incompatibility ? ` · ${escapeHtml(entry.incompatibility)}` : ""}" aria-label="${escapeHtml(stateLabel)}">${version ? `v${escapeHtml(version)}` : "—"}</span></div><div class="meta plugin-setting-description">${escapeHtml(manifest.description || "")}</div></div></div><div class="plugin-setting-actions">${buttons}</div></article>`;
  }).join("");
}
function openPluginSettings(id) {
  const entries = [...pluginSettings.values()].filter(entry => entry.extension === id);
  if (!entries.length) return;
  const card = [...document.querySelectorAll(".plugin-setting-card")].find(item => item.dataset.pluginId === id);
  const title = card?.querySelector(".plugin-setting-heading strong")?.textContent || id;
  const dialog = document.createElement("dialog"); dialog.className = "dialog plugin-settings-dialog";
  dialog.dataset.pluginId = id;
  dialog.innerHTML = `<div class="dialog-head"><h2>${escapeHtml(title)} settings</h2><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div><div class="plugin-settings-dialog-body"></div>`;
  const body = dialog.querySelector(".plugin-settings-dialog-body");
  for (const entry of entries) {
    try { const content = entry.render(); if (content) { const section = document.createElement("section"); section.className = "plugin-settings-section"; section.append(content); body.append(section); } }
    catch(error) { console.error(`plugin settings ${entry.id}`,error); }
  }
  dialog.querySelector("[data-close]").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => dialog.remove(), {once:true});
  dialog.addEventListener("click", event => { if (event.target === dialog) dialog.close(); });
  document.body.append(dialog); dialog.showModal();
  dialog.querySelector("input:not(:disabled), select:not(:disabled), button:not(:disabled)")?.focus();
}
async function startRunPilotRestart(button) {
  button.disabled=true;
  try { await api("api/v1/restart",{method:"POST"}); toast("RunPilot is restarting. This page will reconnect shortly."); }
  catch(error) { toastError(error.message); button.disabled=false; }
}
async function requestRunPilotRestart() {
  if (!await confirmAction({title:"Restart RunPilot?",message:"Interactive sessions will disconnect. This page will reconnect when RunPilot is ready.",confirmLabel:"Restart"})) return;
  await startRunPilotRestart($("restartApplicationButton"));
}
function showPluginRestartDialog(id, enabled) {
  const plugin=pluginStatuses.find(item=>item.manifest.id===id), name=plugin?.manifest.name || id;
  const dialog=document.createElement("dialog"); dialog.className="dialog plugin-restart-dialog";
  dialog.innerHTML=`<div class="dialog-head"><div><h2>Restart required</h2><p>${escapeHtml(name)} was ${enabled?"enabled":"disabled"}. Restart RunPilot to apply this change.</p></div><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div><div class="dialog-actions"><button class="button secondary" type="button" data-close>Later</button><button class="button primary" type="button" data-restart>Restart now</button></div>`;
  dialog.querySelectorAll("[data-close]").forEach(button=>button.addEventListener("click",()=>dialog.close()));
  dialog.querySelector("[data-restart]").addEventListener("click",async event=>{const button=event.currentTarget;await startRunPilotRestart(button);if(button.disabled)dialog.close();});
  dialog.addEventListener("close",()=>dialog.remove(),{once:true});
  dialog.addEventListener("click",event=>{if(event.target===dialog)dialog.close();});
  document.body.append(dialog);dialog.showModal();dialog.querySelector("[data-close]").focus();
}
async function togglePlugin(id, enabled) {
  if (pluginBusy.has(id)) return; pluginBusy.add(id); renderPluginSettings();
  let changed=false, restartRequired=false;
  try { const response=await api(`api/v1/plugins/${encodeURIComponent(id)}`,{method:"PUT",body:JSON.stringify({enabled})}); pluginStatuses=response.plugins||pluginStatuses; restartRequired=!!response.restartRequired; changed=true; }
  catch(error) { toastError(error.message); }
  finally { pluginBusy.delete(id); renderPluginSettings(); }
  if(changed&&restartRequired)showPluginRestartDialog(id,enabled);
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
  if (action === "uninstall" && !await confirmAction({title:"Uninstall plugin?",message:"Plugin data will be retained. Restart RunPilot to finish deactivation.",confirmLabel:"Uninstall",danger:true})) return;
  pluginBusy.add(id); renderPluginSettings();
  try {
    const response=await api(`api/v1/plugins/${encodeURIComponent(id)}${action === "install" ? "/install" : ""}`, {method:action === "install" ? "POST" : "DELETE", ...(action === "install" ? {body:JSON.stringify({version})} : {})});
    pluginStatuses=response.plugins || []; toast("Package changed."); await refreshPluginCatalog();
  } catch(error) { toastError(error.message); }
  finally { pluginBusy.delete(id); renderPluginSettings(); }
}

function startAutoRefresh() {
  if (refreshTimer) return;
  refreshTimer = setInterval(() => {
    if (!document.hidden) refresh();
  }, 5000);
  setInterval(() => { if (!document.hidden && currentPage === "overview") void overviewWidgets.refresh(); }, 30000);
}

async function probeConnection() {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 3500);
  try {
    await fetch("api/v1/system", {credentials:"same-origin", signal:controller.signal});
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
async function openTextEditor(id,path,name) { try { const data=await api(`api/v1/storage/${id}/text?`+new URLSearchParams({path})); editingTextPath={id,path};const canEdit=!!(storagePathCapabilities||storage.find(x=>x.id===id)?.capabilities||{}).textEdit;$("textEditorTitle").textContent=`${canEdit ? "Edit" : "View"}: ${name}`;$("textEditorPath").textContent=path;$("textEditorContent").value=data.content;$("textEditorContent").readOnly=!canEdit;$("saveTextEditor").disabled=!canEdit;updateTextHighlight();$("textEditorDialog").showModal(); }catch(e){toast(e.message)} }
function openNewTextFile() { if (!storageLocation || !(storagePathCapabilities||{}).textEdit) return; const name=prompt("File name:"); if (!name) return; if (name === "." || name === ".." || /[\\/]/.test(name)) { toast("File name must be a single name."); return; } const path=storagePath ? `${storagePath}/${name}` : name; editingTextPath={id:storageLocation,path};$("textEditorTitle").textContent=`New file: ${name}`;$("textEditorPath").textContent=path;$("textEditorContent").value="";$("textEditorContent").readOnly=false;$("saveTextEditor").disabled=false;updateTextHighlight();$("textEditorDialog").showModal(); }
async function saveTextEditor() { if(!editingTextPath)return;try{await api(`api/v1/storage/${editingTextPath.id}/text`,{method:"PUT",body:JSON.stringify({path:editingTextPath.path,content:$("textEditorContent").value})});$("textEditorDialog").close();toast("Fájl mentve");browseStorage(storageLocation,storagePath)}catch(e){toast(e.message)} }

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

async function loadSystemInfo() {
  systemInfo = await api("api/v1/system");
  $("systemVersion").textContent = systemInfo?.version ? `v${systemInfo.version}` : "";
  window.runPilotWebSocketPayloadMode = systemInfo?.websocketPayloadMode || "disabled";
  if (connectionOnline) setConnected(true);
  $("bearerAuthEnabled").checked=!!systemInfo.bearerAuthEnabled;
  $("websocketPayloadMode").value=systemInfo.websocketPayloadModeConfigured || systemInfo.websocketPayloadMode || "disabled";
  const username = systemInfo.username || "Administrator";
  $("accountName").textContent = username;
  $("accountMenuName").textContent = username;
  $("accountAvatar").textContent = username.charAt(0).toUpperCase();
  $("accountSettingsName").textContent = username;
  updatePageHeading();

}

function setPage(page) {
  const registered = pluginNavigation.get(page);
  if (!Object.hasOwn(pageMeta, page) && !registered) return;
  hostDashboard.setActive(page === "overview");
  overviewWidgets.setActive(page === "overview");
  currentPage = page;
  document.querySelectorAll(".nav").forEach(n => { n.classList.toggle("active", n.dataset.page === page); n.setAttribute("aria-current", n.dataset.page === page ? "page" : "false"); });
  document.querySelectorAll(".page").forEach(p => p.classList.remove("active"));
  $(`${page}Page`).classList.add("active");
  const headerActions = $("pluginHeaderActions");
  headerActions.replaceChildren();
  headerActions.classList.add("hidden");
  if (registered?.headerActions) {
    registered.headerActions(headerActions);
    headerActions.classList.toggle("hidden", headerActions.childElementCount === 0);
  }
  updatePageHeading();
  if (page === "settings") void ensureHostSummary();
	if (page === "settings") renderPluginSettings();
	if (registered) { registered.page.replaceChildren(); registered.render(registered.page); }
  window.scrollTo(0, 0);
  $("pageTitle").focus({preventScroll:true});
  refresh();
}

document.querySelectorAll(".nav").forEach(n => n.addEventListener("click", () => setPage(n.dataset.page)));
$("restartApplicationButton").addEventListener("click", requestRunPilotRestart);
$("generalRestartButton").addEventListener("click", requestRunPilotRestart);
$("managePluginsButton").addEventListener("click", () => selectSettingsTab("pluginSettingsTab", true));
$("accountSettingsButton").addEventListener("click", () => {
  document.querySelector(".account-menu").open = false;
  $("accountSettingsDialog").showModal();
  $("currentPassword").focus();
});
$("accountSettingsDialog").addEventListener("close", () => {
  $("passwordChangeForm").reset();
  $("passwordChangeError").textContent = "";
  $("passwordChangeError").classList.add("hidden");
  document.querySelector(".account-menu > summary").focus();
});

document.querySelectorAll("[data-software-tab]").forEach(button => button.addEventListener("click", () => { softwareTab = button.dataset.softwareTab; renderSoftware(); loadSoftwareView(); }));
$("softwareSearchButton").addEventListener("click", softwareSearch);
$("softwareUpgradeAll").addEventListener("click", softwareUpgradeAll);
$("softwareAddBucket").addEventListener("click", softwareAddBucket);
$("softwareProviderSelect").addEventListener("change", event => changeSoftwareProvider(event.target.value));
$("softwareSearchInput").addEventListener("keydown", event => { if (event.key === "Enter") { event.preventDefault(); softwareSearch(); } });
$("softwareBucketSource").addEventListener("keydown", event => { if (event.key === "Enter") { event.preventDefault(); softwareAddBucket(); } });
document.querySelectorAll("[data-dismiss]").forEach(button => button.addEventListener("click", () => $(button.dataset.dismiss)?.close()));
$("storageCreate").addEventListener("click",async()=>{if(!storageLocation)return;const name=prompt("Folder name:");if(!name)return;try{await api(`api/v1/storage/${storageLocation}/directories`,{method:"POST",body:JSON.stringify({parentPath:storagePath,name})});browseStorage(storageLocation,storagePath)}catch(e){toast(e.message)}});
$("storageNewFile").addEventListener("click",openNewTextFile);
$("storageUpload").addEventListener("change",async e=>{if(!storageLocation||!e.target.files.length)return;const form=new FormData();form.append("path",storagePath);for(const f of e.target.files)form.append("files",f);try{await api(`api/v1/storage/${storageLocation}/upload`,{method:"POST",body:form});browseStorage(storageLocation,storagePath)}catch(err){toast(err.message)}finally{e.target.value=""}});
$("storageProvider").addEventListener("change",e=>browseStorage(e.target.value,""));
$("storageShowHidden").addEventListener("change",e=>{storageShowHidden=e.target.checked;browseStorage(storageLocation,storagePath)});
$("saveTextEditor").addEventListener("click",saveTextEditor);$("closeTextEditor").addEventListener("click",()=>$("textEditorDialog").close());$("cancelTextEditor").addEventListener("click",()=>$("textEditorDialog").close());
$("textEditorContent").addEventListener("input",updateTextHighlight);
$("textEditorContent").addEventListener("scroll",e=>{ $("textHighlight").scrollTop=e.target.scrollTop; $("textHighlight").scrollLeft=e.target.scrollLeft; });

$("loginDialog").addEventListener("cancel", event => event.preventDefault());
$("loginForm").addEventListener("submit", async e => {
  e.preventDefault();
  try {
    const response = await fetch("api/v1/auth/login", {method:"POST", credentials:"same-origin", headers:{"Content-Type":"application/json"}, body:JSON.stringify({username:$("loginUsername").value,password:$("loginPassword").value})});
    if (!response.ok) throw new Error("The username or password was rejected.");
    authenticated = true;
    $("loginError").classList.add("hidden");
    $("loginDialog").close();
    setConnected(true);
    await loadSystemInfo();
    await loadDataRootSettings();
    await connectApplicationSocket();
    await refresh();
    hostDashboard.setActive(currentPage === "overview");
    hostDashboard.start();
    overviewWidgets.setActive(currentPage === "overview");
    startAutoRefresh();
    startConnectionMonitor();
  } catch (error) {
    $("loginError").textContent = error.message || "The username or password was rejected.";
    $("loginError").classList.remove("hidden");
  }
});

$("passwordChangeForm").addEventListener("submit",async e=>{e.preventDefault();const message=$("passwordChangeError");message.classList.add("hidden");try{await api("api/v1/auth/password",{method:"POST",body:JSON.stringify({current:$("currentPassword").value,next:$("nextPassword").value,confirm:$("confirmPassword").value})});authenticated=false;hostDashboard.stop();location.reload();}catch(error){message.textContent=error.message;message.classList.remove("hidden");}});
$("logoutButton").addEventListener("click",async()=>{hostDashboard.stop();try{await api("api/v1/auth/logout",{method:"POST"});}finally{authenticated=false;location.reload();}});
$("bearerAuthEnabled").addEventListener("change",async()=>{try{const result=await api("api/v1/admin/bearer-auth",{method:"PUT",body:JSON.stringify({enabled:$("bearerAuthEnabled").checked})});$("bearerAuthEnabled").checked=result.enabled;}catch(error){$("bearerAuthEnabled").checked=!$("bearerAuthEnabled").checked;toast(error.message);}});

$("websocketPayloadModeForm").addEventListener("submit",async event=>{
  event.preventDefault();
  const mode=$("websocketPayloadMode").value, status=$("websocketPayloadModeStatus"), button=$("websocketPayloadModeSave");
  button.disabled=true; status.textContent="Saving WebSocket encryption setting…";
  try {
    const result=await api("api/v1/admin/websocket-payload-mode",{method:"PUT",body:JSON.stringify({mode})});
    status.textContent="Saved. Reconnecting to apply the new mode…";
    systemInfo.websocketPayloadModeConfigured=result.mode;
    setTimeout(()=>location.reload(),500);
  } catch(error) {
    status.textContent=`Could not save setting: ${error.message}`;
    button.disabled=false;
  }
});

async function loadDataRootSettings() {
  try {
    const root = await api("api/v1/admin/data-root");
    $("dataRootDestination").value = root.dataDir;
    $("dataRootDestination").disabled = !root.changeAllowed;
    $("dataRootSave").disabled = !root.changeAllowed;
    $("dataRootStatus").textContent = root.changeAllowed
      ? ""
      : "Directory changes are unavailable here; check the service startup configuration and restart support.";
    $("dataRootStatus").classList.toggle("hidden", root.changeAllowed);
  } catch (error) {
    $("dataRootStatus").textContent = `Could not load the data directory: ${error.message}`;
    $("dataRootStatus").classList.remove("hidden");
  }
}

$("dataRootChangeForm").addEventListener("submit",async e=>{e.preventDefault();const error=$("dataRootError");if(!await confirmAction({title:"Change directory and restart?",message:"No data will be copied or moved. Prepare your data in the selected directory; an empty directory starts a fresh installation.",confirmLabel:"Change and restart",danger:true}))return;error.classList.add("hidden");try{await api("api/v1/admin/data-root",{method:"POST",body:JSON.stringify({destination:$("dataRootDestination").value})});error.textContent="RunPilot is restarting. Sign in again when it is ready.";error.classList.remove("hidden");}catch(reason){error.textContent=reason.message;error.classList.remove("hidden");}});

(async function init() {
  initializeSidebar();
  initializeTheme();
  updatePageHeading();
  try { const setup = await fetch("api/v1/setup", {credentials:"same-origin"}).then(response=>response.json()); if (setup.required) { location.replace("setup"); return; } } catch {}
  try { await api("api/v1/system"); authenticated = true; } catch {
    $("loginDialog").showModal();
    return;
  }
  try { await loadSystemInfo(); } catch (e) { if (e.message !== "Unauthorized") toast(e.message); }
  await loadDataRootSettings();
  setConnected(false);
  startConnectionMonitor();
  try { await connectApplicationSocket(); } catch (e) { toast(e.message); }
  await refresh();
  hostDashboard.setActive(currentPage === "overview");
  hostDashboard.start();
  overviewWidgets.setActive(currentPage === "overview");
  startAutoRefresh();
})();

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) { refresh(); probeConnection(); void hostDashboard.refresh(); void overviewWidgets.refresh(); }
});
