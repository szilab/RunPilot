const assert = require("node:assert/strict"), vm = require("node:vm"), fs = require("node:fs");
const { webcrypto } = require("node:crypto");
const listeners = new Map(), tunnels = [];
let sweep, now = 1000, alive = [];
const config = { basePath: "/p", publicPrefix: "/p/app", publicationId: "generation", assets: "/p/plugins/web.apps/web/" };
class Tunnel {
  constructor(publication, ticket) { this.ready = Promise.resolve(); this.ticket = ticket; this.dead = false; tunnels.push(this); }
  request(request) { return Promise.resolve(new Response(this.ticket + ":" + new URL(request.url).pathname)); }
  close() { this.dead = true; }
}
const context = { self: { RUNPILOT_PUBLICATION: config, addEventListener(type, handler) { listeners.set(type, handler); }, clients: { matchAll: async () => alive.map(id => ({ id })) } }, importScripts() {}, HTTPGatewayTunnel: Tunnel, URL, Response, crypto: webcrypto, location: { origin: "https://host" }, Date: { now: () => now }, setInterval(callback) { sweep = callback; } };
vm.runInNewContext(fs.readFileSync("plugins/web-apps/web/sw.js", "utf8"), context);
async function message(id, data, url = "https://host/p/app/") {
  let done, reply;
  listeners.get("message")({ source: { id, url }, data: { publication: "generation", url, ...data }, ports: [{ postMessage(value) { reply = value; }, close() {} }], waitUntil(promise) { done = promise; } });
  await done; return reply;
}
async function fetch(path, { clientId = "", resultingClientId = "", mode = "navigate" } = {}) {
  let response;
  listeners.get("fetch")({ request: { url: "https://host" + path, mode }, clientId, resultingClientId, respondWith(promise) { response = promise; } });
  return response;
}
(async () => {
  const first = await message("first-bootstrap", { type: "gateway.initialize", ticket: "first", streamId: "stream" });
  assert.equal(first.ok, true);
  // Firefox document fetches supply only the reserved resultingClientId.
  const handoff = await fetch(first.url, { resultingClientId: "first-app" });
  assert.equal(handoff.status, 302); assert.equal(handoff.headers.get("location"), "https://host/p/app/");
  assert.equal(handoff.headers.get("cache-control"), "no-store");
  await sweep(); assert.equal(tunnels[0].dead, false, "sweep closed a reserved client before rendering");
  assert.equal((await fetch(first.url, { resultingClientId: "replay" })).status, 410);
  assert.equal(await (await fetch("/p/app/web/", { resultingClientId: "first-app" })).text(), "first:/p/app/web/");
  assert.equal(await (await fetch("/p/app/image", { clientId: "first-app", mode: "cors" })).text(), "first:/p/app/image");
  assert.equal((await fetch("/p/app/image", { clientId: "foreign", mode: "cors" })).status, 503);
  const second = await message("second-bootstrap", { type: "gateway.initialize", ticket: "second", streamId: "other" });
  await fetch(second.url, { resultingClientId: "second-app" });
  assert.equal(await (await fetch("/p/app/cookie", { clientId: "second-app", mode: "cors" })).text(), "second:/p/app/cookie");
  const resumed = await message("first-reload", { type: "gateway.resume", handle: first.handle }, "https://host/p/app/web/?query=1#page");
  assert.equal(resumed.ok, true); assert.equal(tunnels.length, 2, "reload opened another gateway");
  const reload = await fetch(resumed.url, { resultingClientId: "first-reloaded" });
  assert.equal(reload.headers.get("location"), "https://host/p/app/web/?query=1#page");
  assert.equal(await (await fetch("/p/app/web/", { resultingClientId: "first-reloaded" })).text(), "first:/p/app/web/");
  assert.equal((await message("foreign", { type: "gateway.resume", handle: "forged" })).ok, false);
  assert.equal((await message("foreign", { type: "gateway.resume", handle: first.handle, publication: "old" })).ok, false);
  assert.equal((await message("foreign", { type: "gateway.resume", handle: first.handle }, "https://host/p/other/")).ok, false);
  assert.equal((await message("foreign", { type: "gateway.resume", handle: first.handle, url: "https://evil/app/" })).ok, false);
  const pending = await message("first-reload", { type: "gateway.resume", handle: first.handle });
  now += 60001;
  assert.equal((await fetch(pending.url, { resultingClientId: "expired" })).status, 410);
  // Closing a tab discards its handle but preserves the other tab's tunnel.
  alive = ["second-app"]; await sweep();
  assert.equal(tunnels[0].dead, true); assert.equal(tunnels[1].dead, false);
  assert.equal((await message("gone", { type: "gateway.resume", handle: first.handle })).ok, false);
  tunnels[1].close();
  assert.equal((await message("closed", { type: "gateway.resume", handle: second.handle })).ok, false);
})();
