import test from "node:test";
import assert from "node:assert/strict";
import { createTargetStatus, macField, statusKey } from "./target-status.js";

class Node {
  constructor(key) { this.dataset = { statusKey: key, state: "unknown" }; this.title = ""; this.label = { textContent: "" }; }
  querySelector() { return this.label; }
}
function fakeRoot(keys) {
  const nodes = keys.map(key => new Node(key));
  return { nodes, isConnected: true, querySelectorAll: () => nodes };
}

test("status badges render, refresh in place and escape names", async () => {
  const calls = [];
  const status = createTargetStatus(async method => {
    calls.push(method);
    return { statuses: [{ kind: "rdp", id: "rdp-1", state: "online", latencyMilliseconds: 4 }, { kind: "vnc", id: "vnc-1", state: "service-down" }] };
  }, { setIntervalFn: () => 1, clearIntervalFn: () => {} });
  assert.match(status.badge("rdp", "rdp-1"), /data-state="unknown"/);
  const root = fakeRoot([statusKey("rdp", "rdp-1"), statusKey("vnc", "vnc-1"), statusKey("rdp", "rdp-2")]);
  status.observe(root);
  await status.refresh();
  assert.deepEqual(calls, ["remote.targets.status"]);
  assert.deepEqual(root.nodes.map(node => node.dataset.state), ["online", "service-down", "unknown"]);
  assert.equal(root.nodes[0].label.textContent, "Online");
  assert.match(root.nodes[0].title, /4 ms/);
  assert.match(status.badge("vnc", 'x"><img'), /data-status-key="vnc:x&quot;&gt;&lt;img"/);
});

test("a failed check keeps the last state and reports the error", async () => {
  let fail = false;
  const status = createTargetStatus(async () => { if (fail) throw new Error("socket closed"); return { statuses: [{ kind: "rdp", id: "a", state: "online" }] }; });
  await status.refresh();
  fail = true; await status.refresh();
  const described = status.describe("rdp", "a");
  assert.equal(described.state, "online");
  assert.match(described.title, /socket closed/);
});

test("polling runs only while visible and stops cleanly", async () => {
  let tick, cleared = false, visible = false, count = 0;
  const status = createTargetStatus(async () => { count++; return { statuses: [] }; }, { setIntervalFn: fn => { tick = fn; return 7; }, clearIntervalFn: id => { cleared = id === 7; }, isVisible: () => visible });
  status.start(); await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(count, 1);
  tick(); await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(count, 1, "hidden page must not poll");
  visible = true; tick(); await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(count, 2);
  status.stop(); assert.ok(cleared);
});

test("wake reports the MAC and schedules re-checks", async () => {
  const toasts = [], timers = [], calls = [];
  const status = createTargetStatus(async (method, params) => {
    calls.push([method, params]);
    if (method === "remote.targets.wake") return { mac: "aa:bb:cc:dd:ee:ff", source: "discovered" };
    return { statuses: [] };
  }, { toast: message => toasts.push(message), setTimeoutFn: (fn, delay) => timers.push(delay) });
  const result = await status.wake("vnc", { id: "vnc-1", name: "Pi" });
  assert.equal(result.mac, "aa:bb:cc:dd:ee:ff");
  assert.deepEqual(calls[0], ["remote.targets.wake", { kind: "vnc", id: "vnc-1" }]);
  assert.deepEqual(toasts, ["Wake-on-LAN sent to Pi · aa:bb:cc:dd:ee:ff (detected)"]);
  assert.deepEqual(timers, [15000, 45000]);
  const failing = createTargetStatus(async () => { throw new Error("No MAC address is known for Pi."); }, { toast: message => toasts.push(message) });
  assert.equal(await failing.wake("vnc", { id: "vnc-1", name: "Pi" }), null);
  assert.equal(toasts.at(-1), "No MAC address is known for Pi.");
});

test("the MAC field shows the configured or detected address", () => {
  assert.match(macField({ macAddress: "aa:bb:cc:dd:ee:ff" }), /value="aa:bb:cc:dd:ee:ff"/);
  assert.match(macField({ discoveredMac: "11:22:33:44:55:66" }), /placeholder="Detected: 11:22:33:44:55:66"/);
  assert.match(macField(null), /placeholder="Detected automatically"/);
  assert.match(macField({ macAddress: '"><x' }), /value="&quot;&gt;&lt;x"/);
});
