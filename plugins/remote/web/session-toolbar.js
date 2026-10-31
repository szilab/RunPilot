// Shared toolbar for every Remote session type (RDP, VNC, Xpra). It relabels
// the host session view's controls as icons and adds the canvas-view toggle.

const ICONS = {
  back: '<path d="M19 12H5M12 19l-7-7 7-7"/>',
  fullscreen: '<path d="M8 3H5a2 2 0 0 0-2 2v3M16 3h3a2 2 0 0 1 2 2v3M21 16v3a2 2 0 0 1-2 2h-3M8 21H5a2 2 0 0 1-2-2v-3"/>',
  "page-view": '<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4"/>',
  disconnect: '<path d="m18 6-12 12M6 6l12 12"/>',
  diagnose: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>',
  copy: '<rect x="8" y="8" width="12" height="13" rx="2"/><path d="M16 8V5a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h2"/>',
  paste: '<rect x="5" y="4" width="14" height="17" rx="2"/><path d="M9 4.5h6v3H9zM8 12h8M8 16h8"/>',
};

export function labelIcon(element, name, label) {
  element.classList.add("icon", "rdp-toolbar-icon", `rdp-toolbar-${name}`);
  element.setAttribute("aria-label", label); element.title = label;
  const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  icon.setAttribute("viewBox", "0 0 24 24"); icon.setAttribute("fill", "none");
  icon.setAttribute("stroke", "currentColor"); icon.setAttribute("stroke-width", "1.8");
  icon.setAttribute("stroke-linecap", "round"); icon.setAttribute("stroke-linejoin", "round");
  icon.setAttribute("aria-hidden", "true"); icon.setAttribute("focusable", "false");
  icon.innerHTML = ICONS[name] || "";
  element.replaceChildren(icon);
}

export function toolbarButton(name, label, onClick) {
  const button = document.createElement("button");
  button.type = "button"; button.className = "button secondary small";
  labelIcon(button, name, label);
  button.addEventListener("click", onClick);
  return button;
}

// setupSessionToolbar arranges: back, canvas view, fullscreen, extras, disconnect.
export function setupSessionToolbar(view, { backLabel, extras = [], onPageViewChange = () => {} } = {}) {
  view.element.classList.add("rdp-interactive-view");
  const backButton = view.element.querySelector(".rp-interactive-heading > button");
  const [fullscreenButton, disconnectButton] = view.actions.querySelectorAll("button");
  labelIcon(backButton, "back", backLabel);
  labelIcon(fullscreenButton, "fullscreen", "Toggle fullscreen");
  labelIcon(disconnectButton, "disconnect", "Disconnect");
  view.surface.onFullscreenChange(active => labelIcon(fullscreenButton, "fullscreen", active ? "Exit fullscreen" : "Enter fullscreen"));
  let pageView = false;
  const pageViewButton = toolbarButton("page-view", "Use RunPilot canvas view", () => {
    pageView = !pageView;
    pageViewButton.setAttribute("aria-pressed", String(pageView));
    labelIcon(pageViewButton, "page-view", pageView ? "Exit RunPilot canvas view" : "Use RunPilot canvas view");
    view.setPageView(pageView);
    onPageViewChange(pageView);
  });
  pageViewButton.setAttribute("aria-pressed", "false");
  view.actions.replaceChildren(backButton, pageViewButton, fullscreenButton, ...extras, disconnectButton);
  return { backButton, pageViewButton, fullscreenButton, disconnectButton, get pageView() { return pageView; } };
}
