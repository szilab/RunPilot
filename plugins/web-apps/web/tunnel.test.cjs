const assert = require("node:assert/strict"), vm = require("node:vm"), fs = require("node:fs");
const tick = () => new Promise(resolve => setTimeout(resolve, 0));
class Socket {
  constructor(url, mode) { this.mode = mode; this.url = url; this.sent = []; this.bufferedAmount = 0; setTimeout(() => this.onopen(), 0); }
  async send(data) { this.sent.push(data); if (typeof data === "string") setTimeout(() => this.onmessage({ data: JSON.stringify({ type: "stream.attached", streamId: JSON.parse(data).streamId }) }), 0); }
  close() {}
}
const context = { RunPilotSecureWebSocket: Socket, TextEncoder, TextDecoder, Uint8Array, DataView, URL, Headers, Response, ReadableStream, DecompressionStream, setTimeout, Date, location: { origin: "https://host" } };
vm.runInNewContext(fs.readFileSync("plugins/web-apps/web/tunnel.js", "utf8"), context);
const config = { basePath: "/tenant/pilot", owner: "web.apps", publicPrefix: "/tenant/pilot/app" }, stream = "a".repeat(32);
const encoder = new TextEncoder();
const inbound = (type, id, input = new Uint8Array()) => {
  const data = input instanceof Uint8Array ? input : encoder.encode(JSON.stringify(input));
  const out = new Uint8Array(10 + data.length); out.set([1, type]); const view = new DataView(out.buffer); view.setUint32(2, id); view.setUint32(6, data.length); out.set(data, 10); return out;
};
const grant = n => { const out = new Uint8Array(4); new DataView(out.buffer).setUint32(0, n); return out; };
(async () => {
  const tunnel = new context.HTTPGatewayTunnel(config, "scoped", stream); await tunnel.ready;
  assert.equal(tunnel.socket.mode, "required"); assert.equal(tunnel.socket.url.pathname, "/tenant/pilot/api/v1/ws");
  const first = tunnel.request(new Request("https://host/tenant/pilot/app/one")), second = tunnel.request(new Request("https://host/tenant/pilot/app/two")); await tick();
  const header = inbound(5, 2, { status: 200, headers: { "Content-Type": ["text/plain"], "Set-Cookie": ["secret"] } });
  tunnel.receive(header.slice(0, 7)); tunnel.receive(header.slice(7));
  const response2 = await second; assert.equal(response2.headers.get("set-cookie"), null);
  tunnel.receive(inbound(5, 1, { status: 206, headers: { "Content-Range": ["bytes 0-4/100"] } })); const response1 = await first;
  const reader1 = response1.body.getReader(), reader2 = response2.body.getReader();
  await tick(); tunnel.receive(inbound(6, 2, encoder.encode("two"))); tunnel.receive(inbound(6, 1, encoder.encode("first")));
  assert.equal(new TextDecoder().decode((await reader1.read()).value), "first"); assert.equal(new TextDecoder().decode((await reader2.read()).value), "two");
  assert.equal(response1.status, 206); assert.equal(response1.headers.get("content-range"), "bytes 0-4/100");
  tunnel.receive(inbound(7, 1)); tunnel.receive(inbound(7, 2)); assert.equal((await reader1.read()).done, true); assert.equal(tunnel.exchanges.size, 0);
  // A large upload sends nothing until host credit, then at most the granted window.
  const body = new ReadableStream({ start(controller) { controller.enqueue(new Uint8Array(65536)); controller.enqueue(new Uint8Array(34464)); controller.close(); } });
  const post = tunnel.request(new Request("https://host/tenant/pilot/app/upload", { method: "POST", body, duplex: "half" })); await tick();
  const binary = () => tunnel.socket.sent.filter(data => typeof data !== "string");
  const before = binary().length; await tick(); assert.equal(binary().length, before);
  tunnel.receive(inbound(9, 3, grant(65536))); await tick();
  const uploadBytes = binary().filter(data => data[39] === 2).reduce((sum, data) => sum + data.length - 48, 0);
  assert.equal(uploadBytes, 65536);
  tunnel.receive(inbound(5, 3, { status: 204, headers: {} })); const postResponse = await post; assert.equal(postResponse.body, null); tunnel.receive(inbound(7, 3));
  const abort = new AbortController(), canceled = tunnel.request(new Request("https://host/tenant/pilot/app/cancel", { signal: abort.signal })); await tick(); abort.abort(); await assert.rejects(canceled, /cancel/); assert.equal(tunnel.exchanges.size, 0);
  const queued = new context.HTTPGatewayTunnel(config, "scoped", stream); await queued.ready;
  const active = []; for (let i = 0; i < 16; i++) { const response = queued.request(new Request("https://host/tenant/pilot/app/" + i)); response.catch(() => {}); active.push(response); }
  const waiting = queued.request(new Request("https://host/tenant/pilot/app/wait")); waiting.catch(() => {}); await tick();
  assert.equal(queued.exchanges.size, 16); assert.equal(queued.waiters.length, 1);
  queued.receive(inbound(5, 1, { status: 204, headers: {} })); queued.receive(inbound(7, 1)); await active[0]; await tick();
  assert.equal(queued.exchanges.size, 16); assert.equal(queued.waiters.length, 0);
  queued.fail(new Error("done")); await assert.rejects(waiting, /done/);
  const failed = tunnel.request(new Request("https://host/tenant/pilot/app/fail")); await tick(); tunnel.fail(new Error("Encrypted transport failed")); await assert.rejects(failed, /Encrypted/);
  assert.equal(tunnel.dead, true);
  await assert.rejects(tunnel.request(new Request("https://evil/")), /unavailable/);
})();
