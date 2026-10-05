import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("./plugin.js", import.meta.url), "utf8");
const styles = readFileSync(new URL("./plugin.css", import.meta.url), "utf8");

test("VNC Add target uses the shared application header action slot", () => {
  assert.match(source, /id: "remote-vnc", title: "VNC"[\s\S]*?headerActions\(root\)/);
  assert.match(source, /button\.textContent = "Add target"/);
  assert.doesNotMatch(source, /Connect to saved VNC desktops\./);
  assert.doesNotMatch(source, /header\.innerHTML = `[^`]*<h1>VNC<\/h1>/);
});

test("VNC target cards use the same responsive card structure as RDP", () => {
  assert.match(source, /className = "docker-card remote-card vnc-target-card"/);
  assert.match(source, /vnc-target-meta-row[\s\S]*?vnc-target-actions/);
  assert.match(styles, /\.vnc-target-list \{ display: grid; grid-template-columns: repeat\(3, minmax\(0, 1fr\)\); gap: 12px; \}/);
  assert.match(styles, /@media \(max-width: 1100px\)/);
  assert.match(styles, /@media \(max-width: 680px\)/);
});
