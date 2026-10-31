// Files plugin frontend: a FileBrowser-style manager for the whole host
// filesystem. It uses only the public plugin API (runpilot.ws, navigation,
// ui) and the host's authenticated transfer route.

import { baseName, displayPath, effectiveType, escapeHTML, fileKind, filterEntries, formatDate, formatSize, icon, isImageName, isTextName, joinPath, parentPath, selectRange, sortEntries, uniqueName } from "./helpers.js";
import { pickFolder, promptName } from "./dialogs.js";
import { openImageViewer, openTextEditor } from "./viewers.js";

export { formatSize, highlight, isImageName, isTextName, joinPath, parentPath, uniqueName } from "./helpers.js";

const PLUGIN = "files";
const PREFS_KEY = "runpilot.files.preferences";
const DRAG_TYPE = "application/x-runpilot-files";
const THUMBNAIL_LIMIT = 12 * 1024 * 1024;

function csrfToken() {
  const cookie = document.cookie.split(";").map(value => value.trim()).find(value => value.startsWith("runpilot_csrf="));
  return cookie ? decodeURIComponent(cookie.slice("runpilot_csrf=".length)) : "";
}

// uploadFile streams one file to a transfer URL and reports progress.
function uploadFile(url, file, onProgress) {
  const request = new XMLHttpRequest();
  const done = new Promise((resolve, reject) => {
    request.open("PUT", url);
    request.withCredentials = true;
    const token = csrfToken();
    if (token) request.setRequestHeader("X-RunPilot-CSRF", token);
    request.upload.onprogress = event => onProgress(event.loaded, event.lengthComputable ? event.total : file.size);
    request.onload = () => {
      if (request.status >= 200 && request.status < 300) { resolve(); return; }
      let message = `Upload failed (${request.status})`;
      try { message = JSON.parse(request.responseText).error || message; } catch {}
      reject(new Error(message));
    };
    request.onerror = () => reject(new Error("Upload failed: network error"));
    request.onabort = () => reject(new Error("Upload canceled"));
    request.send(file);
  });
  return { done, abort: () => request.abort() };
}

function loadPreferences() {
  const defaults = { view: "list", sortKey: "name", ascending: true, showHidden: false, info: false, path: "" };
  try { return { ...defaults, ...JSON.parse(localStorage.getItem(PREFS_KEY) || "{}") }; } catch { return defaults; }
}

const listingError = error => ({
  not_found: "This folder does not exist.",
  permission_denied: "The RunPilot service account cannot open this folder.",
}[error.code] || error.message);

