import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";
const source = await fs.readFile("plugins/web-apps/web/plugin.js", "utf8");
const advanced = source.match(/<details class="webapps-advanced">([\s\S]*?)<\/details>/)?.[1];
assert.ok(advanced, "advanced settings use a collapsible details frame");
assert.match(advanced, /Advanced Settings/);
assert.match(advanced, /Base path header/);
assert.match(advanced, /Static headers/);
assert.match(advanced, /Ignore upstream TLS certificate validation/);
assert.match(advanced, /name="insecureSkipVerify"/);
assert.doesNotMatch(source, /Proxy compatibility|Advanced upstream headers/);
const plugin = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);
const ids = { runtime: "r".repeat(32), publication: "p".repeat(32), stream: "s".repeat(32), ticket: "t".repeat(32) };
globalThis.location = { origin: "https://host" };
for (const base of ["/", "/p/", "/tenant/pilot/", "/tenant%20space/%E5%BA%94%E7%94%A8/"]) {
  let opened;
  plugin.launchGateway({ ticket: ids.ticket, session: { id: ids.stream, publicationId: ids.publication, runtimeId: ids.runtime, baseURL: base, publicPrefix: base + "app" } }, (...args) => { opened = args; });
  const url = new URL(opened[0], location.origin);
  assert.equal(url.pathname, base); assert.equal(url.search, "");
  assert.deepEqual(JSON.parse(new URLSearchParams(url.hash.slice(1)).get("runpilot-publication")), ids);
  assert.equal(opened[1], "_blank"); assert.equal(opened[2], "noopener,noreferrer");
}
assert.throws(() => plugin.launchGateway({ ticket: "invalid", session: {} }, () => {}), /Invalid/);
assert.equal(plugin.publicPrefix("/p/", "/app"), "/p/app/");
const rootEntry = await fs.readFile("internal/web/static/browser-launch.js", "utf8");
function rootContext(hash, name = "") {
  const scripts = [], status = { setAttribute(key, value) { this[key] = value; } }, tab = { credential: "copied", removeItem() { this.credential = null; } }, shared = { credential: "legacy", removeItem() { this.credential = null; } };
  const context = { name, location: { hash, pathname: "/p/", search: "" }, history: { replaceState() { context.location.hash = ""; } }, document: { baseURI: "https://host/p/", createElement(tag) { return tag === "p" ? status : { setAttribute(key, value) { this[key] = value; } }; }, head: { append(script) { scripts.push(script.src); script.onload(); } }, body: { replaceChildren() {} } }, sessionStorage: tab, localStorage: shared, URL, URLSearchParams };
  vm.runInNewContext(awaitAuth, context); return { context, scripts, status, tab, shared };
}
const awaitAuth = await fs.readFile("internal/web/static/auth-storage.js", "utf8");
const fragment = "#" + new URLSearchParams({ "runpilot-publication": JSON.stringify(ids) });
const entry = rootContext(fragment); await vm.runInNewContext(rootEntry, entry.context);
assert.equal(entry.context.location.hash, ""); assert.equal(entry.tab.credential, null); assert.equal(entry.shared.credential, null);
assert.equal(entry.scripts.length, 1); assert.ok(entry.scripts[0].endsWith(`/__runpilot__/browser/${ids.runtime}/bootstrap.js?publication=${ids.publication}`));
assert.equal(entry.context.RUNPILOT_BROWSER_LAUNCH.ticket, ids.ticket);
for (const invalid of ["#runpilot-publication=bad-json", "#runpilot-publication=" + "x".repeat(1025), fragment + "&ticket=extra", "#runpilot-publication=" + encodeURIComponent(JSON.stringify({ ...ids, upstreamURL: "https://evil" }))]) {
  const test = rootContext(invalid); await vm.runInNewContext(rootEntry, test.context);
  assert.equal(test.scripts.length, 0); assert.equal(test.tab.credential, null); assert.match(test.status.textContent, /Invalid/);
}
const normal = rootContext("#ordinary-page"); await vm.runInNewContext(rootEntry, normal.context);
assert.deepEqual(normal.scripts, ["https://host/p/overview-widgets.js", "https://host/p/dashboard.js", "https://host/p/app.js"]); assert.equal(normal.tab.credential, "copied");
const stale = rootContext("", "runpilot.browser:" + JSON.stringify({ runtime: ids.runtime, publication: ids.publication, handle: "h".repeat(32) }));
await vm.runInNewContext(rootEntry, stale.context); assert.equal(stale.scripts.length, 1); assert.ok(!stale.scripts[0].endsWith("app.js"));

