import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("./vnc.js", import.meta.url), "utf8");
const host = readFileSync(new URL("./plugin.js", import.meta.url), "utf8");
const styles = readFileSync(new URL("./plugin.css", import.meta.url), "utf8");

test("VNC targets are a protocol view within the combined Remote navigation", () => {
  assert.match(host, /id: "remote", title: "Remote", description: "Connect to RDP and VNC remote desktops\./);
  assert.match(host, /data-protocol="vnc"/);
  assert.match(host, /protocolTab === "vnc" \? vncFeature\.addTarget\(\)/);
  assert.match(source, /export function createVNCFeature/);
  assert.doesNotMatch(source, /navigation\.register/);
});

test("VNC target cards use the same responsive card structure as RDP", () => {
  assert.match(source, /className = "rp-card vnc-target-card"/);
  assert.match(source, /vnc-target-meta-row[\s\S]*?vnc-target-actions/);
  assert.match(styles, /\.vnc-target-list \{ display: grid; grid-template-columns: repeat\(3, minmax\(0, 1fr\)\); gap: 12px; \}/);
  assert.match(styles, /@media \(max-width: 1100px\)/);
  assert.match(styles, /@media \(max-width: 680px\)/);
});
