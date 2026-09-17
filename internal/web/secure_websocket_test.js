const assert = require("assert");
const fs = require("fs");
const vm = require("vm");

class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  constructor(url, protocols) {
    this.url = url;
    this.protocol = Array.isArray(protocols) ? protocols[0] : protocols || "";
    this.extensions = "permessage-deflate";
    this.bufferedAmount = 0;
    this.binaryType = "blob";
    this.readyState = FakeWebSocket.CONNECTING;
    this.sent = [];
    this.listeners = new Map();
    setTimeout(() => { this.readyState = FakeWebSocket.OPEN; this.onopen?.(); }, 0);
  }
  addEventListener(type, listener) { const list = this.listeners.get(type) || []; list.push(listener); this.listeners.set(type, list); }
  removeEventListener(type, listener) { this.listeners.set(type, (this.listeners.get(type) || []).filter(item => item !== listener)); }
  dispatch(type, event = {}) { for (const listener of this.listeners.get(type) || []) listener(event); this[`on${type}`]?.(event); }
  send(data) { this.sent.push(data); }
  close(code, reason) { this.readyState = FakeWebSocket.CLOSED; this.closeArgs = [code, reason]; this.onclose?.({code, reason}); }
}

const context = {
  window: {}, WebSocket: FakeWebSocket, Uint8Array, ArrayBuffer, DataView, Promise,
  TextEncoder, TextDecoder, setTimeout, clearTimeout,
  crypto: { getRandomValues: value => value.fill(7), subtle: {} },
};
vm.runInNewContext(fs.readFileSync("internal/web/static/secure-websocket.js", "utf8"), context);
const SecureWebSocket = context.window.RunPilotSecureWebSocket;

async function testWrapperAPI() {
  const socket = new SecureWebSocket("wss://example.test", "disabled", "guacamole");
  let opened = false, received = null, failed = false, closed = false;
  socket.onopen = () => { opened = true; };
  socket.onmessage = event => { received = event.data; };
  socket.onerror = () => { failed = true; };
  socket.onclose = () => { closed = true; };
  await new Promise(resolve => setTimeout(resolve, 5));
  assert.strictEqual(opened, true);
  assert.strictEqual(socket.protocol, "guacamole");
  assert.strictEqual(socket.extensions, "permessage-deflate");
  assert.strictEqual(socket.readyState, FakeWebSocket.OPEN);
  socket.binaryType = "arraybuffer";
  assert.strictEqual(socket.binaryType, "arraybuffer");
  socket.send("hello");
  assert.strictEqual(socket.socket.sent[0], "hello");
  socket.socket.dispatch("message", {data: "reply"});
  assert.strictEqual(received, "reply");
  socket.close(1000, "done");
  assert.strictEqual(closed, true);
  assert.strictEqual(failed, false);
}

async function testReceiveQueue() {
  const instance = Object.create(SecureWebSocket.prototype);
  instance._failed = false; instance.receiveSequence = 0n; instance.receiveChain = Promise.resolve(); instance.readNonce = new Uint8Array(12); instance.readKey = {};
  instance.socket = new FakeWebSocket("wss://example.test"); instance.socket.readyState = FakeWebSocket.OPEN;
  const received = []; let active = 0; let maximumActive = 0;
  instance.onmessage = event => received.push(new Uint8Array(event.data)[0]);
  context.crypto.subtle.decrypt = async () => {
    active++; maximumActive = Math.max(maximumActive, active);
    await new Promise(resolve => setTimeout(resolve, 5));
    active--; return new Uint8Array([received.length]);
  };
  const frame = sequence => { const data = new Uint8Array(30); data.set([82,80,87,69,1,2]); new DataView(data.buffer).setBigUint64(6, BigInt(sequence)); return {data: data.buffer}; };
  for (let sequence = 0; sequence < 4; sequence++) instance._receive(frame(sequence));
  await instance.receiveChain;
  assert.deepStrictEqual(received, [0, 1, 2, 3]);
  assert.strictEqual(maximumActive, 1);

  instance._receive(frame(1));
  await instance.receiveChain;
  assert.strictEqual(instance._failed, true);
}

(async () => { await testWrapperAPI(); await testReceiveQueue(); })().catch(error => { console.error(error); process.exitCode = 1; });
