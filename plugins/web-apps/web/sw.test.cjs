const assert = require("node:assert/strict"), vm = require("node:vm"), fs = require("node:fs");
const { webcrypto } = require("node:crypto");
const runtimeID = "r".repeat(32), pubA = "a".repeat(32), pubB = "b".repeat(32);
const runtime = { owner: "web.apps", basePath: "/p", scope: "/p/", runtimeId: runtimeID, assets: "/p/plugins/web.apps/web/", workerURL: `/p/__runpilot__/browser/${runtimeID}/sw.js`, publicationsURL: `/p/__runpilot__/browser/${runtimeID}/publications.json` };
const configs = [ { ...runtime, publicPrefix: "/p/app", publicationId: pubA, bootstrapHTML: runtime.assets + "bootstrap.html" }, { ...runtime, publicPrefix: "/p/router", publicationId: pubB, bootstrapHTML: runtime.assets + "bootstrap.html" } ];
const stores = new Map();
const caches = { async open(name) { if (!stores.has(name)) stores.set(name, new Map()); const entries = stores.get(name); return { match: async key => entries.get(key)?.clone(), put: async (key, value) => entries.set(key, value.clone()) }; }, keys: async () => [...stores.keys()], delete: async name => stores.delete(name) };
let online = true;
function worker() {
  const listeners = new Map(), tunnels = [], network = [];
  let sweep, now = 1000, alive = [];
  class Tunnel {
    constructor(config, ticket) { this.config = config; this.ready = Promise.resolve(); this.ticket = ticket; this.dead = false; tunnels.push(this); }
    request(request) { return Promise.resolve(new Response(this.ticket.slice(0, 1) + ":" + new URL(request.url).pathname)); }
    close() { this.dead = true; }
  }
  const context = { self: { RUNPILOT_BROWSER_RUNTIME: runtime, skipWaiting: async () => {}, addEventListener(type, handler) { listeners.set(type, handler); }, clients: { claim: async () => {}, matchAll: async () => alive.map(id => ({ id })) } }, importScripts() {}, HTTPGatewayTunnel: Tunnel, URL, Response, crypto: webcrypto, Uint8Array, btoa, caches, location: { origin: "https://host" }, Date: { now: () => now }, setInterval(callback) { sweep = callback; }, fetch: async input => {
    const path = typeof input === "string" ? input : new URL(input.url).pathname; network.push(path);
    if (path === runtime.publicationsURL) return online ? new Response(JSON.stringify(configs)) : new Response("expired", { status: 410 });
    if (path === runtime.assets + "bootstrap.html") return new Response('<h1>package bootstrap</h1><script>const config=__RUNPILOT_PUBLICATION__;</script>');
    return new Response("ordinary network");
  } };
  vm.runInNewContext(fs.readFileSync("plugins/web-apps/web/sw.js", "utf8"), context);
  async function message(client, data, url = "https://host/p/") {
    let done, reply;
    listeners.get("message")({ source: { id: client, url }, data: { runtime: runtimeID, publication: pubA, url: "https://host/p/app/", ...data }, ports: [{ postMessage(value) { reply = value; }, close() {} }], waitUntil(promise) { done = promise; } });
    await done; return reply;
  }
  async function fetch(path, { clientId = "", resultingClientId = "", replacesClientId = "", mode = "navigate" } = {}) {
    let response;
    listeners.get("fetch")({ request: { url: "https://host" + path, mode }, clientId, resultingClientId, replacesClientId, respondWith(promise) { response = promise; } });
    return response;
  }
  return { message, fetch, tunnels, network, sweep: () => sweep(), alive: value => alive = value, advance: value => now += value };
}
(async () => {
  const w = worker();
  const first = await w.message("first-bootstrap", { type: "gateway.initialize", ticket: "f".repeat(32), streamId: "s".repeat(32) });
  assert.equal(first.ok, true);
  const handoff = await w.fetch(first.url, { resultingClientId: "first-app" });
  assert.equal(handoff.status, 302); assert.equal(handoff.headers.get("location"), "https://host/p/app/");
  assert.equal(handoff.headers.get("cache-control"), "no-store");
  await w.sweep(); assert.equal(w.tunnels[0].dead, false);
  assert.equal((await w.fetch(first.url, { resultingClientId: "replay" })).status, 410);
  assert.equal(await (await w.fetch("/p/app/web/", { resultingClientId: "first-app" })).text(), "f:/p/app/web/");
  assert.equal(await (await w.fetch("/p/app/image", { clientId: "first-app", mode: "cors" })).text(), "f:/p/app/image");
  assert.equal(await (await w.fetch("/p/app/login", { replacesClientId: "first-app", resultingClientId: "chrome-next" })).text(), "f:/p/app/login");
  assert.equal((await w.fetch("/p/router/", { clientId: "first-app" })).status, 403, "client escaped its snapshotted publication");
  const before = w.network.length;
  assert.equal(await w.fetch("/p/api/v1/system", { clientId: "management", mode: "cors" }), undefined);
  assert.equal(await w.fetch("/p/app/image", { clientId: "unbound", mode: "cors" }), undefined);
  assert.equal(await (await w.fetch("/p/", { clientId: "management" })).text(), "ordinary network");
  assert.deepEqual(w.network.slice(before), ["/p/"]);
  // With no initiating IDs, paths select only inert resume HTML, never a tunnel.
  const bridge = await w.fetch("/p/app/web/", { resultingClientId: "firefox-reload" });
  assert.match(await bridge.text(), /package bootstrap/);
  assert.equal(w.network.some(path => path.startsWith("/p/app/")), false, "target document reached network");
  const second = await w.message("second-bootstrap", { type: "gateway.initialize", ticket: "g".repeat(32), streamId: "u".repeat(32), publication: pubB, url: "https://host/p/router/" });
  assert.equal(second.ok, true); await w.fetch(second.url, { resultingClientId: "second-app" });
  assert.equal((await w.message("second-bootstrap", { type: "gateway.resume", handle: first.handle })).ok, false, "bound root client changed gateways");
  assert.equal(await (await w.fetch("/p/router/cookie", { clientId: "second-app", mode: "cors" })).text(), "g:/p/router/cookie");
  const resumed = await w.message("firefox-reload", { type: "gateway.resume", handle: first.handle, url: "https://host/p/app/web/?query=1#page" }, "https://host/p/app/web/");
  assert.equal(resumed.ok, true); assert.equal(w.tunnels.length, 2);
  const reload = await w.fetch(resumed.url, { resultingClientId: "first-reloaded" });
  assert.equal(reload.headers.get("location"), "https://host/p/app/web/?query=1#page");
  assert.equal((await w.message("foreign", { type: "gateway.resume", handle: "forged" })).ok, false);
  assert.equal((await w.message("foreign", { type: "gateway.resume", handle: first.handle, publication: pubB })).ok, false);
  assert.equal((await w.message("foreign", { type: "gateway.resume", handle: first.handle }, "https://host/p/router/")).ok, false);
  assert.equal((await w.message("foreign", { type: "gateway.resume", handle: first.handle, url: "https://evil/app/" })).ok, false);
  assert.equal((await w.message("foreign", { type: "gateway.initialize", ticket: "q".repeat(32), streamId: "z".repeat(32) }, "https://host/p/app/")).ok, false);
  const pending = await w.message("first-reload", { type: "gateway.resume", handle: first.handle });
  w.advance(60001); assert.equal((await w.fetch(pending.url, { resultingClientId: "expired" })).status, 410);
  w.alive(["second-app"]); await w.sweep();
  assert.equal(w.tunnels[0].dead, true); assert.equal(w.tunnels[1].dead, false);
  assert.equal((await w.fetch("/p/app/cookie", { clientId: "first-reloaded", mode: "cors" })), undefined); // swept closed client has no authority
  assert.equal((await w.message("gone", { type: "gateway.resume", handle: first.handle })).ok, false);
  assert.equal(await (await w.fetch("/p/router/cookie", { clientId: "second-app", mode: "cors" })).text(), "g:/p/router/cookie");
  w.tunnels[1].close();
  assert.equal((await w.fetch("/p/router/cookie", { clientId: "second-app", mode: "cors" })).status, 502);
  // A worker process restart keeps public error guards, but no reusable credential.
  online = false;
  const restarted = worker();
  assert.match(await (await restarted.fetch("/p/app/web/", { resultingClientId: "stale-app" })).text(), /package bootstrap/);
  assert.equal((await restarted.message("stale-app", { type: "gateway.resume", handle: first.handle }, "https://host/p/app/web/")).ok, false);
  assert.equal(restarted.tunnels.length, 0); assert.equal(restarted.network.some(path => path.startsWith("/p/app/")), false);
  for (const cache of stores.values()) for (const response of cache.values()) { const body = await response.clone().text(); assert.ok(!body.includes(first.handle)); assert.ok(!body.includes("f".repeat(32))); assert.ok(!body.includes('"streamId"')); }
  console.log("base runtime, client binding, Firefox resume, normal network passthrough, cross-target isolation, expiry and credential-free route guards passed");
})();
