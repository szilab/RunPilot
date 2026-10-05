const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const { MessageChannel } = require("node:worker_threads");

const bridges = [];
class TestCloseEvent extends Event { constructor(type, options = {}) { super(type); Object.assign(this, options); } }
const context = {
  Event, EventTarget, MessageEvent, MessageChannel, CloseEvent: TestCloseEvent,
  URL, TextEncoder, TextDecoder, Blob, ArrayBuffer, DOMException, Uint8Array,
  document: { baseURI: "https://public.example/p/app/" },
  location: { protocol: "https:", origin: "https://public.example", host: "public.example" },
  navigator: { serviceWorker: { controller: { postMessage(message, ports) { bridges.push({ message, port: ports[0] }); } } } },
  globalThis: null,
};
context.globalThis = context;
vm.runInNewContext(fs.readFileSync("plugins/web-apps/web/websocket-shim.js", "utf8"), context);
const tick = () => new Promise(resolve => setTimeout(resolve, 0));

(async () => {
  const WS = context.WebSocket;
  assert.equal(WS.CONNECTING, 0); assert.equal(WS.OPEN, 1); assert.equal(WS.CLOSING, 2); assert.equal(WS.CLOSED, 3);
  for (const protocols of [undefined, "runpilot", ["runpilot", "v2"]]) {
    const socket = protocols === undefined ? new WS("wss://public.example/p/app/socket?x=1") : new WS("wss://public.example/p/app/socket", protocols);
    assert.equal(socket.readyState, WS.CONNECTING); assert.equal(socket.url.startsWith("wss://public.example/p/app/socket"), true);
    assert.throws(() => new WS("wss://attacker.example/p/app/socket"), /outside this RunPilot publication/);
    const bridge = bridges.at(-1).port;
    let opened = 0; socket.onopen = () => { opened++; };
    bridge.postMessage({ type: "open", protocol: "runpilot" }); await tick();
    assert.equal(socket.readyState, WS.OPEN); assert.equal(opened, 1); assert.equal(socket.protocol, "runpilot");
    const outbound = new Promise(resolve => { bridge.onmessage = event => resolve(event.data); });
    socket.send("hello");
    // The fake service worker receives outbound data on its side of the channel.
    const sent = await outbound;
    assert.equal(sent.type, "send"); assert.equal(sent.binary, false); assert.equal(new TextDecoder().decode(sent.data), "hello");
    bridge.postMessage({ type: "drain", amount: 5 }); await tick(); assert.equal(socket.bufferedAmount, 0);
    let text = ""; socket.onmessage = event => { text = event.data; };
    bridge.postMessage({ type: "message", data: "reply", binary: false }); await tick(); assert.equal(text, "reply");
    socket.binaryType = "blob"; let blob; socket.addEventListener("message", event => { blob = event.data; }, { once: true });
    bridge.postMessage({ type: "message", data: new Uint8Array([3, 4]).buffer, binary: true }); await tick(); assert.ok(blob instanceof Blob); assert.deepEqual([...new Uint8Array(await blob.arrayBuffer())], [3, 4]);
    socket.binaryType = "arraybuffer"; let binary; socket.onmessage = event => { binary = event.data; };
    bridge.postMessage({ type: "message", data: new Uint8Array([1, 2]).buffer, binary: true }); await tick(); assert.deepEqual([...new Uint8Array(binary)], [1, 2]);
    const blobOutbound = new Promise(resolve => { bridge.onmessage = event => resolve(event.data); });
    socket.send(new Blob(["blob-send"])); const blobSent = await blobOutbound; assert.equal(blobSent.binary, true); assert.equal(new TextDecoder().decode(blobSent.data), "blob-send");
    socket.close(1000, "done"); assert.equal(socket.readyState, WS.CLOSING);
    const closed = new Promise(resolve => { socket.onclose = resolve; });
    bridge.postMessage({ type: "close", code: 1000, reason: "done", wasClean: true }); const close = await closed;
    assert.equal(socket.readyState, WS.CLOSED); assert.equal(close.code, 1000); assert.equal(close.reason, "done"); assert.equal(close.wasClean, true);
    bridge.close();
  }
  const failed = new WS("wss://public.example/p/app/fail"); let errors = 0; failed.onerror = () => errors++;
  const failedClosed = new Promise(resolve => { failed.onclose = resolve; });
  bridges.at(-1).port.postMessage({ type: "error", message: "upstream refused" }); await failedClosed;
  assert.equal(failed.readyState, WS.CLOSED); assert.equal(errors, 1);
  failed._port.close(); bridges.at(-1).port.close();
  console.log("WebSocket shim URL/protocol constructors, events, data types, close and error handling passed");
})().catch(error => { console.error(error); process.exitCode = 1; });
