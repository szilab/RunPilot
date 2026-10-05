/* The host prepends the immutable publication generation and its narrow scope. */
const publication = self.RUNPILOT_PUBLICATION;
importScripts(publication.basePath + "/secure-websocket.js", publication.assets + "tunnel.js");
const sessions = new Map(), lineages = new Map(), handoffs = new Map(), navigationLeases = new Map();
const navigationPrefix = publication.publicPrefix + "/__runpilot__/navigate/";
function bindNavigation(id, tunnel) {
  sessions.set(id, tunnel); navigationLeases.set(id, Date.now() + 60000);
}
self.addEventListener("install", event => event.waitUntil(self.skipWaiting()));
self.addEventListener("activate", event => event.waitUntil(self.clients.claim()));
self.addEventListener("message", event => {
  if (!["gateway.initialize", "gateway.resume"].includes(event.data?.type)) return;
  event.waitUntil((async () => {
    const client = event.source, port = event.ports[0];
    try {
      const source = new URL(client.url), target = new URL(event.data.url);
      const inside = url => url.origin === location.origin && url.pathname.startsWith(publication.publicPrefix + "/") && !url.pathname.startsWith(navigationPrefix);
      if (!inside(source) || !inside(target) || event.data.publication !== publication.publicationId) throw new Error("Publication expired");
      // Credentials and lineage handles live only in this worker and the tab's window.name.
      // Firefox supplies neither clientId nor replacesClientId for document navigation.
      let handle = event.data.handle, tunnel;
      if (event.data.type === "gateway.initialize") {
        sessions.get(client.id)?.close();
        tunnel = new HTTPGatewayTunnel(publication, event.data.ticket, event.data.streamId);
        await tunnel.ready;
        handle = crypto.randomUUID(); lineages.set(handle, tunnel);
      } else {
        tunnel = lineages.get(handle);
        if (!tunnel || tunnel.dead) throw new Error("This gateway has expired. Open the Web App again from RunPilot.");
      }
      sessions.set(client.id, tunnel);
      // One pending, short-lived handoff per lineage. No capability reaches the upstream.
      for (const [key, handoff] of handoffs) if (handoff.tunnel === tunnel) handoffs.delete(key);
      const key = crypto.randomUUID(); handoffs.set(key, { tunnel, url: target.href, expires: Date.now() + 60000 });
      port.postMessage({ ok: true, handle, url: navigationPrefix + key });
    } catch (error) { port?.postMessage({ ok: false, error: error.message }); }
    finally { port?.close(); }
  })());
});
self.addEventListener("fetch", event => {
  const url = new URL(event.request.url), prefix = publication.publicPrefix + "/";
  if (url.origin !== location.origin || (url.pathname !== publication.publicPrefix && !url.pathname.startsWith(prefix))) return;
  if (url.pathname.startsWith(navigationPrefix)) {
    event.respondWith((async () => {
      const key = url.pathname.slice(navigationPrefix.length), handoff = handoffs.get(key);
      handoffs.delete(key);
      if (event.request.mode !== "navigate" || !handoff || handoff.expires <= Date.now() || handoff.tunnel.dead || !event.resultingClientId) return new Response("Navigation expired. Open this Web App again from RunPilot.", { status: 410 });
      // The reserved client ID remains stable across the subsequent redirect chain.
      bindNavigation(event.resultingClientId, handoff.tunnel);
      return new Response(null, { status: 302, headers: { Location: handoff.url, "Cache-Control": "no-store" } });
    })()); return;
  }
  // Package initialization remains ordinary HTTPS; application resources never fall through.
  if (url.pathname.startsWith(prefix + "__runpilot__/")) return;
  event.respondWith((async () => {
    const tunnel = sessions.get(event.clientId) || sessions.get(event.resultingClientId) || sessions.get(event.replacesClientId);
    if (!tunnel) {
      if (event.request.mode === "navigate") return fetch(event.request); // Host serves package bootstrap only.
      return new Response("Open this Web App again from RunPilot.", { status: 503 });
    }
    if (event.resultingClientId) bindNavigation(event.resultingClientId, tunnel);
    try { return await tunnel.request(event.request); }
    catch (error) { return new Response(error.message || "Encrypted gateway failed", { status: 502, headers: { "Content-Type": "text/plain" } }); }
  })());
});
// Close abandoned lineages. A worker restart loses all credentials and fails closed.
setInterval(async () => {
  for (const [key, handoff] of handoffs) if (handoff.expires <= Date.now() || handoff.tunnel.dead) handoffs.delete(key);
  const clients = await self.clients.matchAll(); const alive = new Set(clients.map(client => client.id));
  // Reserved navigation clients may not appear in matchAll until their response
  // starts rendering. Give them a bounded grace period while upstream is slow.
  for (const [id] of sessions) {
    if (alive.has(id)) navigationLeases.delete(id);
    else if (!(navigationLeases.get(id) > Date.now())) { sessions.delete(id); navigationLeases.delete(id); }
  }
  const active = new Set([...sessions.values(), ...[...handoffs.values()].map(handoff => handoff.tunnel)]);
  for (const [handle, tunnel] of lineages) if (tunnel.dead || !active.has(tunnel)) { lineages.delete(handle); tunnel.close(); }
}, 30000);
