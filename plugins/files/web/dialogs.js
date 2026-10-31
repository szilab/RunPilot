import { escapeHTML, icon, joinPath, parentPath } from "./helpers.js";

// showDialog builds a modal dialog and resolves with the value passed to
// finish(), or null when it is dismissed.
function showDialog(className, html, setup) {
  return new Promise(resolve => {
    const dialog = document.createElement("dialog");
    dialog.className = `dialog ${className}`;
    dialog.innerHTML = html;
    let result = null;
    const finish = value => { result = value; dialog.close(); };
    dialog.querySelectorAll("[data-close]").forEach(button => button.addEventListener("click", () => dialog.close()));
    dialog.addEventListener("close", () => { dialog.remove(); resolve(result); }, { once: true });
    document.body.append(dialog);
    setup(dialog, finish);
    dialog.showModal();
  });
}

const head = title => `<div class="dialog-head"><h2>${escapeHTML(title)}</h2><button class="icon-btn" type="button" data-close aria-label="Close">×</button></div>`;

export function promptName({ title, label, value = "", confirmLabel = "Save" }) {
  return showDialog("small files-name-dialog", `<form method="dialog">${head(title)}<label>${escapeHTML(label)}<input name="name" required maxlength="255" autocomplete="off" spellcheck="false"></label><div class="dialog-actions"><button class="button secondary" type="button" data-close>Cancel</button><button class="button primary" type="submit">${escapeHTML(confirmLabel)}</button></div></form>`, (dialog, finish) => {
    const input = dialog.querySelector("input");
    input.value = value;
    dialog.querySelector("form").addEventListener("submit", event => { event.preventDefault(); finish(input.value.trim() || null); });
    queueMicrotask(() => {
      input.focus();
      const dot = value.lastIndexOf(".");
      input.setSelectionRange(0, dot > 0 ? dot : value.length);
    });
  });
}

// pickFolder lets the user browse the filesystem and choose a target
// folder, like FileBrowser's copy and move dialogs.
export function pickFolder({ title, confirmLabel, start, list, display }) {
  return showDialog("files-picker-dialog", `${head(title)}<div class="files-picker"><div class="files-picker-path"></div><div class="files-picker-list" role="listbox" aria-label="Folders"></div></div><p class="files-picker-error form-error hidden" role="alert"></p><div class="dialog-actions"><button class="button secondary" type="button" data-close>Cancel</button><button class="button primary" type="button" data-choose>${escapeHTML(confirmLabel)}</button></div>`, (dialog, finish) => {
    let path = start;
    const pathNode = dialog.querySelector(".files-picker-path"), listNode = dialog.querySelector(".files-picker-list"), error = dialog.querySelector(".files-picker-error"), choose = dialog.querySelector("[data-choose]");
    async function show() {
      pathNode.innerHTML = `<button type="button" class="files-picker-up" data-up ${path ? "" : "disabled"} aria-label="Parent folder">${icon("up")}</button><span title="${escapeHTML(display(path))}">${escapeHTML(display(path))}</span>`;
      listNode.innerHTML = `<p class="files-picker-empty">Loading…</p>`;
      try {
        const result = await list(path);
        const folders = result.entries.filter(item => item.type === "directory" || (item.type === "symlink" && item.linkType === "directory"));
        // The Windows top level lists drives, which cannot hold items.
        choose.disabled = !result.path && !result.top;
        listNode.innerHTML = folders.length ? folders.map(item => `<button type="button" class="files-picker-folder" data-folder="${escapeHTML(item.name)}">${icon("folder")}<span>${escapeHTML(item.name)}</span></button>`).join("") : `<p class="files-picker-empty">No subfolders. Choose “${escapeHTML(confirmLabel)}” to use this folder.</p>`;
        error.classList.add("hidden");
      } catch (cause) { listNode.innerHTML = ""; error.textContent = cause.message; error.classList.remove("hidden"); }
    }
    dialog.addEventListener("click", event => {
      const target = event.target.closest("button");
      if (!target) return;
      if (target.dataset.folder) { path = joinPath(path, target.dataset.folder); void show(); }
      else if (target.dataset.up !== undefined) { path = parentPath(path); void show(); }
      else if (target.dataset.choose !== undefined && !target.disabled) finish(path);
    });
    void show();
  });
}
