import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { attachClipboardReader, createGuacamoleStreamTunnel, guacamoleAssetURL, loadGuacamole, normalizeClipboardText, receiveClipboard, sendClipboardText } from "./plugin.js";

const frontendSource = readFileSync(new URL("./plugin.js", import.meta.url), "utf8");

const encodeInstruction = (...elements) => {
  const encoder = new TextEncoder();
  return encoder.encode(elements.map((element, index) => `${index ? "," : ""}${encoder.encode(element).length}.${element}`).join("") + ";");
};

function fakeGuacamole() {
  class Tunnel {
    static State = { CONNECTING: 0, OPEN: 1, CLOSED: 2 };
    constructor() { this.state = Tunnel.State.CLOSED; }
    setState(state) { this.state = state; this.onstatechange?.(state); }
  }
  return {
    Tunnel,
    Parser: { toInstruction: elements => new TextDecoder().decode(encodeInstruction(...elements)) },
    Status: Object.assign(class Status { constructor(code, message) { this.code = code; this.message = message; } }, { Code: { SERVER_ERROR: 512, UPSTREAM_UNAVAILABLE: 520 } }),
  };
}

test("stream tunnel delivers ready and parses fragmented UTF-8 instructions", async () => {
  const sent = [], instructions = [];
  const stream = { send: bytes => sent.push(new Uint8Array(bytes)), close() {} };
  const guac = fakeGuacamole();
  const tunnel = createGuacamoleStreamTunnel(guac, stream, "connection-1");
  tunnel.oninstruction = (opcode, args) => instructions.push({ opcode, args });
  tunnel.connect("");
  await new Promise(resolve => queueMicrotask(resolve));
  assert.deepEqual(instructions.shift(), { opcode: "ready", args: ["connection-1"] });
  const message = encodeInstruction("name", "Désktop");
  const split = message.indexOf(new TextEncoder().encode("é")[0]) + 1;
  stream.ondata(message.slice(0, split));
  stream.ondata(message.slice(split));
  assert.deepEqual(instructions, [{ opcode: "name", args: ["Désktop"] }]);
  tunnel.sendMessage("key", "65", "1");
  assert.equal(new TextDecoder().decode(sent[0]), "3.key,2.65,1.1;");
});

test("stream tunnel echoes Guacamole ping and closes on malformed protocol data", async () => {
  const sent = [], errors = [];
  let closes = 0;
  const stream = { send: bytes => sent.push(new Uint8Array(bytes)), close: () => closes++ };
  const guac = fakeGuacamole();
  const tunnel = createGuacamoleStreamTunnel(guac, stream, "connection-2");
  tunnel.onerror = error => errors.push(error);
  tunnel.connect("");
  await new Promise(resolve => queueMicrotask(resolve));
  stream.ondata(encodeInstruction("", "ping", "server"));
  assert.equal(new TextDecoder().decode(sent[0]), "0.,4.ping,6.server;");
  stream.ondata(new TextEncoder().encode("x."));
  assert.equal(tunnel.state, guac.Tunnel.State.CLOSED);
  assert.equal(closes, 1);
  assert.equal(errors.length, 1);
});

test("session startup catches measurement errors and exposes Diagnose beside Disconnect", () => {
  assert.match(frontendSource, /const state = \{ target, view,[^\n]*lastResize: null \}/);
  assert.doesNotMatch(frontendSource, /lastResize:\s*initial/);
  assert.match(frontendSource, /view\.actions\.insertBefore\(diagnose, view\.actions\.lastElementChild\)/);
  assert.match(frontendSource, /rdp\.session\.diagnostics/);
  assert.match(frontendSource, /rdp\.session\.diagnostic/);
});

test("session surface hides the local cursor on every Guacamole layer", () => {
  const css = readFileSync(new URL("./plugin.css", import.meta.url), "utf8");
  assert.match(frontendSource, /view\.surface\.element\.classList\.add\("rdp-session-surface"\)/);
  assert.match(css, /\.rdp-session-surface, \.rdp-session-surface \* \{ cursor: none; \}/);
});

