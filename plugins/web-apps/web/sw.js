/* The host prepends the immutable publication generation and its narrow scope. */
const publication = self.RUNPILOT_PUBLICATION;
importScripts(publication.basePath + "/secure-websocket.js", publication.assets + "tunnel.js");
const sessions = new Map();
self.addEventListener("install", event => event.waitUntil(self.skipWaiting()));
self.addEventListener("activate", event => event.waitUntil(self.clients.claim()));
self.addEventListener("message", event => {
  if (event.data?.type !== "gateway.initialize") return;
  event.waitUntil((async () => {
    const client = event.source, port = event.ports[0];
    try {
      const url = new URL(client.url);
      if (url.origin !== location.origin || !url.pathname.startsWith(publication.publicPrefix + "/") || event.data.publication !== publication.publicationId) throw new Error("Publication expired");
      // Credentials are never persisted in browser storage. Each client lineage owns its session.
      const prior = sessions.get(client.id); prior?.close();
      const tunnel = new HTTPGatewayTunnel(publication, event.data.ticket, event.data.streamId);
      await tunnel.ready; sessions.set(client.id, tunnel); port.postMessage({ ok: true });
    } catch (error) { port?.postMessage({ ok: false, error: error.message }); }
    finally { port?.close(); }
  })());
});
self.addEventListener("fetch", event => {
  const url = new URL(event.request.url), prefix = publication.publicPrefix + "/";
  if (url.origin !== location.origin || (url.pathname !== publication.publicPrefix && !url.pathname.startsWith(prefix))) return;
  // Package initialization remains ordinary HTTPS; application resources never fall through.
  if (url.pathname.startsWith(prefix + "__runpilot__/")) return;
  event.respondWith((async () => {
    const tunnel = sessions.get(event.clientId);
    if (!tunnel) {
      if (event.request.mode === "navigate") return fetch(event.request); // Host serves package bootstrap only.
      return new Response("Open this Web App again from RunPilot.", { status: 503 });
    }
    if (event.resultingClientId) sessions.set(event.resultingClientId, tunnel);
    try { return await tunnel.request(event.request); }
    catch (error) { return new Response(error.message || "Encrypted gateway failed", { status: 502, headers: { "Content-Type": "text/plain" } }); }
  })());
});
// Close abandoned lineages. A worker restart loses all credentials and fails closed.
setInterval(async () => {
  const clients = await self.clients.matchAll(); const alive = new Set(clients.map(client => client.id));
  const removed = new Set(); for (const [id, tunnel] of sessions) if (!alive.has(id)) { sessions.delete(id); removed.add(tunnel); }
  for (const tunnel of removed) if (![...sessions.values()].includes(tunnel)) tunnel.close();
}, 30000);