const bootstrap = await fs.readFile("plugins/web-apps/web/bootstrap.js", "utf8");
let registration, initialized, navigated, changed, removed = false;
const config = { runtimeId: ids.runtime, publicationId: ids.publication, scope: "/multi/p/", publicPrefix: "/multi/p/app", workerURL: `/multi/p/__runpilot__/browser/${ids.runtime}/sw.js` };
const controller = { scriptURL: "https://host" + config.workerURL, postMessage(message, ports) { initialized = message; ports[0].peer.onmessage({ data: { ok: true, handle: "h".repeat(32), url: "/multi/p/__runpilot__/browser/runtime/navigate/one-use" } }); } };
class Channel { constructor() { this.port1 = { close() {} }; this.port2 = { peer: this.port1, close() {} }; } }
const status = { setAttribute() {} };
const serviceWorker = { getRegistrations: async () => [{ scope: "https://host/multi/p/app/", active: { scriptURL: "https://host/multi/p/app/__runpilot__/sw.js?publication=old" }, unregister: async () => { removed = true; } }], register: async (url, options) => { registration = { url, options }; }, ready: Promise.resolve(), controller: null, addEventListener(type, fn) { changed = fn; }, removeEventListener() {} };
const context = { RUNPILOT_PUBLICATION: config, RUNPILOT_BROWSER_LAUNCH: { ...ids }, document: { getElementById() { return status; } }, location: { href: "https://host/multi/p/", origin: "https://host" }, isSecureContext: true, crypto: { subtle: {} }, navigator: { serviceWorker }, sessionStorage: { removeItem() {} }, localStorage: { credential: "legacy", removeItem() { this.credential = null; } }, URL, URLSearchParams, MessageChannel: Channel, setTimeout, clearTimeout };
vm.runInNewContext(awaitAuth, context);
const pending = vm.runInNewContext(bootstrap, context); await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(initialized, undefined); assert.equal(navigated, undefined); assert.equal(removed, true);
serviceWorker.controller = controller; changed();
context.location.replace = url => { navigated = url; }; await pending;
assert.equal(context.localStorage.credential, null); assert.equal(context.RUNPILOT_BROWSER_LAUNCH, undefined);
assert.equal(registration.options.scope, "/multi/p/"); assert.equal(registration.url, controller.scriptURL);
assert.equal(initialized.type, "gateway.initialize"); assert.equal(initialized.ticket, ids.ticket); assert.equal(initialized.url, "https://host/multi/p/app/"); assert.ok(navigated.includes("/navigate/"));
context.location.href = "https://host/multi/p/app/web/?query=1#route";
await vm.runInNewContext(bootstrap, context); assert.equal(initialized.type, "gateway.resume"); assert.equal(initialized.handle, "h".repeat(32)); assert.equal(initialized.url, context.location.href);
context.isSecureContext = false; await vm.runInNewContext(bootstrap, context); assert.match(status.textContent, /HTTPS/);
context.isSecureContext = true; context.name = ""; await vm.runInNewContext(bootstrap, context); assert.match(status.textContent, /expired/);
context.name = "runpilot.browser:" + JSON.stringify({ runtime: ids.runtime, publication: ids.publication, handle: "h".repeat(32) });
serviceWorker.ready = new Promise(() => {}); initialized = undefined; navigated = undefined;
context.setTimeout = fn => setTimeout(fn, 0);
await vm.runInNewContext(bootstrap, context); assert.match(status.textContent, /could not activate/); assert.equal(initialized, undefined); assert.equal(navigated, undefined);
console.log("root fragment dispatch, auth isolation, bounded launch, base scope, worker control ordering, legacy worker retirement and lineage resume passed");
