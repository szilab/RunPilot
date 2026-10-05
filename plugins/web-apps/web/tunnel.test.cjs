const assert = require("node:assert/strict"), vm = require("node:vm"), fs = require("node:fs");
const tick = () => new Promise(resolve => setTimeout(resolve, 0));
class Socket {
  constructor(url, mode) { this.mode = mode; this.url = url; this.sent = []; this.bufferedAmount = 0; setTimeout(() => this.onopen(), 0); }
  async send(data) { this.sent.push(data); if (typeof data === "string") setTimeout(() => this.onmessage({ data: JSON.stringify({ type: "stream.attached", streamId: JSON.parse(data).streamId }) }), 0); }
  close() {}
}
const context = { RunPilotSecureWebSocket: Socket, TextEncoder, TextDecoder, Uint8Array, DataView, URL, Headers, Response, ReadableStream, DecompressionStream, setTimeout, clearTimeout, Date, location: { origin: "https://host" } };
vm.runInNewContext(fs.readFileSync("plugins/web-apps/web/tunnel.js", "utf8"), context);
const config = { basePath: "/tenant/pilot", owner: "web.apps", publicPrefix: "/tenant/pilot/app" }, stream = "a".repeat(32);
const encoder = new TextEncoder();
const inbound = (type, id, input = new Uint8Array()) => {
  const data = input instanceof Uint8Array ? input : encoder.encode(JSON.stringify(input));
  const out = new Uint8Array(10 + data.length); out.set([2, type]); const view = new DataView(out.buffer); view.setUint32(2, id); view.setUint32(6, data.length); out.set(data, 10); return out;
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
  // Firefox's incoming Request has blob() but no body property. Its Blob stream
  // must carry the upload with the same credit bounds as the native body stream.
  const firefoxRequest = new Request("https://host/tenant/pilot/app/firefox", { method: "POST", body: "x".repeat(70000) });
  Object.defineProperty(firefoxRequest, "body", { value: undefined });
  const firefoxId = tunnel.nextId, firefox = tunnel.request(firefoxRequest); await tick();
  const firefoxBytes = () => binary().filter(data => data[39] === 2 && new DataView(data.buffer).getUint32(40) === firefoxId).reduce((sum, data) => sum + data.length - 48, 0);
  assert.equal(firefoxBytes(), 0);
  tunnel.receive(inbound(9, firefoxId, grant(65536))); await tick(); await tick();
  assert.equal(firefoxBytes(), 65536);
  tunnel.receive(inbound(9, firefoxId, grant(4464))); await tick(); await tick();
  assert.equal(firefoxBytes(), 70000);
  tunnel.receive(inbound(5, firefoxId, { status: 204, headers: {} })); await firefox; tunnel.receive(inbound(7, firefoxId));
  // WebSocket channels use the same encrypted stream with a separate high-bit ID space.
  const wsEvents = [], wsReady = tunnel.openSocket("/tenant/pilot/app/socket?x=1", ["chat"], event => wsEvents.push(event));
  await tick(); const wsId = 0x80000001;
  const wsOpen = binary().find(data => data[39] === 11 && new DataView(data.buffer).getUint32(40) === wsId);
  assert.ok(wsOpen, "WebSocket open was multiplexed through the existing parent tunnel");
  const openFrame = inbound(12, wsId, { protocol: "chat" }); tunnel.receive(openFrame); assert.equal(await wsReady, wsId);
  const wsPayload = new Uint8Array([1, 1, ...encoder.encode("hello")]); tunnel.receive(inbound(13, wsId, wsPayload)); await tick();
  assert.deepEqual(wsEvents.map(event => event.type), ["open", "message"]); assert.equal(wsEvents[1].data, "hello");
  tunnel.receive(inbound(16, wsId, { code: 1001, reason: "remote", wasClean: true }));
  assert.equal(wsEvents[2].type, "close"); assert.equal(wsEvents[2].code, 1001); assert.equal(wsEvents[2].reason, "remote"); assert.equal(wsEvents[2].wasClean, true);
  const unrelated = tunnel.request(new Request("https://host/tenant/pilot/app/still-alive")); await tick(); const unrelatedId = tunnel.nextId - 1;
  const failedReady = tunnel.openSocket("/tenant/pilot/app/fails", [], () => {}); await tick();
  tunnel.receive(inbound(17, 0x80000002, encoder.encode("upstream refused"))); await assert.rejects(failedReady, /upstream refused/);
  assert.equal(tunnel.dead, false); assert.equal(tunnel.exchanges.has(unrelatedId), true);
  tunnel.receive(inbound(5, unrelatedId, { status: 204, headers: {} })); const unrelatedResponse = await unrelated; tunnel.receive(inbound(7, unrelatedId)); assert.equal(unrelatedResponse.status, 204);
  const otherEvents = [], otherReady = tunnel.openSocket("/tenant/pilot/app/other", [], event => otherEvents.push(event)); await tick();
  tunnel.receive(inbound(12, 0x80000003, { protocol: "" })); await otherReady;
  const queued = new context.HTTPGatewayTunnel(config, "scoped", stream); await queued.ready;
  const active = []; for (let i = 0; i < 16; i++) { const response = queued.request(new Request("https://host/tenant/pilot/app/" + i)); response.catch(() => {}); active.push(response); }
  const waiting = queued.request(new Request("https://host/tenant/pilot/app/wait")); waiting.catch(() => {}); await tick();
  assert.equal(queued.exchanges.size, 16); assert.equal(queued.waiters.length, 1);
  queued.receive(inbound(5, 1, { status: 204, headers: {} })); queued.receive(inbound(7, 1)); await active[0]; await tick();
  assert.equal(queued.exchanges.size, 16); assert.equal(queued.waiters.length, 0);
  queued.fail(new Error("done")); await assert.rejects(waiting, /done/);
  const failed = tunnel.request(new Request("https://host/tenant/pilot/app/fail")); await tick(); tunnel.fail(new Error("Encrypted transport failed")); await assert.rejects(failed, /Encrypted/);
  assert.ok(otherEvents.some(event => event.type === "close" && event.code === 1006), "parent close cleaned up active WebSockets");
  assert.equal(tunnel.dead, true);
  await assert.rejects(tunnel.request(new Request("https://evil/")), /unavailable/);
})();