test("RDP navigation uses a monitor and targets reuse the legacy responsive cards", () => {
  const css = readFileSync(new URL("./plugin.css", import.meta.url), "utf8");
  assert.match(frontendSource, /title: "RDP", icon: "monitor"/);
  assert.match(frontendSource, /settingsForm.className = "plugin-settings-form rdp-plugin-settings"/);
  assert.match(frontendSource, /class="docker-card remote-card rdp-target-card"/);
  assert.match(frontendSource, /class="rdp-target-meta-row"><div class="rdp-target-facts"><span title=/);
  assert.match(frontendSource, /span title="\$\{escapeHTML\(endpoint\)\}"/);
  assert.match(frontendSource, /rdp-target-identity" title="\$\{escapeHTML\(identity\)\}"/);
  assert.match(frontendSource, /class="rdp-target-actions"><button class="button danger small"/);
  assert.match(frontendSource, /formatUsername\(target\.options\.username, target\.options\.domain\)/);
  assert.doesNotMatch(frontendSource, /class="status installed">RDP<\/span>/);
  assert.match(css, /\.rdp-target-list \{ display: grid; grid-template-columns: repeat\(3, minmax\(0, 1fr\)\)/);
  assert.match(css, /@media \(max-width: 680px\)/);
  assert.match(css, /\.rdp-target-list \{ grid-template-columns: minmax\(0, 1fr\); \}/);
  assert.match(css, /\.rdp-target-facts span \{ overflow:hidden;[^}]*text-overflow:ellipsis; white-space:nowrap; \}/);
  assert.match(css, /\.rdp-target-identity \{ overflow:hidden;[^}]*text-overflow:ellipsis; white-space:nowrap; \}/);
  assert.doesNotMatch(frontendSource, /rdp-target-row/);
});

test("guacd TLS uses a labeled native checkbox styled as a sliding switch", () => {
  const css = readFileSync(new URL("./plugin.css", import.meta.url), "utf8");
  assert.match(frontendSource, /class="check span-2 rdp-tls-toggle"/);
  assert.match(frontendSource, /name="tls" type="checkbox" role="switch"/);
  assert.match(frontendSource, /<span>Use TLS for GUACD endpoint<\/span>/);
  assert.match(frontendSource, /tls: fields\.tls\.checked/);
  assert.match(css, /input:checked::before \{[^}]*transform: translateX\(16px\)/);
  assert.match(css, /input:focus-visible/);
  assert.match(css, /prefers-reduced-motion: reduce/);
});

test("guacd settings keep host and port visible and group other options in advanced settings", () => {
  assert.match(frontendSource, /name="port"[^\n]*<\/label><\/div><div class="plugin-settings-footer"><details class="rdp-settings-advanced"/);
  assert.match(frontendSource, /<summary>Advanced settings<\/summary><div class="form-grid"><label>Connect timeout/);
  assert.match(frontendSource, /settingsForm\.querySelector\("\.rdp-settings-advanced"\)\?\.open/);
  assert.match(frontendSource, /<\/details><div class="plugin-settings-actions rdp-settings-actions">/);
});

test("clipboard text normalization and bounds are explicit", () => {
  assert.equal(normalizeClipboardText("a\r\nb\rc\n", "unix"), "a\nb\nc\n");
  assert.equal(normalizeClipboardText("a\nb\r", "windows"), "a\r\nb\r\n");
  assert.equal(normalizeClipboardText("a\r\nb", "preserve"), "a\r\nb");
  const client = {};
  assert.equal(receiveClipboard(client, true, "remote text"), true);
  assert.equal(client.latestRemoteClipboard, "remote text");
  assert.equal(receiveClipboard(client, false, "hidden"), false);
  assert.equal(receiveClipboard(client, true, "x".repeat(1024 * 1024 + 1)), false);
});

