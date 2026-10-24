// Target reachability badges and Wake-on-LAN for RDP and VNC target lists.
const escapeHTML = value => String(value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;").replaceAll("'", "&#39;");

export const STATUS_LABELS = Object.freeze({
  online: "Online",
  "service-down": "Host up, service off",
  offline: "Offline",
  unknown: "Checking…",
});

const STATUS_HINTS = Object.freeze({
  online: "The remote desktop port accepts connections.",
  "service-down": "The computer answers, but the remote desktop port is closed.",
  offline: "No answer from the computer. It may be switched off or unreachable.",
  unknown: "Status has not been checked yet.",
});

export function statusKey(kind, id) { return `${kind}:${id}`; }

export function createTargetStatus(call, { interval = 30000, toast = () => {}, setIntervalFn = setInterval, clearIntervalFn = clearInterval, setTimeoutFn = setTimeout, isVisible = () => !globalThis.document?.hidden } = {}) {
  const statuses = new Map();
  let timer = null, busy = false, roots = new Set(), paused = false, error = "";

  function describe(kind, id) {
    const entry = statuses.get(statusKey(kind, id));
    const state = entry?.state || "unknown";
    let title = STATUS_HINTS[state] || STATUS_HINTS.unknown;
    if (state === "online" && Number.isFinite(entry.latencyMilliseconds)) title += ` (${entry.latencyMilliseconds} ms)`;
    if (error) title += ` Last check failed: ${error}`;
    return { state, label: STATUS_LABELS[state] || STATUS_LABELS.unknown, title };
  }

  function badge(kind, id) {
    const { state, label, title } = describe(kind, id);
    return `<span class="remote-target-status" data-status-key="${escapeHTML(statusKey(kind, id))}" data-state="${escapeHTML(state)}" title="${escapeHTML(title)}" role="status"><span class="remote-target-status-dot" aria-hidden="true"></span><span class="remote-target-status-label">${escapeHTML(label)}</span></span>`;
  }

  function apply() {
    for (const root of roots) {
      if (!root?.isConnected) { roots.delete(root); continue; }
      root.querySelectorAll("[data-status-key]").forEach(node => {
        const [kind, ...rest] = node.dataset.statusKey.split(":");
        const { state, label, title } = describe(kind, rest.join(":"));
        node.dataset.state = state; node.title = title;
        const text = node.querySelector(".remote-target-status-label"); if (text) text.textContent = label;
      });
    }
  }

  async function refresh() {
    if (busy || paused) return;
    busy = true;
    try {
      const result = await call("remote.targets.status");
      statuses.clear();
      for (const item of result?.statuses || []) statuses.set(statusKey(item.kind, item.id), item);
      error = "";
    } catch (cause) { error = cause?.message || "status check failed"; }
    finally { busy = false; }
    apply();
  }

  return {
    badge,
    describe,
    // observe registers a rendered list so later checks update its badges in place.
    observe(root) { if (root) roots.add(root); apply(); },
    start() {
      if (timer !== null) return;
      void refresh();
      timer = setIntervalFn(() => { if (isVisible()) void refresh(); }, interval);
    },
    stop() { if (timer !== null) clearIntervalFn(timer); timer = null; },
    pause(value) { paused = !!value; if (!paused) void refresh(); },
    refresh,
    async wake(kind, target) {
      try {
        const result = await call("remote.targets.wake", { kind, id: target.id });
        const origin = result.source === "configured" ? "" : " (detected)";
        toast(`Wake-on-LAN sent to ${target.name} · ${result.mac}${origin}`);
        // A waking computer needs time; check again shortly.
        for (const delay of [15000, 45000]) setTimeoutFn(() => void refresh(), delay);
        return result;
      } catch (cause) { toast(cause?.message || "Could not send Wake-on-LAN"); return null; }
    },
  };
}

// macField renders the optional Wake-on-LAN MAC address input for a target editor.
export function macField(target) {
  const detected = target?.discoveredMac ? `Detected: ${target.discoveredMac}` : "Detected automatically";
  return `<label class="remote-mac-field">MAC address for Wake-on-LAN<input name="macAddress" maxlength="17" spellcheck="false" autocomplete="off" placeholder="${escapeHTML(detected)}" value="${escapeHTML(target?.macAddress || "")}"></label><p class="meta remote-mac-help">Leave empty to use the address RunPilot detects while the computer is on and on the local network.</p>`;
}
