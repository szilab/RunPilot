(function (global) {
  const element = (tag, className = "", value = "") => {
    const node = document.createElement(tag);
    node.className = className;
    node.textContent = String(value);
    return node;
  };
  const fraction = (used, total) => total > 0 ? Math.max(0, Math.min(100, used * 100 / total)) : 0;
  const utilization = value => Math.max(0, Math.min(100, Number(value) || 0));
  const percentage = value => `${Math.round(value)}%`;
  const size = value => {
    if (!Number.isFinite(value) || value < 0) return "—";
    const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
    let index = 0;
    while (value >= 1024 && index < units.length - 1) { value /= 1024; index++; }
    return `${value.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
  };
  const uptime = value => {
    const seconds = Number(value);
    if (!Number.isFinite(seconds) || seconds <= 0) return "Unavailable";
    const days = Math.floor(seconds / 86400), hours = Math.floor(seconds % 86400 / 3600), minutes = Math.floor(seconds % 3600 / 60);
    return days ? `${days}d ${hours}h` : hours ? `${hours}h ${minutes}m` : `${Math.max(1, minutes)}m`;
  };
  function card(title, extra = "") {
    const node = element("article", `host-dashboard-card ${extra}`.trim());
    node.append(element("h2", "host-dashboard-title", title));
    return node;
  }
  function bar(label, value) {
    const track = element("div", "host-dashboard-bar");
    track.setAttribute("role", "progressbar");
    track.setAttribute("aria-label", label);
    track.setAttribute("aria-valuemin", "0");
    track.setAttribute("aria-valuemax", "100");
    track.setAttribute("aria-valuenow", String(Math.round(utilization(value))));
    const fill = element("span", "host-dashboard-bar-fill");
    fill.style.width = `${utilization(value)}%`;
    track.append(fill);
    return track;
  }
  function metricCard(title, value, details, subheading = "") {
    const node = card(title);
    if (subheading) node.append(element("p", "host-dashboard-subheading", subheading));
    node.append(element("strong", "host-dashboard-value", value));
    if (value !== "—") node.append(bar(title, Number(value.replace("%", ""))));
    for (const line of details.filter(Boolean)) node.append(element("p", "host-dashboard-detail", line));
    return node;
  }
  function renderStatus(root, data) {
    const host = data?.host || {};
    const top = element("div", "host-dashboard-top");
    const identity = card("Host", "host-dashboard-identity");
    identity.append(element("strong", "host-dashboard-name", host.hostname || "Unknown host"));
    identity.append(element("p", "host-dashboard-platform", [host.os, host.architecture].filter(Boolean).join(" · ") || "Platform unavailable"));
    identity.append(element("p", "host-dashboard-uptime", `Uptime ${uptime(data?.hostUptimeSeconds)}`));
    top.append(identity);

    const cpuAvailable = host.cpuAvailable !== false && Number.isFinite(host.cpuPercent);
    const load = Array.isArray(host.loadAverage) && host.loadAverage.length === 3
      ? `Load ${host.loadAverage.map(value => Number(value).toFixed(2)).join(" / ")}` : "";
    const cores = host.cpuCount > 0 ? `${host.cpuCount} logical CPUs` : "";
    const cpu = metricCard("CPU", cpuAvailable ? percentage(utilization(host.cpuPercent)) : "—", [cores, load]);
    if (host.cpuModel) cpu.append(element("p", "host-dashboard-model", host.cpuModel));
    top.append(cpu);

    const totalMemory = Number(host.memoryTotalBytes) || 0;
    const availableMemory = Math.min(totalMemory, Math.max(0, Number(host.memoryFreeBytes) || 0));
    const usedMemory = Math.max(0, totalMemory - availableMemory);
    const memory = metricCard("Memory", totalMemory ? percentage(fraction(usedMemory, totalMemory)) : "—",
      [totalMemory ? `${size(usedMemory)} / ${size(totalMemory)}` : "Memory metrics unavailable", totalMemory ? `${size(availableMemory)} available` : ""]);
    top.append(memory);

    if (host.gpuAvailable) {
      const gpu = metricCard("GPU", percentage(utilization(host.gpuPercent)), [], host.gpuName || "Utilization");
      if (host.gpuMemoryTotalBytes > 0) {
        const used = Math.min(host.gpuMemoryTotalBytes, Math.max(0, host.gpuMemoryUsedBytes || 0));
        const memoryLabel = element("div", "host-dashboard-gpu-memory");
        memoryLabel.append(element("span", "", "Video memory"), element("strong", "", percentage(fraction(used, host.gpuMemoryTotalBytes))));
        gpu.append(memoryLabel, bar("Video memory", fraction(used, host.gpuMemoryTotalBytes)));
        gpu.append(element("p", "host-dashboard-detail", `${size(used)} / ${size(host.gpuMemoryTotalBytes)}`));
      }
      top.append(gpu);
    }

    const storage = element("section", "host-dashboard-card host-dashboard-storage");
    const heading = element("div", "host-dashboard-section-head");
    heading.append(element("h2", "host-dashboard-title", "Storage"));
    const disks = Array.isArray(host.disks) ? host.disks.filter(disk => disk && disk.totalBytes > 0) : [];
    heading.append(element("span", "host-dashboard-count", `${disks.length} filesystem${disks.length === 1 ? "" : "s"}`));
    storage.append(heading);
    if (disks.length) {
      const grid = element("div", "host-dashboard-disks");
      for (const disk of disks) {
        const total = Number(disk.totalBytes) || 0;
        const free = Math.min(total, Math.max(0, Number(disk.freeBytes) || 0));
        const used = total - free;
        const item = element("article", "host-dashboard-disk");
        const headline = element("div", "host-dashboard-disk-head");
        const path = element("strong", "host-dashboard-disk-path", disk.path || "Filesystem");
        path.title = disk.path || "Filesystem";
        headline.append(path, element("strong", "host-dashboard-disk-percent", percentage(fraction(used, total))));
        item.append(headline, element("p", "host-dashboard-disk-size", `${size(used)} / ${size(total)}`), bar(disk.path || "Filesystem", fraction(used, total)));
        grid.append(item);
      }
      storage.append(grid);
    } else storage.append(element("p", "host-dashboard-empty", "No filesystem metrics available."));
    root.replaceChildren(top, storage);
  }

  class HostDashboard {
    constructor(root, api, {interval = 8000, setIntervalFn = setInterval, clearIntervalFn = clearInterval} = {}) {
      this.root = root; this.api = api; this.interval = interval;
      this.setIntervalFn = setIntervalFn; this.clearIntervalFn = clearIntervalFn;
      this.active = false; this.timer = null; this.controller = null; this.busy = false; this.generation = 0;
    }
    setActive(active) {
      if (this.active === active) return;
      this.active = active;
      if (!active) {
        this.generation++;
        this.controller?.abort(); this.controller = null; this.busy = false;
        if (this.timer !== null) this.clearIntervalFn(this.timer);
        this.timer = null;
        return;
      }
      if (!this.root.childElementCount) this.root.replaceChildren(element("p", "host-dashboard-loading", "Loading host metrics…"));
      void this.refresh();
      this.timer = this.setIntervalFn(() => { if (!document.hidden) void this.refresh(); }, this.interval);
    }
    async refresh() {
      if (!this.active || this.busy) return;
      const generation = ++this.generation;
      this.busy = true;
      this.controller = new AbortController();
      try {
        const data = await this.api("api/v1/dashboard/status", {signal:this.controller.signal});
        if (this.active && generation === this.generation) renderStatus(this.root, data);
      } catch (error) {
        if (this.active && generation === this.generation && error?.name !== "AbortError") {
          this.root.replaceChildren(element("p", "host-dashboard-error", `Host metrics unavailable: ${error.message || error}`));
        }
      } finally {
        if (generation === this.generation) { this.busy = false; this.controller = null; }
      }
    }
  }
  global.RunPilotDashboard = {HostDashboard, renderStatus};
  if (typeof module !== "undefined") module.exports = global.RunPilotDashboard;
})(typeof window === "undefined" ? globalThis : window);
