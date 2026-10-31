import { baseName, escapeHTML, highlight } from "./helpers.js";

// openTextEditor shows a text file in an editor dialog with syntax colors,
// Ctrl+S saving and conflict detection. save() receives the content and the
// expected modification time ("" to overwrite) and returns the new one.
export function openTextEditor({ path, location, content, modifiedAt, readOnly, create, save }) {
  const dialog = document.createElement("dialog");
  dialog.className = "dialog files-editor-dialog";
  dialog.innerHTML = `<div class="dialog-head"><div><h2>${escapeHTML(baseName(path))}</h2><p class="files-editor-path">${escapeHTML(location)}</p></div><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div><div class="files-editor"><pre aria-hidden="true"><code></code></pre><textarea spellcheck="false" wrap="off" aria-label="File content" ${readOnly ? "readonly" : ""}></textarea></div><p class="files-editor-status" role="status">${readOnly ? "Read-only" : ""}</p><div class="dialog-actions"><button class="button secondary" type="button" data-close>${readOnly ? "Close" : "Cancel"}</button>${readOnly ? "" : '<button class="button primary" type="button" data-save>Save</button>'}</div>`;
  const area = dialog.querySelector("textarea"), code = dialog.querySelector("code"), pre = dialog.querySelector("pre"), status = dialog.querySelector(".files-editor-status");
  let expected = modifiedAt, dirty = false, saving = false, creating = create;
  const paint = () => { code.innerHTML = highlight(area.value, path); };
  area.value = content; paint();
  area.addEventListener("input", () => { dirty = true; status.textContent = "Unsaved changes"; paint(); });
  area.addEventListener("scroll", () => { pre.scrollTop = area.scrollTop; pre.scrollLeft = area.scrollLeft; });
  area.addEventListener("keydown", event => {
    if (event.key === "Tab" && !readOnly) { event.preventDefault(); area.setRangeText("  ", area.selectionStart, area.selectionEnd, "end"); area.dispatchEvent(new Event("input")); }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") { event.preventDefault(); void store(false); }
  });
  async function store(force) {
    if (readOnly || saving) return;
    saving = true; status.textContent = "Saving…";
    try {
      expected = await save(area.value, force || creating ? "" : expected, creating);
      dirty = false; creating = false; status.textContent = "Saved";
    } catch (error) {
      if (error.code === "conflict") {
        status.innerHTML = 'The file changed since it was opened. <button type="button" class="button danger small" data-overwrite>Overwrite</button>';
        status.querySelector("[data-overwrite]").addEventListener("click", () => void store(true));
      } else status.textContent = error.message;
    } finally { saving = false; }
  }
  dialog.querySelector("[data-save]")?.addEventListener("click", () => void store(false));
  dialog.querySelectorAll("[data-close]").forEach(button => button.addEventListener("click", () => { if (!dirty || confirm("Discard unsaved changes?")) dialog.close(); }));
  dialog.addEventListener("cancel", event => { if (dirty && !confirm("Discard unsaved changes?")) event.preventDefault(); });
  dialog.addEventListener("close", () => dialog.remove(), { once: true });
  document.body.append(dialog);
  dialog.showModal();
  area.focus();
}

// openImageViewer shows images of the current folder one at a time with
// previous/next navigation; url(path) resolves an inline image URL.
export function openImageViewer({ paths, index, url, onDownload }) {
  const dialog = document.createElement("dialog");
  dialog.className = "dialog files-viewer-dialog";
  dialog.innerHTML = `<div class="dialog-head"><h2></h2><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div><div class="files-viewer"><button type="button" class="files-viewer-nav" data-step="-1" aria-label="Previous image">‹</button><img alt=""><button type="button" class="files-viewer-nav" data-step="1" aria-label="Next image">›</button></div><div class="dialog-actions"><span class="files-viewer-count"></span><button class="button secondary" type="button" data-download>Download</button><button class="button primary" type="button" data-close>Close</button></div>`;
  const image = dialog.querySelector("img"), title = dialog.querySelector("h2"), count = dialog.querySelector(".files-viewer-count");
  async function show(next) {
    index = (next + paths.length) % paths.length;
    title.textContent = baseName(paths[index]); image.alt = title.textContent;
    count.textContent = paths.length > 1 ? `${index + 1} / ${paths.length}` : "";
    try { image.src = await url(paths[index]); } catch { image.removeAttribute("src"); }
  }
  dialog.addEventListener("click", event => {
    const step = event.target.closest("[data-step]")?.dataset.step;
    if (step) void show(index + Number(step));
    if (event.target.closest("[data-download]")) onDownload(paths[index]);
    if (event.target.closest("[data-close]")) dialog.close();
  });
  dialog.addEventListener("keydown", event => {
    if (event.key === "ArrowLeft") void show(index - 1);
    if (event.key === "ArrowRight") void show(index + 1);
  });
  dialog.querySelectorAll(".files-viewer-nav").forEach(button => { button.hidden = paths.length < 2; });
  dialog.addEventListener("close", () => dialog.remove(), { once: true });
  document.body.append(dialog);
  dialog.showModal();
  void show(index);
}