export function activate(runpilot) {
  const { ui } = runpilot;
  const call = async (method, params = {}) => {
    const result = await runpilot.ws.call(PLUGIN, method, params);
    if (result?.error) { const error = new Error(result.error.message || "Files request failed"); error.code = result.error.code; throw error; }
    return result;
  };
  const fail = error => ui.toast(error.message || String(error), "error");
  const confirmAction = options => ui.confirm ? ui.confirm(options) : Promise.resolve(confirm(options.message));

  const prefs = loadPreferences();
  const savePreferences = () => { try { localStorage.setItem(PREFS_KEY, JSON.stringify(prefs)); } catch {} };
  // drives: the Windows top level, which lists drives and cannot hold items.
  const state = { path: prefs.path || "", entries: [], hiddenCount: 0, drives: false, loading: true, error: "", query: "", selected: new Set(), anchor: "", clipboard: null, editingPath: false };
  const tasks = new Map(); // background operations and uploads
  const thumbnails = new Map(); // path -> {url, expires}
  let page = null, headerRoot = null, menu = null, observer = null, loaded = false;

  const pageActive = () => !!page?.isConnected && (!page.closest || page.closest(".page")?.classList.contains("active"));
  const visibleEntries = () => sortEntries(filterEntries(state.entries, state.query), prefs.sortKey, prefs.ascending);
  const selectedItems = () => state.entries.filter(item => state.selected.has(item.name));
  const pathOf = name => joinPath(state.path, name);
  const isFolder = item => effectiveType(item) === "directory";
  const isFile = item => effectiveType(item) === "file";
  const writable = () => !state.drives && !state.error;

  // ---- data

  async function loadListing() {
    state.loading = true; render();
    try {
      const result = await call("files.list", { path: state.path, showHidden: prefs.showHidden });
      state.path = result.path ?? state.path;
      state.entries = result.entries || []; state.hiddenCount = result.hiddenCount || 0; state.error = "";
      state.drives = !state.path && !result.top;
    } catch (error) {
      state.entries = []; state.drives = false; state.error = listingError(error);
    }
    state.loading = false;
    state.selected = new Set([...state.selected].filter(name => state.entries.some(item => item.name === name)));
    prefs.path = state.error ? prefs.path : state.path; savePreferences();
    render();
  }

  async function navigate(path) {
    if (path !== state.path) state.query = "";
    state.path = path; state.selected.clear(); state.anchor = ""; state.editingPath = false;
    await loadListing();
    page?.querySelector(".files-content")?.focus({ preventScroll: true });
  }

  async function inlineURL(path) {
    const cached = thumbnails.get(path);
    if (cached && cached.expires - Date.now() > 30000) return cached.url;
    const ticket = await call("files.download", { path, inline: true });
    if (!ticket.inline) throw new Error("not an inline image");
    thumbnails.set(path, { url: ticket.url, expires: Date.parse(ticket.expiresAt) || Date.now() + 240000 });
    return ticket.url;
  }

  // ---- rendering

  function renderHeader() {
    if (!headerRoot || !pageActive()) return;
    const show = loaded && writable();
    headerRoot.replaceChildren();
    // The host hides an empty slot when the page opens; the buttons appear
    // only after the folder has loaded, so keep the slot's visibility in sync.
    headerRoot.classList?.toggle("hidden", !show);
    if (!show) return;
    const add = (label, handler, style = "secondary") => { const button = document.createElement("button"); button.type = "button"; button.className = `button ${style} small`; button.textContent = label; button.addEventListener("click", handler); headerRoot.append(button); };
    add("New folder", () => void createFolder());
    add("New file", () => void createFile());
    add("Upload", () => chooseUploads(), "primary");
  }

  const toolButton = (action, name, label, extra = "") => `<button type="button" class="files-tool" data-action="${action}" aria-label="${escapeHTML(label)}" title="${escapeHTML(label)}" ${extra}>${icon(name)}</button>`;

  function renderBar() {
    const items = selectedItems(), count = items.length;
    if (count) {
      const single = count === 1 ? items[0] : null, files = items.filter(isFile), changeable = !state.drives;
      const openable = single && (isFolder(single) || isFile(single));
      const actions = [
        openable ? toolButton("open-selected", isFolder(single) ? "folder" : isTextName(single.name) ? "edit" : "eye", isFolder(single) ? "Open" : isTextName(single.name) ? "Edit" : "Open") : "",
        files.length ? toolButton("download-selected", "download", files.length > 1 ? `Download ${files.length} files` : "Download") : "",
        single && changeable ? toolButton("rename-selected", "rename", "Rename (F2)") : "",
        changeable ? toolButton("copy-to", "copy", "Copy to…") : "",
        changeable ? toolButton("move-to", "move", "Move to…") : "",
        changeable ? toolButton("delete-selected", "delete", "Delete (Del)", 'data-danger="true"') : "",
        toolButton("toggle-info", "info", "Details", `aria-pressed="${prefs.info}"`),
      ].join("");
      return `<div class="files-bar files-selection-bar">${toolButton("clear-selection", "close", "Clear selection (Esc)")}<span class="files-selection-count">${count} selected</span><div class="files-bar-actions">${actions}</div></div>`;
    }
    const paste = state.clipboard && writable() ? `<button type="button" class="button secondary small" data-action="paste">Paste ${state.clipboard.items.length}</button>` : "";
    return `<div class="files-bar"><label class="files-search">${icon("search")}<input type="search" data-role="search" placeholder="Filter this folder" value="${escapeHTML(state.query)}" aria-label="Filter this folder"></label><div class="files-bar-actions">${paste}${toolButton("toggle-view", prefs.view === "grid" ? "list" : "grid", prefs.view === "grid" ? "List view" : "Grid view")}${toolButton("toggle-hidden", "eye", prefs.showHidden ? "Hide hidden files" : "Show hidden files", `aria-pressed="${prefs.showHidden}"`)}${toolButton("toggle-info", "info", "Details", `aria-pressed="${prefs.info}"`)}${toolButton("refresh", "refresh", "Refresh")}</div></div>`;
  }

  function renderPath() {
    const up = toolButton("up", "up", "Parent folder (Backspace)", state.path ? "" : "disabled");
    if (state.editingPath) {
      return `<div class="files-path">${up}<form class="files-path-form" data-role="path-form"><input data-role="path-input" value="${escapeHTML(displayPath(state.path))}" spellcheck="false" autocomplete="off" aria-label="Folder path"></form></div>`;
    }
    const parts = state.path ? state.path.split("/") : [];
    const crumbs = [`<button type="button" class="files-crumb files-crumb-root" data-action="go" data-path="" data-drop="crumb" title="${escapeHTML(displayPath(""))}">${icon("computer")}<span>${/^[A-Za-z]:/.test(state.path) || state.drives ? "This PC" : "/"}</span></button>`]
      .concat(parts.map((part, index) => `${index ? '<span class="files-crumb-sep" aria-hidden="true">/</span>' : ""}<button type="button" class="files-crumb" data-action="go" data-path="${escapeHTML(parts.slice(0, index + 1).join("/"))}" data-drop="crumb">${escapeHTML(part)}</button>`)).join("");
    return `<div class="files-path">${up}<nav class="files-crumbs" aria-label="Path" data-action="edit-path">${crumbs}</nav>${toolButton("edit-path", "edit", "Type a path (Ctrl+L)")}</div>`;
  }

  function itemAttributes(item) {
    const selected = state.selected.has(item.name);
    const thumb = prefs.view === "grid" && isFile(item) && isImageName(item.name) && item.size <= THUMBNAIL_LIMIT;
    const cut = state.clipboard?.mode === "cut" && state.clipboard.items.includes(pathOf(item.name));
    return `class="files-item${selected ? " selected" : ""}${item.hidden ? " is-hidden" : ""}${cut ? " is-cut" : ""}${item.type === "symlink" ? " is-link" : ""}" data-name="${escapeHTML(item.name)}" data-type="${effectiveType(item)}"${isFolder(item) ? ' data-drop="folder"' : ""}${thumb ? ` data-thumb="${escapeHTML(pathOf(item.name))}"` : ""} draggable="${state.drives ? "false" : "true"}" role="option" aria-selected="${selected}" tabindex="-1"`;
  }

  const sizeText = item => item.type === "file" ? formatSize(item.size) : "";

  function renderItems(entries) {
    if (prefs.view === "grid") {
      return `<div class="files-grid" role="listbox" aria-multiselectable="true" aria-label="Files">${entries.map(item => `<div ${itemAttributes(item)}><div class="files-tile-visual files-kind-${fileKind(item)}">${icon(state.drives ? "drive" : fileKind(item))}<img alt="" hidden></div><div class="files-tile-name" title="${escapeHTML(item.name)}">${escapeHTML(item.name)}</div><div class="files-tile-meta">${item.type === "file" ? formatSize(item.size) : isFolder(item) ? (state.drives ? "Drive" : "Folder") : "Link"}</div></div>`).join("")}</div>`;
    }
    const sortHead = (key, label) => `<button type="button" class="files-sort${prefs.sortKey === key ? " active" : ""}" data-action="sort" data-key="${key}">${label}${prefs.sortKey === key ? `<span aria-hidden="true">${prefs.ascending ? "▲" : "▼"}</span>` : ""}</button>`;
    return `<div class="files-list" role="listbox" aria-multiselectable="true" aria-label="Files"><div class="files-list-head">${sortHead("name", "Name")}${sortHead("size", "Size")}${sortHead("modified", "Modified")}</div>${entries.map(item => `<div ${itemAttributes(item)}><span class="files-item-name"><span class="files-kind-${fileKind(item)}">${icon(state.drives ? "drive" : fileKind(item))}</span><span class="files-item-text" title="${escapeHTML(item.name)}">${escapeHTML(item.name)}</span></span><span class="files-item-size">${sizeText(item)}</span><time class="files-item-date" datetime="${escapeHTML(item.modifiedAt || "")}" title="${item.modifiedAt ? escapeHTML(new Date(item.modifiedAt).toLocaleString()) : ""}">${item.modifiedAt ? escapeHTML(formatDate(item.modifiedAt)) : ""}</time></div>`).join("")}</div>`;
  }

  function renderInfo() {
    if (!prefs.info) return "";
    const items = selectedItems();
    let body;
    if (items.length === 1) {
      const item = items[0];
      const thumb = isFile(item) && isImageName(item.name) && item.size <= THUMBNAIL_LIMIT;
      const type = item.type === "symlink" ? `Link to ${item.linkType || "a missing target"}` : isFolder(item) ? "Folder" : fileKind(item);
      body = `<div class="files-info-visual files-kind-${fileKind(item)}"${thumb ? ` data-thumb="${escapeHTML(pathOf(item.name))}"` : ""}>${icon(fileKind(item))}${thumb ? '<img class="files-info-image" alt="" hidden>' : ""}</div><h3>${escapeHTML(item.name)}</h3><dl><dt>Type</dt><dd>${escapeHTML(type)}</dd>${item.type === "file" ? `<dt>Size</dt><dd>${formatSize(item.size)} (${item.size.toLocaleString()} bytes)</dd>` : ""}${item.modifiedAt ? `<dt>Modified</dt><dd>${escapeHTML(new Date(item.modifiedAt).toLocaleString())}</dd>` : ""}<dt>Path</dt><dd class="files-info-path">${escapeHTML(displayPath(pathOf(item.name)))}</dd></dl>`;
    } else if (items.length > 1) {
      const size = items.reduce((total, item) => total + (item.type === "file" ? item.size : 0), 0);
      body = `<div class="files-info-visual">${icon("copy")}</div><h3>${items.length} items selected</h3><dl><dt>Files</dt><dd>${items.filter(isFile).length}</dd><dt>Folders</dt><dd>${items.filter(isFolder).length}</dd><dt>Size of files</dt><dd>${formatSize(size)}</dd></dl>`;
    } else {
      body = `<div class="files-info-visual files-kind-folder">${icon(state.path ? "folder" : "computer")}</div><h3>${escapeHTML(state.path ? baseName(state.path) : state.drives ? "This PC" : "/")}</h3><dl><dt>Path</dt><dd class="files-info-path">${escapeHTML(displayPath(state.path))}</dd></dl>`;
    }
    return `<aside class="files-info" aria-label="Details">${toolButton("toggle-info", "close", "Close details")}${body}</aside>`;
  }

  function renderFooter(entries) {
    const folders = entries.filter(isFolder).length, files = entries.length - folders;
    const size = entries.reduce((total, item) => total + (item.type === "file" ? item.size : 0), 0);
    const parts = [`${folders} ${folders === 1 ? "folder" : "folders"}`, `${files} ${files === 1 ? "file" : "files"}`];
    if (files) parts.push(formatSize(size));
    if (state.query) parts.push(`filtered from ${state.entries.length}`);
    if (state.hiddenCount && !prefs.showHidden) parts.push(`${state.hiddenCount} hidden`);
    return `<footer class="files-footer">${parts.join(" · ")}</footer>`;
  }

  function renderTasks() {
    if (!tasks.size) return "";
    const rows = [...tasks.values()].map(task => {
      const total = task.totalBytes || 0, percent = total > 0 ? Math.min(100, Math.round(task.bytes / total * 100)) : (task.state === "succeeded" ? 100 : 0);
      const detail = task.state === "running" ? (total > 0 ? `${formatSize(task.bytes)} of ${formatSize(total)}` : task.phase || "working…") : task.state === "succeeded" ? "Done" : task.error || task.state;
      return `<div class="files-task" data-state="${escapeHTML(task.state)}"><div class="files-task-text"><strong title="${escapeHTML(task.label)}">${escapeHTML(task.label)}</strong><span>${escapeHTML(detail)}</span></div>${task.state === "running" ? `<button type="button" class="files-tool" data-action="cancel-task" data-id="${escapeHTML(task.id)}" aria-label="Cancel" title="Cancel">${icon("close")}</button>` : ""}<progress max="100" value="${percent}"></progress></div>`;
    }).join("");
    return `<section class="files-tasks" aria-label="File activity">${rows}</section>`;
  }

  function render() {
    renderHeader();
    if (!page?.isConnected) return;
    const active = globalThis.document?.activeElement;
    const focused = active?.dataset?.role === "search" ? [active.selectionStart, active.selectionEnd] : null;
    const entries = visibleEntries();
    const content = state.error ? `<div class="files-message" role="alert">${icon("folder", "files-empty-icon")}<p>${escapeHTML(state.error)}</p><button type="button" class="button secondary small" data-action="up">Go to the parent folder</button></div>`
      : state.loading && !state.entries.length ? `<div class="files-message" role="status">Loading…</div>`
      : entries.length ? renderItems(entries)
      : `<div class="files-message">${icon("folder", "files-empty-icon")}<p>${state.query ? "No items match the filter." : "This folder is empty. Drop files here to upload."}</p></div>`;
    page.innerHTML = `<div class="files-plugin${prefs.info ? " with-info" : ""}"><section class="files-main">${renderBar()}${renderPath()}<div class="files-content view-${prefs.view}" tabindex="0" data-role="content">${content}</div>${renderTasks()}${renderFooter(entries)}</section>${renderInfo()}</div>`;
    if (focused) {
      const search = page.querySelector('[data-role="search"]');
      search?.focus(); search?.setSelectionRange(...focused);
    }
    if (state.editingPath) {
      const input = page.querySelector('[data-role="path-input"]');
      input?.focus(); input?.select();
    }
    observeThumbnails();
  }

  let renderPending = false;
  function scheduleRender() {
    if (renderPending) return;
    renderPending = true;
    requestAnimationFrame(() => { renderPending = false; render(); });
  }

  // Thumbnails load lazily when their tile scrolls into view.
  function observeThumbnails() {
    observer?.disconnect();
    const imageOf = node => node.tagName === "IMG" ? node : node.querySelector("img");
    const nodes = [...(page.querySelectorAll?.("[data-thumb]") || [])].filter(node => !imageOf(node)?.getAttribute("src"));
    if (!nodes.length || typeof IntersectionObserver !== "function") return;
    observer = new IntersectionObserver(found => {
      for (const entry of found) {
        if (!entry.isIntersecting) continue;
        observer.unobserve(entry.target);
        const image = imageOf(entry.target);
        inlineURL(entry.target.dataset.thumb).then(url => {
          image.onload = () => { image.hidden = false; image.closest(".files-tile-visual, .files-info-visual")?.classList.add("has-image"); };
          image.src = url;
        }).catch(() => {});
      }
    }, { root: null, rootMargin: "200px" });
    nodes.forEach(node => observer.observe(node));
  }

  // ---- selection

  function selectOnly(name) { state.selected = new Set(name ? [name] : []); state.anchor = name || ""; }

  // updateSelection refreshes only the selection marks, the top bar and the
  // details panel. Replacing the items would break double-click detection.
  function updateSelection() {
    if (!page?.querySelectorAll) { render(); return; }
    for (const node of page.querySelectorAll(".files-item")) {
      const selected = state.selected.has(node.dataset.name);
      node.classList.toggle("selected", selected);
      node.setAttribute("aria-selected", String(selected));
    }
    const bar = page.querySelector(".files-main > .files-bar");
    if (bar) bar.outerHTML = renderBar();
    const info = page.querySelector(".files-info");
    if (info) { info.outerHTML = renderInfo(); observeThumbnails(); }
  }

  function clickItem(item, event) {
    const name = item.dataset.name;
    if (event.shiftKey && state.anchor) {
      const range = selectRange(visibleEntries().map(entry => entry.name), state.anchor, name);
      state.selected = new Set(event.ctrlKey || event.metaKey ? [...state.selected, ...range] : range);
    } else if (event.ctrlKey || event.metaKey) {
      if (state.selected.has(name)) state.selected.delete(name); else state.selected.add(name);
      state.anchor = name;
    } else selectOnly(name);
    updateSelection();
    page.querySelector(".files-content")?.focus({ preventScroll: true });
  }

  function moveFocus(step, extend) {
    const names = visibleEntries().map(entry => entry.name);
    if (!names.length) return;
    const current = names.indexOf([...state.selected].at(-1) ?? "");
    const next = names[Math.max(0, Math.min(names.length - 1, current < 0 ? 0 : current + step))];
    if (extend && state.anchor) state.selected = new Set(selectRange(names, state.anchor, next)); else selectOnly(next);
    updateSelection();
    page.querySelector(`.files-item[data-name="${CSS.escape(next)}"]`)?.scrollIntoView({ block: "nearest" });
  }

  function gridColumns() {
    const tiles = [...page.querySelectorAll(".files-grid .files-item")];
    if (tiles.length < 2) return 1;
    const top = tiles[0].offsetTop;
    const index = tiles.findIndex(tile => tile.offsetTop !== top);
    return index < 0 ? tiles.length : index;
  }

  // ---- opening and transfers

  async function openItem(name) {
    const item = state.entries.find(entry => entry.name === name);
    if (!item) return;
    const path = pathOf(name);
    if (isFolder(item)) { await navigate(path); return; }
    if (!isFile(item)) { ui.toast("This link points to a missing item.", "error"); return; }
    if (isImageName(name)) {
      const images = visibleEntries().filter(entry => isFile(entry) && isImageName(entry.name)).map(entry => pathOf(entry.name));
      openImageViewer({ paths: images, index: Math.max(0, images.indexOf(path)), url: inlineURL, onDownload: imagePath => void download([imagePath]) });
      return;
    }
    if (isTextName(name)) { await editText(path); return; }
    await download([path]);
  }

  async function editText(path, create = false) {
    let opened = { content: "", modifiedAt: "" };
    if (!create) {
      try { opened = await call("files.text.read", { path }); }
      catch (error) {
        if (error.code === "binary" || error.code === "too_large") { ui.toast(error.message); await download([path]); return; }
        fail(error); return;
      }
    }
    openTextEditor({
      path, location: displayPath(path), content: opened.content, modifiedAt: opened.modifiedAt, readOnly: false, create,
      save: async (content, expectedModifiedAt, creating) => {
        const result = await call("files.text.write", { path, content, expectedModifiedAt, create: creating });
        if (parentPath(path) === state.path) void loadListing();
        return result.modifiedAt;
      },
    });
  }

  async function download(paths) {
    for (const path of paths) {
      try {
        const ticket = await call("files.download", { path });
        const link = document.createElement("a"); link.href = ticket.url; link.download = baseName(path); link.rel = "noopener";
        document.body.append(link); link.click(); link.remove();
      } catch (error) { fail(error); }
    }
  }

  function chooseUploads() {
    const input = document.createElement("input"); input.type = "file"; input.multiple = true;
    input.addEventListener("change", () => void uploadFiles([...input.files], state.path));
    input.click();
  }

  async function uploadFiles(files, dir) {
    if (!files.length || (dir === state.path && !writable())) return;
    const taken = dir === state.path ? new Set(state.entries.map(item => item.name)) : new Set();
    for (const file of files) {
      let overwrite = false;
      if (taken.has(file.name)) {
        overwrite = await confirmAction({ title: "Replace file?", message: `“${file.name}” already exists in this folder. Replace it?`, confirmLabel: "Replace", danger: true });
        if (!overwrite) continue;
      }
      const id = `upload-${Date.now()}-${Math.random().toString(36).slice(2)}`;
      const task = { id, label: `Uploading ${file.name}`, state: "running", bytes: 0, totalBytes: file.size, upload: null };
      tasks.set(id, task); render();
      try {
        const ticket = await call("files.upload", { path: dir, name: file.name, overwrite });
        task.upload = uploadFile(ticket.url, file, (loaded, total) => { task.bytes = loaded; task.totalBytes = total; scheduleRender(); });
        await task.upload.done;
        task.state = "succeeded"; task.bytes = task.totalBytes;
      } catch (error) { task.state = "failed"; task.error = error.message; }
      finishTask(id);
      if (dir === state.path) void loadListing();
    }
  }

  // Finished tasks stay visible briefly; failures stay longer.
  function finishTask(id) {
    render();
    const linger = tasks.get(id)?.state === "succeeded" ? 2500 : 15000;
    setTimeout(() => { const task = tasks.get(id); if (task && task.state !== "running") { tasks.delete(id); render(); } }, linger);
  }

  // ---- file operations

  async function createFolder() {
    const name = await promptName({ title: "New folder", label: "Folder name", confirmLabel: "Create" });
    if (!name) return;
    try { await call("files.folder.create", { path: state.path, name }); await loadListing(); selectOnly(name); updateSelection(); }
    catch (error) { fail(error); }
  }

  async function createFile() {
    const name = await promptName({ title: "New file", label: "File name", value: "new.txt", confirmLabel: "Create" });
    if (!name) return;
    if (state.entries.some(item => item.name === name)) { ui.toast("An item with this name already exists", "error"); return; }
    await editText(pathOf(name), true);
  }

  async function rename(name) {
    if (state.drives) return;
    const next = await promptName({ title: "Rename", label: "New name", value: name, confirmLabel: "Rename" });
    if (!next || next === name) return;
    try { await call("files.rename", { path: pathOf(name), name: next }); await loadListing(); selectOnly(next); updateSelection(); }
    catch (error) { fail(error); }
  }

  async function remove(names) {
    if (state.drives || !names.length) return;
    const message = names.length === 1 ? `Permanently delete “${names[0]}”? Folders are deleted with all their contents.` : `Permanently delete ${names.length} items? Folders are deleted with all their contents.`;
    if (!await confirmAction({ title: "Delete?", message, confirmLabel: "Delete", danger: true })) return;
    state.selected.clear();
    for (const name of names) await startOperation("delete", pathOf(name), null, `Deleting ${name}`);
  }

  async function startOperation(kind, from, to, label) {
    try {
      const result = await call("files.operation.start", to === null ? { kind, from } : { kind, from, to });
      tasks.set(result.operation.id, { ...result.operation, label });
      render();
    } catch (error) { fail(error); }
  }

  // transfer copies or moves paths into a target folder, renaming copies
  // that would collide and skipping moves onto themselves.
  async function transfer(kind, paths, target) {
    let taken = new Set();
    try { taken = new Set((await call("files.list", { path: target, showHidden: true })).entries.map(item => item.name)); }
    catch (error) { fail(error); return; }
    for (const path of paths) {
      if (target === path || target.startsWith(`${path}/`)) continue; // into itself
      if (kind === "move" && parentPath(path) === target) continue; // already there
      const name = uniqueName(baseName(path), taken); taken.add(name);
      await startOperation(kind, path, joinPath(target, name), `${kind === "move" ? "Moving" : "Copying"} ${baseName(path)}`);
    }
  }

  async function transferTo(kind, names) {
    const target = await pickFolder({
      title: kind === "move" ? "Move to" : "Copy to", confirmLabel: kind === "move" ? "Move here" : "Copy here",
      start: state.path, display: displayPath,
      list: path => call("files.list", { path, showHidden: prefs.showHidden }),
    });
    if (target !== null) await transfer(kind, names.map(pathOf), target);
  }

  function setClipboard(mode, names) {
    if (!names.length || state.drives) return;
    state.clipboard = { mode, items: names.map(pathOf) };
    ui.toast(`${names.length} ${names.length === 1 ? "item" : "items"} ${mode === "cut" ? "cut" : "copied"}. Open the target folder and press Ctrl+V or Paste.`);
    render();
  }

  async function paste() {
    const clipboard = state.clipboard;
    if (!clipboard || !writable()) return;
    if (clipboard.mode === "cut") state.clipboard = null;
    await transfer(clipboard.mode === "cut" ? "move" : "copy", clipboard.items, state.path);
    render();
  }

  // ---- context menu

  function closeMenu() { menu?.remove(); menu = null; }

  function openMenu(x, y, entries) {
    closeMenu();
    menu = document.createElement("div");
    menu.className = "files-context-menu";
    menu.setAttribute("role", "menu");
    menu.innerHTML = entries.filter(Boolean).map(entry => entry === "-" ? '<div class="files-menu-separator" role="separator"></div>' : `<button type="button" role="menuitem" class="files-menu-item${entry.danger ? " danger" : ""}" data-menu="${entry.action}">${icon(entry.icon)}<span>${escapeHTML(entry.label)}</span>${entry.key ? `<kbd>${escapeHTML(entry.key)}</kbd>` : ""}</button>`).join("");
    document.body.append(menu);
    const box = menu.getBoundingClientRect();
    menu.style.left = `${Math.max(8, Math.min(x, innerWidth - box.width - 8))}px`;
    menu.style.top = `${Math.max(8, Math.min(y, innerHeight - box.height - 8))}px`;
    menu.addEventListener("click", event => {
      const action = event.target.closest("[data-menu]")?.dataset.menu;
      closeMenu();
      if (action) void runAction(action);
    });
    menu.querySelector("button")?.focus();
  }

  function itemMenu(x, y) {
    const items = selectedItems(), single = items.length === 1 ? items[0] : null, files = items.filter(isFile), changeable = !state.drives;
    openMenu(x, y, [
      single && (isFolder(single) || isFile(single)) && { action: "open-selected", icon: isFolder(single) ? "folder" : "eye", label: "Open", key: "Enter" },
      single && isFile(single) && isTextName(single.name) && { action: "edit-selected", icon: "edit", label: "Edit" },
      files.length && { action: "download-selected", icon: "download", label: files.length > 1 ? `Download ${files.length} files` : "Download" },
      changeable && "-",
      single && changeable && { action: "rename-selected", icon: "rename", label: "Rename", key: "F2" },
      changeable && { action: "copy-selected", icon: "copy", label: "Copy", key: "Ctrl+C" },
      changeable && { action: "cut-selected", icon: "move", label: "Cut", key: "Ctrl+X" },
      changeable && { action: "copy-to", icon: "copy", label: "Copy to…" },
      changeable && { action: "move-to", icon: "move", label: "Move to…" },
      { action: "toggle-info", icon: "info", label: "Details" },
      changeable && "-",
      changeable && { action: "delete-selected", icon: "delete", label: "Delete", key: "Del", danger: true },
    ]);
  }

  function folderMenu(x, y) {
    const canWrite = writable();
    openMenu(x, y, [
      canWrite && { action: "new-folder", icon: "folder", label: "New folder" },
      canWrite && { action: "new-file", icon: "text", label: "New file" },
      canWrite && { action: "upload", icon: "download", label: "Upload files" },
      canWrite && state.clipboard && { action: "paste", icon: "copy", label: `Paste ${state.clipboard.items.length}`, key: "Ctrl+V" },
      canWrite && "-",
      { action: "select-all", icon: "check", label: "Select all", key: "Ctrl+A" },
      { action: "edit-path", icon: "edit", label: "Go to path…", key: "Ctrl+L" },
      { action: "refresh", icon: "refresh", label: "Refresh" },
    ]);
  }

  // ---- actions

  async function runAction(action, target = null) {
    const names = selectedItems().map(item => item.name);
    switch (action) {
      case "go": await navigate(target.dataset.path); break;
      case "up": if (state.path) await navigate(parentPath(state.path)); break;
      case "refresh": await loadListing(); break;
      case "edit-path": state.editingPath = true; render(); break;
      case "toggle-view": prefs.view = prefs.view === "grid" ? "list" : "grid"; savePreferences(); render(); break;
      case "toggle-hidden": prefs.showHidden = !prefs.showHidden; savePreferences(); await loadListing(); break;
      case "toggle-info": prefs.info = !prefs.info; savePreferences(); render(); break;
      case "sort": {
        const key = target.dataset.key;
        prefs.ascending = prefs.sortKey === key ? !prefs.ascending : true; prefs.sortKey = key; savePreferences(); render(); break;
      }
      case "clear-selection": selectOnly(""); updateSelection(); break;
      case "select-all": state.selected = new Set(visibleEntries().map(item => item.name)); updateSelection(); break;
      case "open-selected": if (names.length === 1) await openItem(names[0]); break;
      case "edit-selected": if (names.length === 1) await editText(pathOf(names[0])); break;
      case "download-selected": await download(selectedItems().filter(isFile).map(item => pathOf(item.name))); break;
      case "rename-selected": if (names.length === 1) await rename(names[0]); break;
      case "copy-selected": setClipboard("copy", names); break;
      case "cut-selected": setClipboard("cut", names); break;
      case "copy-to": await transferTo("copy", names); break;
      case "move-to": await transferTo("move", names); break;
      case "delete-selected": await remove(names); break;
      case "paste": await paste(); break;
      case "new-folder": await createFolder(); break;
      case "new-file": await createFile(); break;
      case "upload": chooseUploads(); break;
      case "cancel-task": {
        const task = tasks.get(target.dataset.id);
        if (task?.upload) task.upload.abort();
        else if (task) call("files.operation.cancel", { id: task.id }).catch(fail);
        break;
      }
    }
  }

  // ---- events

  function onClick(event) {
    const actionTarget = event.target.closest("[data-action]");
    if (actionTarget && page.contains(actionTarget)) {
      // A click on a crumb navigates; a click on the empty path area edits it.
      if (actionTarget.dataset.action === "edit-path" && event.target.closest(".files-crumb")) return;
      if (!actionTarget.disabled) void runAction(actionTarget.dataset.action, actionTarget);
      return;
    }
    const item = event.target.closest(".files-item");
    if (item) {
      // Touch has no double-click: a tap opens, like a mobile file manager.
      if (event.pointerType === "touch" && !state.selected.size) { void openItem(item.dataset.name); return; }
      clickItem(item, event);
      return;
    }
    if (event.target.closest(".files-content") && state.selected.size) { selectOnly(""); updateSelection(); }
  }

  function onDoubleClick(event) {
    const item = event.target.closest(".files-item");
    if (item) void openItem(item.dataset.name);
  }

  function onContextMenu(event) {
    if (!event.target.closest(".files-content")) return;
    event.preventDefault();
    const item = event.target.closest(".files-item");
    if (item) {
      if (!state.selected.has(item.dataset.name)) { selectOnly(item.dataset.name); updateSelection(); }
      itemMenu(event.clientX, event.clientY);
    } else folderMenu(event.clientX, event.clientY);
  }

  function onSubmit(event) {
    if (event.target.dataset.role !== "path-form") return;
    event.preventDefault();
    const value = event.target.querySelector("input").value.trim().replaceAll("\\", "/").replace(/^\/+/, "").replace(/\/+$/, "");
    void navigate(value);
  }

  function onKeyDown(event) {
    if (event.target.dataset?.role === "path-input" && event.key === "Escape") { state.editingPath = false; render(); return; }
    if (event.target.closest?.("input, textarea, select, dialog")) {
      if (event.key === "Escape" && event.target.dataset?.role === "search" && state.query) { state.query = ""; render(); }
      return;
    }
    const modifier = event.ctrlKey || event.metaKey, key = event.key.toLowerCase();
    const names = selectedItems().map(item => item.name);
    const handled = () => event.preventDefault();
    if (modifier && key === "a") { handled(); void runAction("select-all"); }
    else if (modifier && key === "c") { handled(); setClipboard("copy", names); }
    else if (modifier && key === "x") { handled(); setClipboard("cut", names); }
    else if (modifier && key === "v") { handled(); void paste(); }
    else if (modifier && key === "f") { handled(); page.querySelector('[data-role="search"]')?.focus(); }
    else if (modifier && key === "l") { handled(); void runAction("edit-path"); }
    else if (event.key === "Delete") { handled(); void remove(names); }
    else if (event.key === "F2" && names.length === 1) { handled(); void rename(names[0]); }
    else if (event.key === "Enter" && names.length === 1) { handled(); void openItem(names[0]); }
    else if (event.key === "Backspace" || (event.altKey && event.key === "ArrowUp")) { handled(); void runAction("up"); }
    else if (event.key === "Escape") { closeMenu(); if (state.selected.size) { handled(); selectOnly(""); updateSelection(); } }
    else if (event.key === "ArrowDown" || event.key === "ArrowUp") { handled(); moveFocus((event.key === "ArrowDown" ? 1 : -1) * (prefs.view === "grid" ? gridColumns() : 1), event.shiftKey); }
    else if (prefs.view === "grid" && (event.key === "ArrowRight" || event.key === "ArrowLeft")) { handled(); moveFocus(event.key === "ArrowRight" ? 1 : -1, event.shiftKey); }
  }

  function onInput(event) {
    if (event.target.dataset.role !== "search") return;
    state.query = event.target.value;
    selectOnly("");
    render();
  }

  // Dragging items onto a folder or a path crumb moves them (copies with
  // Ctrl); dropping files from the computer uploads them.
  function dropTarget(event) {
    const node = event.target.closest?.("[data-drop]");
    if (!node || !page.contains(node)) return null;
    if (node.dataset.drop === "folder") return { node, path: pathOf(node.dataset.name) };
    return { node, path: node.dataset.path };
  }

  function onDragStart(event) {
    const item = event.target.closest?.(".files-item");
    if (!item || state.drives) return;
    if (!state.selected.has(item.dataset.name)) { selectOnly(item.dataset.name); updateSelection(); }
    event.dataTransfer.setData(DRAG_TYPE, JSON.stringify(selectedItems().map(entry => pathOf(entry.name))));
    event.dataTransfer.effectAllowed = "copyMove";
  }

  function onDragOver(event) {
    const types = [...(event.dataTransfer?.types || [])];
    const target = dropTarget(event);
    page.querySelectorAll(".drop-hover").forEach(node => node.classList.remove("drop-hover"));
    if (types.includes(DRAG_TYPE) && target && (target.path || !state.drives)) {
      event.preventDefault();
      event.dataTransfer.dropEffect = event.ctrlKey ? "copy" : "move";
      target.node.classList.add("drop-hover");
    } else if (types.includes("Files") && writable()) {
      event.preventDefault();
      event.dataTransfer.dropEffect = "copy";
      (target?.node || page.querySelector(".files-content"))?.classList.add("drop-hover");
    }
  }

  function onDrop(event) {
    page.querySelectorAll(".drop-hover").forEach(node => node.classList.remove("drop-hover"));
    const target = dropTarget(event);
    const internal = event.dataTransfer?.getData(DRAG_TYPE);
    if (internal) {
      if (!target) return;
      event.preventDefault();
      void transfer(event.ctrlKey ? "copy" : "move", JSON.parse(internal), target.path);
      return;
    }
    if (event.dataTransfer?.files?.length && writable()) {
      event.preventDefault();
      void uploadFiles([...event.dataTransfer.files], target ? target.path : state.path);
    }
  }

  function wire(root) {
    if (root.dataset.filesWired) return;
    root.dataset.filesWired = "true";
    root.addEventListener("click", onClick);
    root.addEventListener("dblclick", onDoubleClick);
    root.addEventListener("contextmenu", onContextMenu);
    root.addEventListener("keydown", onKeyDown);
    root.addEventListener("input", onInput);
    root.addEventListener("submit", onSubmit);
    root.addEventListener("focusout", event => {
      if (event.target.dataset?.role === "path-input" && state.editingPath) { state.editingPath = false; setTimeout(render); }
    });
    root.addEventListener("dragstart", onDragStart);
    root.addEventListener("dragover", onDragOver);
    root.addEventListener("dragleave", event => { if (!root.contains(event.relatedTarget)) root.querySelectorAll(".drop-hover").forEach(node => node.classList.remove("drop-hover")); });
    root.addEventListener("drop", onDrop);
    globalThis.document?.addEventListener("pointerdown", event => { if (menu && !menu.contains(event.target)) closeMenu(); });
    globalThis.addEventListener?.("blur", closeMenu);
  }

  // ---- registration

  const stopEvents = runpilot.ws.on(PLUGIN, "files.operation", status => {
    const task = tasks.get(status?.id);
    if (!task) return;
    Object.assign(task, status);
    if (status.state !== "running") {
      if (status.state === "failed") ui.toast(`${task.label} failed: ${status.error || "unknown error"}`, "error");
      finishTask(status.id);
      void loadListing();
    } else scheduleRender();
  });

  runpilot.navigation.register({
    id: PLUGIN, title: "Files", description: "Browse and manage files on this host.",
    icon: { src: new URL("./icon.svg", import.meta.url).href },
    headerActions: root => { headerRoot = root; renderHeader(); },
    render: async root => {
      page = root; wire(root); render();
      await loadListing();
      // A remembered folder that no longer opens falls back to the top.
      if (state.error && state.path && !loaded) { state.path = ""; await loadListing(); }
      loaded = true; render();
    },
  });

  return () => { stopEvents?.(); closeMenu(); observer?.disconnect(); for (const task of tasks.values()) task.upload?.abort(); page = null; headerRoot = null; };
}
