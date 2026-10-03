import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { activate, splitArgs, formatArgs } from "./plugin.js";

const frontendSource = readFileSync(new URL("./plugin.js", import.meta.url), "utf8");
assert.match(frontendSource, /data-settings-test/);
assert.match(frontendSource, /call\("terminal.settings.test", \{ command: settings.command, args \}\)/);
assert.match(frontendSource, /settings\.saving \|\| settings\.testing/);
assert.match(frontendSource, /settings\.status = "Terminal command started successfully\."/);

assert.deepEqual(splitArgs(""), []);
assert.deepEqual(splitArgs(" --noprofile   --norc "), ["--noprofile", "--norc"]);
assert.deepEqual(splitArgs(`--title "My terminal" 'single quoted' "" escaped\\ value`), ["--title", "My terminal", "single quoted", "", "escaped value"]);
assert.deepEqual(splitArgs("'C:\\Users\\name'"), ["C:\\Users\\name"]);
assert.throws(() => splitArgs('"unclosed'), /unclosed quote/);
assert.throws(() => splitArgs("trailing\\"), /escape character/);
const argumentValues = ["-l", "", "two words", 'a"quote', "a'quote", "C:\\Users\\name", "tab\tvalue", "$HOME", "&&"];
assert.deepEqual(splitArgs(formatArgs(argumentValues)), argumentValues);

let entry;
let settingsEntry;
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
  settings: { register: value => { settingsEntry = value; } },
  ws: { call: async () => ({}), on: (...args) => { subscriptions.push(args); return () => {}; } },
};
const deactivate = await activate(runpilot);
assert.equal(entry.id, "terminal");
assert.equal(entry.title, "Terminal");
assert.equal(settingsEntry.id, "terminal");
assert.equal(typeof settingsEntry.render, "function");
assert.equal(subscriptions.length, 3);
assert.ok(subscriptions.every(([plugin]) => plugin === "terminal"));
assert.equal(themeListeners.size, 1);
assert.ok(assets.some(asset => asset.href?.endsWith("/vendor/xterm.css")));
assert.ok(assets.some(asset => asset.src?.endsWith("/vendor/xterm.js")));
assert.ok(assets.some(asset => asset.src?.endsWith("/vendor/addon-fit.js")));
deactivate();
assert.equal(themeListeners.size, 0);
console.log("terminal frontend registration ok");
