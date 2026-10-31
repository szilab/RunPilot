import test from "node:test";
import assert from "node:assert/strict";
import { activate } from "./plugin.js";
import { fileKind, filterEntries, formatSize, highlight, isImageName, isTextName, joinPath, parentPath, selectRange, sortEntries, uniqueName } from "./helpers.js";

test("helpers format sizes, paths and pasted names", () => {
  assert.equal(formatSize(0), "0 B");
  assert.equal(formatSize(1536), "1.5 KB");
  assert.equal(formatSize(5 * 1024 ** 3), "5.0 GB");
  assert.equal(joinPath("", "a"), "a");
  assert.equal(joinPath("a/b", "c"), "a/b/c");
  assert.equal(parentPath("a/b/c"), "a/b");
  assert.equal(parentPath("a"), "");
  const taken = new Set(["a.txt", "a (copy).txt", "folder"]);
  assert.equal(uniqueName("b.txt", taken), "b.txt");
  assert.equal(uniqueName("a.txt", taken), "a (copy 2).txt");
  assert.equal(uniqueName("folder", taken), "folder (copy)");
  assert.ok(isTextName("compose.yaml") && isTextName(".env") && isTextName(".env.local") && isTextName("Dockerfile"));
  assert.ok(!isTextName("movie.mkv") && isImageName("photo.JPG") && !isImageName("drawing.svg"));
  assert.equal(fileKind({ name: "x", type: "directory" }), "folder");
  assert.equal(fileKind({ name: "Film.MKV", type: "file" }), "video");
  assert.equal(fileKind({ name: "backup.tar", type: "file" }), "archive");
});

test("sorting keeps folders first and filtering matches names", () => {
  const entries = [
    { name: "file10.txt", type: "file", size: 5, modifiedAt: "2026-01-03T00:00:00Z" },
    { name: "file2.txt", type: "file", size: 50, modifiedAt: "2026-01-01T00:00:00Z" },
    { name: "Zeta", type: "directory", size: 0, modifiedAt: "2026-01-02T00:00:00Z" },
  ];
  assert.deepEqual(sortEntries(entries).map(item => item.name), ["Zeta", "file2.txt", "file10.txt"]);
  assert.deepEqual(sortEntries(entries, "size", false).map(item => item.name), ["Zeta", "file2.txt", "file10.txt"]);
  assert.deepEqual(sortEntries(entries, "modified", true).map(item => item.name), ["Zeta", "file2.txt", "file10.txt"]);
  assert.deepEqual(sortEntries(entries, "modified", false).map(item => item.name), ["Zeta", "file10.txt", "file2.txt"]);
  assert.deepEqual(filterEntries(entries, " FILE1 ").map(item => item.name), ["file10.txt"]);
  assert.deepEqual(selectRange(["a", "b", "c", "d"], "c", "a"), ["a", "b", "c"]);
  assert.deepEqual(selectRange(["a", "b"], "x", "b"), ["b"]);
});

test("highlighting escapes markup and colors config keys", () => {
  const html = highlight("name: <b>\n# comment\nAPI_KEY='x'", "app.yaml");
  assert.ok(!html.includes("<b>"));
  assert.match(html, /files-syntax-key">name</);
  assert.match(html, /files-syntax-comment"># comment/);
  assert.match(highlight("API_KEY=1", ".env"), /files-syntax-key">API_KEY</);
});

function fakeRuntime(replies) {
  const registered = {}, calls = [];
  const runpilot = {
    ws: { call: async (plugin, method, params) => { calls.push([method, params]); const reply = replies[method]; return typeof reply === "function" ? reply(params) : reply; }, on: () => () => {} },
    navigation: { register: entry => { registered.navigation = entry; } },
    settings: { register: entry => { registered.settings = entry; } },
    ui: { escape: String, toast: () => {}, confirm: async () => true },
  };
  return { runpilot, registered, calls };
}

const flush = () => new Promise(resolve => setTimeout(resolve, 0));
const fakePage = () => ({ isConnected: true, innerHTML: "", dataset: {}, addEventListener() {}, contains: () => true });
const fakeHeader = () => { const children = []; return { children, hidden: false, replaceChildren() { children.length = 0; }, append(node) { children.push(node); }, classList: { toggle(name, value) { this.value = value; } } }; };

test("the page opens the filesystem top and escapes names", async () => {
  const { runpilot, registered, calls } = fakeRuntime({
    "files.list": params => params.path === "" ? { path: "", top: true, hiddenCount: 1, entries: [
      { name: "<img src=x onerror=alert(1)>", type: "file", size: 10, modifiedAt: "2026-01-01T00:00:00Z" },
      { name: "srv", type: "directory", size: 0, modifiedAt: "2026-01-01T00:00:00Z" },
      { name: "bin", type: "symlink", linkType: "directory", size: 0, modifiedAt: "2026-01-01T00:00:00Z" },
    ] } : { error: { code: "not_found", message: "missing" } },
  });
  activate(runpilot);
  assert.equal(registered.navigation.id, "files");
  assert.equal(registered.settings, undefined, "Files has no settings");
  globalThis.document = { createElement: () => ({ addEventListener() {} }), addEventListener() {} };
  const page = fakePage(), header = fakeHeader();
  registered.navigation.headerActions(header);
  await registered.navigation.render(page);
  await flush();
  delete globalThis.document;
  assert.equal(header.children.length, 3);
  assert.deepEqual(calls[0], ["files.list", { path: "", showHidden: false }]);
  assert.ok(!page.innerHTML.includes("<img src=x"));
  assert.match(page.innerHTML, /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.match(page.innerHTML, /data-name="srv" data-type="directory" data-drop="folder"/);
  assert.match(page.innerHTML, /is-link" data-name="bin" data-type="directory" data-drop="folder"/, "a link to a folder opens like a folder");
  assert.match(page.innerHTML, /files-crumb-root[^>]*>.*<span>\/<\/span>/);
  assert.match(page.innerHTML, /2 folders · 1 file · 10 B · 1 hidden/);
  assert.doesNotMatch(page.innerHTML, /files-sidebar|data-action="source"/);
  assert.equal(header.classList.value, false, "the top folder offers create and upload actions");
});

test("Windows drives cannot be changed", async () => {
  const { runpilot, registered } = fakeRuntime({
    "files.list": { path: "", top: false, hiddenCount: 0, entries: [{ name: "C:", type: "directory", size: 0, modifiedAt: "" }] },
  });
  activate(runpilot);
  const page = fakePage(), header = fakeHeader();
  registered.navigation.headerActions(header);
  await registered.navigation.render(page);
  await flush();
  assert.match(page.innerHTML, /data-name="C:" data-type="directory"/);
  assert.match(page.innerHTML, /This PC/);
  assert.equal(header.classList.value, true, "the drive list has no create or upload actions");
});
