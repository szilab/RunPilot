import assert from "node:assert/strict";
import { activate } from "./plugin.js";

let entry;
const subscriptions = [];
const runpilot = {
  ui: { escape: value => String(value) },
  navigation: { register: value => { entry = value; } },
  ws: { call: async () => ({}), on: (...args) => { subscriptions.push(args); return () => {}; } },
};
const deactivate = activate(runpilot);
assert.equal(entry.id, "terminal-plugin");
assert.equal(entry.title, "Terminal (plugin)");
assert.equal(subscriptions.length, 3);
assert.ok(subscriptions.every(([plugin]) => plugin === "terminal"));
deactivate();
console.log("terminal frontend registration ok");
