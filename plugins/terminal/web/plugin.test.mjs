import assert from "node:assert/strict";
import { activate } from "./plugin.js";

let entry;
const subscriptions = [];
const assets = [];
globalThis.window = {};
globalThis.document = {
  head: { append(node) {
    assets.push(node);
    if (node.tagName === "script") {
      if (node.src.endsWith("/xterm.js")) window.Terminal = function Terminal() {};
      if (node.src.endsWith("/addon-fit.js")) window.FitAddon = { FitAddon: function FitAddon() {} };
      queueMicrotask(() => node.onload?.());
    } else if (node.tagName === "link") queueMicrotask(() => node.onload?.());
  } },
  createElement(tagName) { return { tagName, dataset: {} }; },
  querySelector() { return assets.find(asset => asset.dataset?.terminalPluginXterm); },
};
const runpilot = {
  ui: { escape: value => String(value) },
  navigation: { register: value => { entry = value; } },
  ws: { call: async () => ({}), on: (...args) => { subscriptions.push(args); return () => {}; } },
};
const deactivate = await activate(runpilot);
assert.equal(entry.id, "terminal-plugin");
assert.equal(entry.title, "Terminal");
assert.equal(subscriptions.length, 3);
assert.ok(subscriptions.every(([plugin]) => plugin === "terminal"));
assert.ok(assets.some(asset => asset.href?.endsWith("/vendor/xterm.css")));
assert.ok(assets.some(asset => asset.src?.endsWith("/vendor/xterm.js")));
assert.ok(assets.some(asset => asset.src?.endsWith("/vendor/addon-fit.js")));
deactivate();
console.log("terminal frontend registration ok");
