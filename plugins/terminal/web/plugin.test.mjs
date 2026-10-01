import assert from "node:assert/strict";
import { activate } from "./plugin.js";

let entry;
const subscriptions = [];
const assets = [];
const themeListeners = new Set();
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
  ui: {
    escape: value => String(value),
    theme: {
      get: () => ({ fontFamily: "monospace", colors: { surfaceElevated: "surface", text: "text", accent: "accent", selection: "selection" } }),
      subscribe: listener => { themeListeners.add(listener); return () => themeListeners.delete(listener); },
    },
  },
  navigation: { register: value => { entry = value; } },
  ws: { call: async () => ({}), on: (...args) => { subscriptions.push(args); return () => {}; } },
};
const deactivate = await activate(runpilot);
assert.equal(entry.id, "terminal");
assert.equal(entry.title, "Terminal");
assert.equal(subscriptions.length, 3);
assert.ok(subscriptions.every(([plugin]) => plugin === "terminal"));
assert.equal(themeListeners.size, 1);
assert.ok(assets.some(asset => asset.href?.endsWith("/vendor/xterm.css")));
assert.ok(assets.some(asset => asset.src?.endsWith("/vendor/xterm.js")));
assert.ok(assets.some(asset => asset.src?.endsWith("/vendor/addon-fit.js")));
deactivate();
assert.equal(themeListeners.size, 0);
console.log("terminal frontend registration ok");
