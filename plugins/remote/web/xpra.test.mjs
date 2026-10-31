import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createStreamChannel } from "./stream-channel.js";
import { clientURL } from "./xpra.js";

function streamHandle() {
  return { ondata: null, onclose: null, onerror: null, sent: [], closes: 0,
    send(bytes) { this.sent.push(Uint8Array.from(bytes)); }, close() { this.closes++; this.onclose?.({ code: 1000, reason: "closed" }); } };
}

test("an announced channel opens asynchronously and replays buffered bytes", async () => {
  const stream = streamHandle(), events = [];
  const channel = createStreamChannel(stream, { label: "Xpra", pending: [new Uint8Array([80, 1])], announceOpen: true });
  assert.equal(channel.readyState, 0);
  channel.addEventListener("open", () => events.push("open"));
  channel.addEventListener("message", event => events.push([...new Uint8Array(event.data)]));
  stream.ondata(new Uint8Array([2]));
  await Promise.resolve();
  assert.equal(channel.readyState, 1);
  assert.deepEqual(events, ["open", [80, 1]]);
  stream.ondata(new Uint8Array([3]));
  assert.deepEqual(events.at(-1), [3]);
  const closes = [];
  channel.onclose = event => closes.push(event.code);
  channel.addEventListener("close", event => closes.push(event.wasClean));
  channel.close();
  assert.deepEqual(closes, [1000, true]);
  assert.equal(stream.closes, 1);
});

test("the client URL keeps the session private and disables reconnect loops", () => {
  const url = new URL(clientURL({ clipboard: false }, "https://runpilot.example/plugins/remote/web/xpra.js"));
  assert.equal(url.pathname, "/plugins/remote/web/vendor/xpra-html5/index.html");
  assert.equal(url.searchParams.get("submit"), "true");
  assert.equal(url.searchParams.get("reconnect"), "false");
  assert.equal(url.searchParams.get("clipboard"), "false");
  assert.equal(url.searchParams.get("floating_menu"), "false");
  for (const name of ["password", "token", "username"]) assert.equal(url.searchParams.has(name), false);
  assert.equal(new URL(clientURL({})).searchParams.get("clipboard"), "true");
});

test("the bridge replaces the client's WebSocket with the attached stream once", async () => {
  const stream = streamHandle(), pending = [new Uint8Array([9])], seen = { ready: 0, closed: 0 };
  globalThis.window = { frameElement: { runpilotXpra: { stream, pending, onReady: () => seen.ready++, onClose: () => seen.closed++ } }, WORKER: true };
  await import("./xpra-bridge.js?first");
  assert.equal(window.WORKER, false);
  const socket = new window.WebSocket("wss://localhost:443/", "binary");
  const received = [];
  socket.addEventListener("message", event => received.push([...new Uint8Array(event.data)]));
  await Promise.resolve();
  assert.equal(socket.readyState, window.WebSocket.OPEN);
  assert.deepEqual(received, [[9]]);
  assert.equal(seen.ready, 1);
  socket.send(new Uint8Array([80, 0, 0, 0]));
  assert.deepEqual([...stream.sent[0]], [80, 0, 0, 0]);

  // A reconnect attempt cannot open a second connection.
  const retry = new window.WebSocket("wss://localhost:443/", "binary");
  const retryEvents = [];
  retry.addEventListener("close", event => retryEvents.push(event.code));
  await Promise.resolve(); await Promise.resolve();
  assert.deepEqual(retryEvents, [1006]);
  assert.equal(stream.sent.length, 1);

  stream.onclose({ code: 1011, reason: "network stream closed" });
  assert.equal(seen.closed, 1);
  delete globalThis.window;
});

test("the vendored client loads the bridge as a module and nothing from other origins", () => {
  const page = readFileSync(new URL("./vendor/xpra-html5/index.html", import.meta.url), "utf8");
  assert.match(page, /<script type="module" src="\.\.\/\.\.\/xpra-bridge\.js"><\/script>/);
  assert.doesNotMatch(page, /<script[^>]+src="(https?:)?\/\//);
  assert.doesNotMatch(page, /<link[^>]+href="(https?:)?\/\//);
  for (const file of ["LICENSE", "README.runpilot.md", "js/Client.js", "js/Protocol.js", "js/lib/jquery.js", "default-settings.txt"]) {
    assert.ok(readFileSync(new URL(`./vendor/xpra-html5/${file}`, import.meta.url)).length > 0, file);
  }
});
