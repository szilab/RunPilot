/* The host prepends this owner's immutable, listener-scoped runtime configuration. */
const runtime = self.RUNPILOT_BROWSER_RUNTIME;
importScripts(runtime.basePath + "/secure-websocket.js", runtime.assets + "tunnel.js");
const sessions = new Map(), lineages = new Map(), handoffs = new Map(), navigationLeases = new Map(), guards = new Map();
const navigationPrefix = runtime.scope + "__runpilot__/browser/" + runtime.runtimeId + "/navigate/";
const cachePrefix = "runpilot.browser.routes:" + encodeURIComponent(runtime.scope) + ":", cacheName = cachePrefix + runtime.runtimeId;
const id = value => typeof value === "string" && /^[A-Za-z0-9_-]{32}$/.test(value);
function randomID() { return btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(24)))).replace(/\+/g, "-").replace(/\//g, "_"); }
function bindNavigation(client, tunnel) { sessions.set(client, tunnel); navigationLeases.set(client, Date.now() + 60000); }
function localError(event, message, status) {
  if (event.request.mode !== "navigate") return new Response(message, {status, headers:{"Content-Type":"text/plain; charset=utf-8", "Cache-Control":"no-store"}});
  const safe = String(message).slice(0, 1024).replace(/[&<>"']/g, character => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[character]));
  // A local navigation failure is a package-owned screen. Upstream responses
  // and subresource errors retain their original payloads and HTTP semantics.
  const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Web App unavailable · RunPilot</title><style>
    :root{color-scheme:light;--bg:#eaf0f8;--card:#f9fbff;--text:#17243b;--muted:#5d6d85;--border:#d6e0ee}
    @media(prefers-color-scheme:dark){:root:not([data-theme="light"]){color-scheme:dark;--bg:#101827;--card:#182338;--text:#edf3ff;--muted:#9cacc5;--border:#2d3c54}}
    :root[data-theme="dark"]{color-scheme:dark;--bg:#101827;--card:#182338;--text:#edf3ff;--muted:#9cacc5;--border:#2d3c54}
    *{box-sizing:border-box}body{margin:0;min-height:100dvh;display:grid;place-items:center;padding:24px;font:15px/1.6 system-ui,sans-serif;background:var(--bg);color:var(--text)}
    main{width:min(460px,100%);padding:32px;border:1px solid var(--border);border-radius:20px;background:var(--card);box-shadow:0 16px 60px rgba(8,16,32,.12)}small{color:var(--muted);font-size:12px;letter-spacing:.08em}h1{margin:12px 0;font-size:24px;line-height:1.3;letter-spacing:-.03em}p{color:var(--muted);margin:12px 0 0;overflow-wrap:anywhere}
    </style><script>try{const theme=localStorage.getItem("runpilot.theme");if(theme==="light"||theme==="dark")document.documentElement.dataset.theme=theme}catch{}</script></head><body><main><small>RUNPILOT · WEB APPS</small><h1>Web App unavailable</h1><p role="alert">${safe}</p><p>Check that the upstream service is running, then open this app again from RunPilot.</p></main></body></html>`;
  return new Response(html, {status, headers:{"Content-Type":"text/html; charset=utf-8", "Cache-Control":"no-store", "Content-Security-Policy":"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'"}});
}
function validPublication(p) {
  if (!p || p.runtimeId !== runtime.runtimeId || p.owner !== runtime.owner || !id(p.publicationId) || p.scope !== runtime.scope || p.workerURL !== runtime.workerURL || p.assets !== runtime.assets) return false;
  const prefix = new URL(p.publicPrefix, location.origin);
  const bootstrap = new URL(p.bootstrapHTML, location.origin);
  return prefix.origin === location.origin && prefix.pathname === p.publicPrefix && prefix.pathname.startsWith(runtime.scope) && prefix.pathname !== runtime.scope && !prefix.search && !prefix.hash && bootstrap.origin === location.origin && bootstrap.pathname === p.bootstrapHTML && p.bootstrapHTML.startsWith(runtime.assets) && p.bootstrapHTML.endsWith(".html") && !p.bootstrapHTML.slice(runtime.assets.length).includes("%");
}
// Cache only public mount metadata and package HTML, never sessions, credentials,
// application responses, or cookie state. It lets a restarted worker fail closed
// at a previously virtual path even though all live gateway bindings are gone.
async function refreshPublications() {
  const response = await fetch(runtime.publicationsURL, { cache: "no-store" });
  if (!response.ok) throw new Error("Browser runtime expired. Open this Web App again from RunPilot.");
  const publications = await response.json();
  if (!Array.isArray(publications) || publications.length > 256 || !publications.every(validPublication)) throw new Error("Invalid browser publication metadata");
  for (const p of publications) { guards.delete(p.publicationId); guards.set(p.publicationId, p); }
  // Active mounts plus a bounded history of retired paths used only as error guards.
  while (guards.size > 512) guards.delete(guards.keys().next().value);
  const cache = await caches.open(cacheName);
  await cache.put(runtime.publicationsURL, new Response(JSON.stringify([...guards.values()]), { headers: { "Content-Type": "application/json" } }));
  return publications;
}
const routingReady = (async () => {
  const cache = await caches.open(cacheName), saved = await cache.match(runtime.publicationsURL);
  if (saved) try { const items = await saved.json(); if (Array.isArray(items) && items.length <= 512) for (const p of items) if (validPublication(p)) guards.set(p.publicationId, p); } catch { /* Replace corrupt public metadata from the host; it cannot authorize a session. */ }
  try { await refreshPublications(); } catch { /* Cached public guards still deny expired navigation locally. */ }
})();
async function localBootstrap(config) {
  const cache = await caches.open(cacheName);
  let template = await cache.match(config.bootstrapHTML);
  if (!template) { template = await fetch(config.bootstrapHTML, { cache: "no-store" }); if (!template.ok) throw new Error("Browser bootstrap unavailable"); await cache.put(config.bootstrapHTML, template.clone()); }
  const html = (await template.text()).replaceAll("__RUNPILOT_PUBLICATION__", JSON.stringify(config).replace(/</g, "\\u003c"));
  return new Response(html, { headers: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store", "Content-Security-Policy": "default-src 'none'; script-src 'self' 'unsafe-inline'; connect-src 'self'; worker-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'" } });
}
self.addEventListener("install", event => event.waitUntil(self.skipWaiting()));
self.addEventListener("activate", event => event.waitUntil((async () => {
  await routingReady; await self.clients.claim();
  for (const key of await caches.keys()) if (key.startsWith(cachePrefix) && key !== cacheName) await caches.delete(key);
})()));
self.addEventListener("message", event => {
  if (event.data?.type === "websocket.open") {
    event.waitUntil((async () => {
      const client = event.source, port = event.ports[0];
      let tunnel, socketId;
      try {
        await routingReady;
        tunnel = sessions.get(client.id);
        if (!tunnel || tunnel.dead || !port || typeof event.data.url !== "string" || !Array.isArray(event.data.protocols) || event.data.protocols.length > 16) throw new Error("WebSocket publication is not bound to this client");
        const url = new URL(event.data.url, client.url), prefix = tunnel.config.publicPrefix;
        if ((url.protocol !== "ws:" && url.protocol !== "wss:") || url.host !== location.host || url.username || url.password || !(url.pathname === prefix || url.pathname.startsWith(prefix + "/"))) throw new Error("WebSocket URL is outside this publication");
        socketId = await tunnel.openSocket(url.pathname + url.search, event.data.protocols, message => port.postMessage(message));
        port.postMessage({ type: "ready" });
        port.onmessage = async ({ data }) => {
          try {
            if (data?.type === "send") { await tunnel.sendSocket(socketId, data.data, !!data.binary); port.postMessage({ type: "drain", amount: data.amount }); }
            else if (data?.type === "close") { await tunnel.closeSocket(socketId, data.code, data.reason); port.close(); }
          } catch (error) { port.postMessage({ type: "error", message: error.message }); }
        };
        port.start();
      } catch (error) { port?.postMessage({ type: "error", message: error.message || "WebSocket gateway failed" }); port?.close(); }
    })());
    return;
  }
  if (!["gateway.initialize", "gateway.resume"].includes(event.data?.type)) return;
  event.waitUntil((async () => {
    const client = event.source, port = event.ports[0];
    try {
      await routingReady;
      const source = new URL(client.url), target = new URL(event.data.url);
      if (source.origin !== location.origin || target.origin !== location.origin || event.data.runtime !== runtime.runtimeId) throw new Error("Publication expired");
      let handle = event.data.handle, tunnel;
      if (event.data.type === "gateway.initialize") {
        // Only the trusted root bootstrap may redeem a new restricted ticket.
        if (source.pathname !== runtime.scope || !id(event.data.ticket) || !id(event.data.streamId)) throw new Error("Invalid publication bootstrap");
        const configs = await refreshPublications(), config = configs.find(p => p.publicationId === event.data.publication);
        if (!config || target.pathname !== config.publicPrefix + "/" || target.search || target.hash) throw new Error("Publication expired");
        if (sessions.has(client.id)) throw new Error("Browser client is already bound");
        tunnel = new HTTPGatewayTunnel(config, event.data.ticket, event.data.streamId);
        await tunnel.ready;
        handle = randomID(); lineages.set(handle, tunnel);
      } else {
        tunnel = lineages.get(handle);
        if (!tunnel || tunnel.dead || tunnel.config.publicationId !== event.data.publication) throw new Error("This gateway has expired. Open the Web App again from RunPilot.");
        if (sessions.has(client.id) && sessions.get(client.id) !== tunnel) throw new Error("Foreign gateway binding");
      }
      const prefix = tunnel.config.publicPrefix;
      if (!(target.pathname === prefix || target.pathname.startsWith(prefix + "/"))) throw new Error("Navigation outside gateway publication");
      if (!(source.pathname === runtime.scope || source.pathname === prefix || source.pathname.startsWith(prefix + "/"))) throw new Error("Foreign publication client");
      sessions.set(client.id, tunnel);
      for (const [key, handoff] of handoffs) if (handoff.tunnel === tunnel) handoffs.delete(key);
      const key = randomID(); handoffs.set(key, { tunnel, url: target.href, expires: Date.now() + 60000 });
      port.postMessage({ ok: true, handle, url: navigationPrefix + key });
    } catch (error) { port?.postMessage({ ok: false, error: error.message }); }
    finally { port?.close(); }
  })());
});
self.addEventListener("fetch", event => {
  const url = new URL(event.request.url);
  if (url.origin !== location.origin || !url.pathname.startsWith(runtime.scope)) return;
  if (url.pathname.startsWith(navigationPrefix)) {
    event.respondWith((async () => {
      const key = url.pathname.slice(navigationPrefix.length), handoff = handoffs.get(key); handoffs.delete(key);
      if (event.request.mode !== "navigate" || !handoff || handoff.expires <= Date.now() || handoff.tunnel.dead || !event.resultingClientId) return localError(event, "Navigation expired. Open this Web App again from RunPilot.", 410);
      bindNavigation(event.resultingClientId, handoff.tunnel);
      return new Response(null, { status: 302, headers: { Location: handoff.url, "Cache-Control": "no-store" } });
    })()); return;
  }
  const tunnel = sessions.get(event.clientId) || sessions.get(event.resultingClientId) || sessions.get(event.replacesClientId);
  if (tunnel) {
    event.respondWith((async () => {
      if (tunnel.dead) return localError(event, "Gateway expired. Open this Web App again from RunPilot.", 502);
      const prefix = tunnel.config.publicPrefix;
      if (url.pathname === runtime.assets + "websocket-shim.js") return fetch(event.request);
      if (!(url.pathname === prefix || url.pathname.startsWith(prefix + "/"))) return localError(event, "Request outside gateway publication", 403);
      if (event.resultingClientId) bindNavigation(event.resultingClientId, tunnel);
      try {
        const response = await tunnel.request(event.request);
        if (event.request.mode === "navigate" && /text\/html\b/i.test(response.headers.get("content-type") || "")) return await injectWebSocketShim(response);
        return response;
      }
      catch (error) { return localError(event, error.message || "Encrypted gateway failed", 502); }
    })()); return;
  }
  // Normal management clients and unbound subresources use ordinary networking.
  // Firefox omits initiating IDs on document navigation. A virtual path can only
  // select inert package resume HTML; it never selects or authorizes a tunnel.
  if (event.request.mode === "navigate") event.respondWith((async () => {
    await routingReady;
    const config = [...guards.values()].reverse().find(p => url.pathname === p.publicPrefix || url.pathname.startsWith(p.publicPrefix + "/"));
    if (!config) return fetch(event.request);
    try { return await localBootstrap(config); }
    catch { return localError(event, "Gateway expired. Open this Web App again from RunPilot.", 503); }
  })());
});

async function injectWebSocketShim(response) {
  if (!response.body || [204, 205, 304].includes(response.status)) return response;
  const reader = response.body.getReader(), chunks = [], limit = 65536;
  let size = 0, injectionPoint = -1, buffered = new Uint8Array();
  const encoder = new TextEncoder();
  while (size <= limit) {
    const { value, done } = await reader.read();
    if (done) break;
    chunks.push(value); size += value.length;
    const merged = new Uint8Array(buffered.length + value.length); merged.set(buffered); merged.set(value, buffered.length); buffered = merged;
    // Latin-1 preserves byte offsets while locating ASCII markup tokens even
    // when an upstream document uses a legacy character encoding.
    const text = new TextDecoder("latin1").decode(buffered);
    const head = /<head(?:\s[^>]*)?>/i.exec(text);
    if (head) {
      const offset = head.index + head[0].length;
      if (offset <= limit) injectionPoint = offset;
      break;
    }
    if (/<body(?:\s|>)/i.test(text) || size > limit) break;
  }
  const prefix = new Uint8Array(Math.min(injectionPoint < 0 ? size : injectionPoint, size));
  let at = 0; for (const chunk of chunks) { const take = Math.min(chunk.length, prefix.length - at); if (take > 0) { prefix.set(chunk.subarray(0, take), at); at += take; } }
  let remainder = [];
  if (injectionPoint >= 0) {
    let skipped = injectionPoint;
    for (const chunk of chunks) { if (skipped >= chunk.length) { skipped -= chunk.length; continue; } remainder.push(chunk.subarray(skipped)); skipped = 0; }
  } else remainder = [];
  const script = injectionPoint >= 0 ? encoder.encode(`<script src="${runtime.assets}websocket-shim.js"></script>`) : new Uint8Array();
  const body = new ReadableStream({ async pull(controller) {
    if (!this.prefixSent) { this.prefixSent = true; if (prefix.length) controller.enqueue(prefix); if (script.length) controller.enqueue(script); return; }
    if (remainder.length) { const chunk = remainder.shift(); if (chunk.length) { controller.enqueue(chunk); return; } }
    const { value, done } = await reader.read(); if (done) controller.close(); else controller.enqueue(value);
  }, cancel(reason) { return reader.cancel(reason); } });
  const headers = new Headers(response.headers);
  if (injectionPoint >= 0) {
    for (const name of ["content-length", "etag", "last-modified", "content-md5", "digest", "content-digest", "repr-digest"]) headers.delete(name);
  }
  return new Response(body, { status: response.status, statusText: response.statusText, headers });
}
setInterval(async () => {
  for (const [key, handoff] of handoffs) if (handoff.expires <= Date.now() || handoff.tunnel.dead) handoffs.delete(key);
  const clients = await self.clients.matchAll(), alive = new Set(clients.map(client => client.id));
  for (const [client] of sessions) {
    if (alive.has(client)) navigationLeases.delete(client);
    else if (!(navigationLeases.get(client) > Date.now())) { sessions.delete(client); navigationLeases.delete(client); }
  }
  const active = new Set([...sessions.values(), ...[...handoffs.values()].map(handoff => handoff.tunnel)]);
  for (const [handle, tunnel] of lineages) if (tunnel.dead || !active.has(tunnel)) { lineages.delete(handle); tunnel.close(); }
}, 30000);
