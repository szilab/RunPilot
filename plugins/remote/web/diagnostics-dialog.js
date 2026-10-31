// Credential-safe session log dialog shared by Remote session types.
export function openLogDialog({ title, stateText = "", text = "", onClose = () => {} }) {
  const dialog = document.createElement("dialog"); dialog.className = "dialog rdp-diagnostics-dialog";
  dialog.innerHTML = `<div class="dialog-head"><div><h2></h2><p class="rdp-diagnostics-state"></p></div><button type="button" class="icon-btn" data-close aria-label="Close">×</button></div><pre class="rdp-diagnostics-log"></pre><div class="dialog-actions"><button type="button" class="button secondary" data-close>Close</button></div>`;
  dialog.querySelector("h2").textContent = title;
  const stateNode = dialog.querySelector(".rdp-diagnostics-state"), output = dialog.querySelector(".rdp-diagnostics-log");
  stateNode.textContent = stateText; output.textContent = text;
  dialog.querySelectorAll("[data-close]").forEach(node => node.addEventListener("click", () => dialog.close()));
  dialog.addEventListener("close", () => { onClose(); dialog.remove(); }, { once: true });
  document.body.append(dialog); dialog.showModal();
  return { dialog, output, setState: value => { stateNode.textContent = value; } };
}
