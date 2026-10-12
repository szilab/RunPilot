(function (global) {
  const element = (tag, className = "", value = "") => {
    const node = document.createElement(tag); node.className = className; node.textContent = String(value); return node;
  };
  const fraction = (used, total) => total > 0 ? Math.max(0, Math.min(100, used * 100 / total)) : 0;
  const utilization = value => Math.max(0, Math.min(100, Number(value) || 0));
  const percentage = value => `${Math.round(value)}%`;
  const size = value => {
    if (!Number.isFinite(value) || value < 0) return "—";
    const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"]; let index = 0;
    while (value >= 1024 && index < units.length - 1) { value /= 1024; index++; }
    return `${value.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
  };
  const uptime = value => {
    const seconds = Number(value); if (!Number.isFinite(seconds) || seconds <= 0) return "Unavailable";
    const days = Math.floor(seconds / 86400), hours = Math.floor(seconds % 86400 / 3600), minutes = Math.floor(seconds % 3600 / 60);
    return days ? `${days}d ${hours}h` : hours ? `${hours}h ${minutes}m` : `${Math.max(1, minutes)}m`;
  };
  const historyLimit = 32;
  function historyStorage() {
    try { return global.localStorage; } catch { return null; }
  }
  function historyStorageKey() {
    try { return `runpilot.hostMetrics.v1:${new URL("./", document.baseURI).pathname}`; } catch { return null; }
  }
  const paths = {
    cpu:'<rect x="6" y="6" width="12" height="12" rx="2"/><rect x="9" y="9" width="6" height="6" rx="1"/><path d="M9 3v3m6-3v3M9 18v3m6-3v3M3 9h3m-3 6h3m12-6h3m-3 6h3"/>',
    memory:'<rect x="3" y="6" width="18" height="12" rx="2"/><path d="M7 9v6m5-6v6m5-6v6M6 18v3m4-3v3m4-3v3m4-3v3"/>',
    storage:'<rect x="4" y="4" width="16" height="16" rx="3"/><path d="M4 14h16m-11 3h.01M9 4v7h6V4"/>',
    gpu:'<rect x="3" y="5" width="18" height="13" rx="2"/><circle cx="10" cy="11.5" r="3.5"/><path d="M17 8v7M6 18v3m10-3v3"/>',
    plugins:'<path d="M9 3h6v5a3 3 0 1 1 0 6v7H9v-5a3 3 0 1 1-6 0v-5h6V3Z"/>',
    host:'<rect x="4" y="3" width="16" height="7" rx="2"/><rect x="4" y="14" width="16" height="7" rx="2"/><path d="M8 6.5h.01M8 17.5h.01m4-11h5m-5 11h5"/>',
    check:'<path d="m5 12 4 4L19 6"/>',
    settings:'<circle cx="12" cy="12" r="3"/><path d="m9 3-1 3-3 1-2 4 2 2 1 4 4 3 3-1 3 1 4-3 1-4 2-2-2-4-3-1-1-3H9Z"/>',
    arrow:'<path d="M5 12h14m-5-5 5 5-5 5"/>',
    activity:'<path d="M3 12h4l3-8 4 16 3-8h4"/>',
    restart:'<path d="M4 9a8 8 0 1 1 0 6m0-12v6h6"/>',
  };
  function icon(name) { return `<svg viewBox="0 0 24 24" aria-hidden="true">${paths[name] || paths.plugins}</svg>`; }
  function card(title, extra = "") { const node = element("article", `host-dashboard-card ${extra}`.trim()); node.append(element("h2", "host-dashboard-title", title)); return node; }
  function bar(label, value) {
    const track = element("div", "host-dashboard-bar"); track.setAttribute("role", "progressbar"); track.setAttribute("aria-label", label); track.setAttribute("aria-valuemin", "0"); track.setAttribute("aria-valuemax", "100"); track.setAttribute("aria-valuenow", String(Math.round(utilization(value))));
    const fill = element("span", "host-dashboard-bar-fill"); fill.style.width = `${utilization(value)}%`; track.append(fill); return track;
  }
  function readings(data) {
    const host = data?.host || {}, total = Number(host.memoryTotalBytes) || 0, free = Math.min(total, Math.max(0, Number(host.memoryFreeBytes) || 0));
    const disks = Array.isArray(host.disks) ? host.disks.filter(d => d && d.totalBytes > 0) : [];
    const primary = disks.find(d => d.path === "/") || disks[0];
    return {cpu:host.cpuAvailable !== false && Number.isFinite(host.cpuPercent) ? utilization(host.cpuPercent) : null, memory:total ? fraction(total-free,total) : null, gpu:host.gpuAvailable ? utilization(host.gpuPercent) : null, storage:primary ? fraction(Math.max(0,primary.totalBytes-primary.freeBytes),primary.totalBytes) : null, primary, disks, totalMemory:total, freeMemory:free};
  }
  function trend(values, title) {
    const node = element("div", "host-metric-trend"), valid = values.filter(Number.isFinite);
    node.setAttribute("role", "img");
    if (valid.length < 2) { node.setAttribute("aria-label", `Collecting recent ${title} readings`); node.append(element("span", "trend-pending", "Collecting samples…")); return node; }
    // Sparklines scale to the observed range; the adjacent meter keeps the full 0–100 scale.
    const low=Math.max(0,Math.min(...valid)-4), high=Math.min(100,Math.max(Math.max(...valid)+4,low+10));
    node.setAttribute("aria-label", `${title}: ${valid.length} recent samples, chart range ${percentage(low)} to ${percentage(high)}, latest ${percentage(valid[valid.length-1])}`);
    const points = valid.map((value, i) => `${(i*200/(valid.length-1)).toFixed(1)},${(32-(utilization(value)-low)*28/(high-low)).toFixed(1)}`).join(" ");
    node.innerHTML = `<svg viewBox="0 0 200 36" preserveAspectRatio="none" aria-hidden="true"><polygon class="trend-area" points="0,36 ${points} 200,36"/><polyline class="trend-line" points="${points}"/></svg>`;
    return node;
  }
  function metricCard(title, key, value, details, model, samples) {
    const node = element("article", "host-dashboard-card host-metric"); node.dataset.metric = key;
    const heading = element("div", "host-metric-heading"), glyph = element("span", "feature-icon"); glyph.innerHTML = icon(key); heading.append(element("h2", "host-dashboard-title", title), glyph);
    const reading = element("div", "host-metric-reading"); reading.setAttribute("role", "meter"); reading.setAttribute("aria-label", title); reading.setAttribute("aria-valuemin", "0"); reading.setAttribute("aria-valuemax", "100");
    if (value !== null) reading.setAttribute("aria-valuenow", String(Math.round(value))); else reading.setAttribute("aria-valuetext", "Unavailable");
    const circumference = 251.327, offset = value === null ? circumference : circumference*(1-utilization(value)/100);
    reading.innerHTML = `<svg class="host-metric-ring" viewBox="0 0 94 94" aria-hidden="true"><circle cx="47" cy="47" r="40"/><circle class="ring-value" cx="47" cy="47" r="40" stroke-dasharray="${circumference}" stroke-dashoffset="${offset.toFixed(2)}"/></svg>`;
    reading.append(element("strong", "host-dashboard-value", value === null ? "—" : percentage(value))); node.append(heading, reading);
    for (const line of details.filter(Boolean)) node.append(element("p", "host-dashboard-detail", line));
    if (model) { const text = element("p", "host-dashboard-model", model); text.title = model; node.append(text); }
    node.append(trend(samples.map(sample => sample[key]), title)); return node;
  }
  function renderStatus(root, data, {samples = [], onOpenSettings} = {}) {
    const focusKey = root.contains?.(document.activeElement) ? document.activeElement?.dataset?.dashboardControl : null;
    const host = data?.host || {}, values = readings(data);
    const identity = card("Host", "host-dashboard-identity");
    const device = element("img", "host-device"); device.src = "host-device.svg"; device.alt = "";
    const copy = element("div", "host-identity-copy"), heading = element("div", "host-identity-heading");
    heading.append(element("strong", "host-dashboard-name", host.hostname || "Unknown host"), element("span", "host-online", "Connected"));
    copy.append(heading, element("p", "host-dashboard-platform", [host.os, host.architecture].filter(Boolean).join(" · ") || "Platform unavailable"), element("p", "host-dashboard-uptime", `Uptime ${uptime(data?.hostUptimeSeconds)}`));
    identity.append(device, copy);
    if (onOpenSettings) { const manage = element("button", "button secondary small", "Host settings →"); manage.type="button"; manage.dataset.dashboardControl="settings"; manage.onclick=onOpenSettings; identity.append(manage); }
    const top = element("div", "host-dashboard-top");
    const load = Array.isArray(host.loadAverage) && host.loadAverage.length === 3 ? `Load ${host.loadAverage.map(value => Number(value).toFixed(2)).join(" / ")}` : "";
    top.append(metricCard("CPU", "cpu", values.cpu, [host.cpuCount > 0 ? `${host.cpuCount} logical CPUs` : "CPU metrics unavailable"], load || host.cpuModel || "", samples));
    top.append(metricCard("Memory", "memory", values.memory, [values.totalMemory ? `${size(values.totalMemory-values.freeMemory)} / ${size(values.totalMemory)}` : "Memory metrics unavailable"], values.totalMemory ? `${size(values.freeMemory)} available` : "", samples));
    const disk = values.primary;
    top.append(metricCard("Storage", "storage", values.storage, [disk ? `${size(Math.max(0,disk.totalBytes-disk.freeBytes))} / ${size(disk.totalBytes)}` : "Storage metrics unavailable"], disk ? disk.path : "", samples));
    if (host.gpuAvailable) {
      const used = Math.min(host.gpuMemoryTotalBytes || 0, Math.max(0, host.gpuMemoryUsedBytes || 0));
      const gpu = metricCard("GPU", "gpu", values.gpu, [host.gpuMemoryTotalBytes > 0 ? `${size(used)} / ${size(host.gpuMemoryTotalBytes)} video memory` : "Utilization"], host.gpuName || "", samples);
      top.append(gpu);
    }
    top.dataset.count = String(top.childElementCount);
    const storage = element("details", "host-dashboard-card host-dashboard-storage"), storageHeading = element("summary", "host-dashboard-section-head");
    storageHeading.dataset.dashboardControl="filesystems";
    storage.open = !!root.querySelector?.("details.host-dashboard-storage")?.open;
    storageHeading.append(element("h2", "host-dashboard-title", "Filesystems"), element("span", "host-dashboard-count", `${values.disks.length} filesystem${values.disks.length === 1 ? "" : "s"}`)); storage.append(storageHeading);
    if (values.disks.length) {
      const grid = element("div", "host-dashboard-disks");
      for (const disk of values.disks) {
        const total = Number(disk.totalBytes) || 0, free = Math.min(total, Math.max(0, Number(disk.freeBytes) || 0)), used = total-free;
        const item = element("article", "host-dashboard-disk"), headline = element("div", "host-dashboard-disk-head"), path = element("strong", "host-dashboard-disk-path", disk.path || "Filesystem"); path.title = disk.path || "Filesystem";
        headline.append(path, element("strong", "host-dashboard-disk-percent", percentage(fraction(used,total)))); item.append(headline, element("p", "host-dashboard-disk-size", `${size(used)} / ${size(total)}`), bar(disk.path || "Filesystem", fraction(used,total))); grid.append(item);
      }
      storage.append(grid);
    } else storage.append(element("p", "host-dashboard-empty", "No filesystem metrics available."));
    root.replaceChildren(identity, top, storage);
    if (["filesystems", "settings"].includes(focusKey)) root.querySelector?.(`[data-dashboard-control="${focusKey}"]`)?.focus({preventScroll:true});
  }
  class HostDashboard {
    constructor(root, api, {interval = 8000, setIntervalFn = (callback, delay) => setInterval(callback, delay), clearIntervalFn = id => clearInterval(id), storage = historyStorage(), storageKey = historyStorageKey(), nowFn = () => Date.now(), onUpdate, onOpenSettings} = {}) {
      this.root=root; this.api=api; this.interval=interval; this.setIntervalFn=setIntervalFn; this.clearIntervalFn=clearIntervalFn; this.onUpdate=onUpdate; this.onOpenSettings=onOpenSettings;
      this.storage=storage; this.storageKey=storageKey; this.nowFn=nowFn; this.sampleHost=null;
      this.active=false; this.running=false; this.timer=null; this.controller=null; this.busy=false; this.generation=0; this.samples=[]; this.lastData=null;
    }
    setActive(active) {
      if (this.active===active) return; this.active=active;
      if (!active) return;
      if (this.lastData) renderStatus(this.root,this.lastData,{samples:this.samples,onOpenSettings:this.onOpenSettings});
      else this.root.replaceChildren(element("p","host-dashboard-loading","Loading host metrics…"));
    }
    start() {
      if (this.running) return;
      this.running=true;
      void this.refresh();
      this.timer=this.setIntervalFn(()=>{if(!document.hidden)void this.refresh();},this.interval);
    }
    stop() {
      this.running=false; this.generation++; this.controller?.abort(); this.controller=null; this.busy=false;
      if(this.timer!==null)this.clearIntervalFn(this.timer);
      this.timer=null;
    }
    restoreSamples(hostname) {
      try {
        const saved=this.storageKey && JSON.parse(this.storage?.getItem(this.storageKey) || "null");
        if (saved?.version!==1 || saved.hostname!==hostname || !Array.isArray(saved.samples)) return [];
        const now=this.nowFn();
        return saved.samples.slice(-historyLimit).filter(sample => sample && Number.isFinite(sample.at) && sample.at>0 && sample.at<=now && ["cpu","memory","storage","gpu"].every(key => sample[key]===null || (Number.isFinite(sample[key]) && sample[key]>=0 && sample[key]<=100)))
          .sort((a,b)=>a.at-b.at).map(({at,cpu,memory,storage,gpu})=>({at,cpu,memory,storage,gpu}));
      } catch { return []; }
    }
    saveSamples() {
      // Browser storage can be unavailable or full; live collection must still work.
      try {
        if (this.storageKey) this.storage?.setItem(this.storageKey,JSON.stringify({version:1,hostname:this.sampleHost,samples:this.samples}));
      } catch {}
    }
    async refresh() {
      if(!this.running||this.busy)return;const generation=++this.generation;this.busy=true;this.controller=new AbortController();
      try {
        const data=await this.api("api/v1/dashboard/status",{signal:this.controller.signal});
        if(this.running&&generation===this.generation){
          const hostname=data.host?.hostname || "";
          if(this.sampleHost!==hostname){this.sampleHost=hostname;this.samples=this.restoreSamples(hostname);}
          const {cpu,memory,storage,gpu}=readings(data);
          this.samples.push({at:this.nowFn(),cpu,memory,storage,gpu});
          if(this.samples.length>historyLimit)this.samples.shift();
          this.saveSamples();this.lastData=data;
          if(this.active)renderStatus(this.root,data,{samples:this.samples,onOpenSettings:this.onOpenSettings});
          this.onUpdate?.(data);
        }
      } catch(error) {
        if(this.running&&generation===this.generation&&error?.name!=="AbortError"){
          if(this.active)this.root.replaceChildren(element("p","host-dashboard-error",`Host metrics unavailable: ${error.message||error}`));
          if(error?.message==="Unauthorized")this.stop();
        }
      }
      finally {if(generation===this.generation){this.busy=false;this.controller=null;}}
    }
  }
  global.RunPilotDashboard={HostDashboard,renderStatus,readings,icon,size,uptime};
  if(typeof module!=="undefined")module.exports=global.RunPilotDashboard;
})(typeof window==="undefined"?globalThis:window);
