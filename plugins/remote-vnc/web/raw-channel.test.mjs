import test from "node:test";
import assert from "node:assert/strict";
import { createVNCChannelAdapter } from "./raw-channel.js";

globalThis.WebSocket ??= { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 };

function streamHandle() {
  return { ondata: null, onclose: null, onerror: null, sent: [], closes: 0,
    send(bytes) { this.sent.push(bytes.slice()); }, close() { this.closes++; this.onclose?.({ code: 1000, reason: "closed" }); } };
}

test("raw channel sends and receives binary payloads without changing their bytes", () => {
  const stream = streamHandle(), channel = createVNCChannelAdapter(stream), received = [];
  channel.onmessage = event => received.push(new Uint8Array(event.data));
  channel.send(new Uint8Array([0, 1, 127, 255]));
  stream.ondata(new Uint8Array([255, 4, 0]));
  assert.deepEqual([...stream.sent[0]], [0, 1, 127, 255]);
  assert.deepEqual([...received[0]], [255, 4, 0]);
  assert.equal(channel.binaryType, "arraybuffer");
  assert.equal(channel.readyState, WebSocket.OPEN);
});

test("raw channel splits large noVNC frames at the RunPilot stream frame limit", () => {
  const stream = streamHandle(), channel = createVNCChannelAdapter(stream);
  const payload = Uint8Array.from({ length: 70000 }, (_, index) => index % 251);
  channel.send(payload);
  assert.deepEqual(stream.sent.map(frame => frame.length), [32768, 32768, 4464]);
  assert.deepEqual(Buffer.concat(stream.sent.map(frame => Buffer.from(frame))), Buffer.from(payload));
});

test("raw channel forwards close and error lifecycle once", () => {
  const stream = streamHandle(), channel = createVNCChannelAdapter(stream), closed = [], errors = [];
  channel.onclose = event => closed.push(event);
  channel.onerror = event => errors.push(event);
  stream.onclose({ code: "disconnected", reason: "network stream closed" });
  stream.onclose({ code: "disconnected" });
  assert.equal(channel.readyState, WebSocket.CLOSED);
  assert.equal(closed.length, 1);
  const failed = createVNCChannelAdapter(streamHandle());
  failed.onerror = event => errors.push(event);
  failed.send(new Uint8Array());
  failed.close();
  assert.equal(errors.length, 0);
  const brokenStream = streamHandle();
  brokenStream.send = () => { throw new Error("send failed"); };
  const broken = createVNCChannelAdapter(brokenStream);
  broken.onerror = event => errors.push(event);
  assert.throws(() => broken.send(new Uint8Array([1])), /send failed/);
  assert.equal(broken.readyState, WebSocket.CLOSED);
  assert.equal(errors.length, 1);
  assert.equal(closed.length, 1);
});

test("raw channel rejects text frames", () => {
  const channel = createVNCChannelAdapter(streamHandle());
  assert.throws(() => channel.send("password"), /binary/);
});