test("Guacamole clipboard streams receive text, reject MIME/disabled data, bound size, and clean up", () => {
  class StringReader { constructor(stream) { this.stream = stream; } }
  class StringWriter { constructor(stream) { this.stream = stream; } sendText(text) { this.stream.text = text; } sendEnd() { this.stream.ended = true; } }
  const guac = { StringReader, StringWriter }, client = {}, input = { sendEnd() { this.ended = true; } };
  let active = true, completed = 0;
  const reader = attachClipboardReader(guac, client, input, "text/plain; charset=utf-8", { clipboard: true, copy: true }, () => active, () => completed++);
  reader.ontext("remote text"); reader.onend();
  assert.equal(client.latestRemoteClipboard, "remote text"); assert.equal(completed, 1);
  assert.equal(attachClipboardReader(guac, client, input, "image/png", { clipboard: true, copy: true }), null);
  assert.equal(input.ended, true);
  assert.equal(attachClipboardReader(guac, client, input, "text/plain", { clipboard: true, copy: false }), null);
  const disabled = attachClipboardReader(guac, client, input, "text/plain", { clipboard: false, copy: true });
  assert.equal(disabled, null);
  const oversized = attachClipboardReader(guac, client, input, "text/plain", { clipboard: true, copy: true });
  oversized.ontext("x".repeat(1024 * 1024 + 1)); oversized.onend();
  assert.equal(client.latestRemoteClipboard, "remote text");
  const stale = attachClipboardReader(guac, client, input, "text/plain", { clipboard: true, copy: true }, () => active);
  active = false; stale.ontext("late"); stale.onend();
  assert.equal(client.latestRemoteClipboard, "remote text");
  stale.ontext = null; stale.onend = null;
  assert.equal(stale.ontext, null); assert.equal(stale.onend, null);
});

test("Guacamole clipboard writer sends local text only when paste is enabled", () => {
  class StringWriter { constructor(stream) { this.stream = stream; } sendText(text) { this.stream.text = text; } sendEnd() { this.stream.ended = true; } }
  const guac = { StringWriter }, sent = [];
  const client = { createOutputStream(mimetype) { const stream = { mimetype }; sent.push(stream); return stream; } };
  assert.equal(sendClipboardText(guac, client, "local text", true), true);
  assert.deepEqual(sent[0], { mimetype: "text/plain", text: "local text", ended: true });
  assert.equal(sendClipboardText(guac, client, "blocked", false), false);
  assert.equal(sendClipboardText(guac, client, "x".repeat(1024 * 1024 + 1), true), false);
  assert.equal(sent.length, 1);
});

test("Guacamole loader obtains plugin asset with no core preload and memoizes concurrent loads", async () => {
  const oldWindow = globalThis.window, oldDocument = globalThis.document;
  const scripts = [];
  globalThis.window = {};
  globalThis.document = { createElement: () => ({}), head: { append(script) {
    scripts.push(script);
    queueMicrotask(() => {
      if (scripts.length === 1) { script.onerror(); return; }
      globalThis.window.Guacamole = { Client: function Client() {}, Tunnel: function Tunnel() {} }; script.onload();
    });
  } } };
  try {
    assert.equal(window.Guacamole, undefined);
    await assert.rejects(loadGuacamole(), /packaged Guacamole client/);
    const first = loadGuacamole(), second = loadGuacamole();
    assert.equal(first, second);
    const guac = await first;
    assert.equal(typeof guac.Client, "function");
    assert.equal(scripts.length, 2);
    assert.equal(scripts[1].src, guacamoleAssetURL());
  } finally {
    globalThis.window = oldWindow; globalThis.document = oldDocument;
  }
});

test("plugin asset URL stays under root and non-root plugin module paths", () => {
  assert.equal(guacamoleAssetURL("https://host/plugins/remote.rdp/web/plugin.js"), "https://host/plugins/remote.rdp/web/vendor/guacamole/guacamole-common-js-1.6.0.min.js");
  assert.equal(guacamoleAssetURL("https://host/runpilot/plugins/remote.rdp/web/plugin.js"), "https://host/runpilot/plugins/remote.rdp/web/vendor/guacamole/guacamole-common-js-1.6.0.min.js");
});

test("plugin package owns its frontend runtime and licensing files", () => {
  for (const name of ["vendor/guacamole/guacamole-common-js-1.6.0.min.js", "vendor/guacamole/LICENSE", "vendor/guacamole/NOTICE"])
    assert.ok(readFileSync(new URL(name, import.meta.url)).length > 0, `${name} must be packaged`);
  assert.match(frontendSource, /new URL\("\.\/vendor\/guacamole/);
  assert.doesNotMatch(frontendSource, /new window\.Guacamole/);
});
