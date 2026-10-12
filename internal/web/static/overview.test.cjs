const assert = require("node:assert/strict");
const OverviewWidgets = require("./overview-widgets.js");
const {HostDashboard, renderStatus} = require("./dashboard.js");
class Element {
  constructor(tag) { this.tag = tag; this.children = []; this.dataset = {}; this.style = {}; this.attributes = {}; this.parent = null; this.textContent = ""; this.className = ""; }
  append(...nodes) { for (const node of nodes) { node.remove(); this.children.push(node); node.parent = this; } }
  replaceChildren(...nodes) { for (const child of this.children) child.parent = null; this.children = []; this.append(...nodes); }
  remove() { if (this.parent) { this.parent.children = this.parent.children.filter(child => child !== this); this.parent = null; } }
  setAttribute(key, value) { this.attributes[key] = value; }
  get childElementCount() { return this.children.length; }
}
global.document = {hidden:false, createElement: tag => new Element(tag)};
const descendants = node => [node, ...node.children.flatMap(descendants)];
const fixture = {host:{hostname:"desk",os:"linux",architecture:"amd64",cpuAvailable:true,cpuCount:16,cpuPercent:12.4,loadAverage:[1.7,1.4,1.2],cpuModel:"Ryzen",memoryTotalBytes:32*1024**3,memoryFreeBytes:20*1024**3,gpuAvailable:true,gpuName:"RTX",gpuPercent:38,gpuMemoryUsedBytes:2*1024**3,gpuMemoryTotalBytes:8*1024**3,disks:[{path:"/",totalBytes:1000,freeBytes:250},{path:"/mnt/data",totalBytes:2000,freeBytes:1000}]},hostUptimeSeconds:10*86400+14*3600};
(async () => {
  const root = new Element("root"), widgets = new OverviewWidgets(root), events = [];
  widgets.register("good", {id:"good.summary",title:"Good",size:"small",order:20,mount(body){ events.push("good mount"); body.textContent="ok"; return () => events.push("good cleanup"); },refresh(){ events.push("good refresh"); },dispose(){events.push("good dispose");}});
  widgets.register("bad", {id:"bad.summary",title:"Bad",order:10,mount(){ throw new Error("broken"); }});
  widgets.register("good", {id:"good.summary",title:"Good",mount(){events.push("duplicate");}});
  const previous = console.error; console.error = () => {};
  widgets.setActive(true);
  await new Promise(resolve => setTimeout(resolve, 0));
  console.error = previous;
  assert.equal(root.children.length,2); assert.equal(root.children[0].dataset.size,"medium");
  assert.equal(events.filter(event=>event==="good mount").length,1);
  assert.equal(root.children[0].children[1].children[0].textContent,"broken");
  await widgets.refresh(); assert.ok(events.includes("good refresh"));
  widgets.unregisterOwner("good"); assert.equal(root.children.length,1); assert.ok(events.includes("good cleanup")); assert.ok(events.includes("good dispose"));
  widgets.setActive(false); assert.equal(root.children.length,0);

  // Navigation may leave and return while a plugin is still mounting or refreshing.
  let finishMount, finishRefresh, refreshCalls = 0;
  const pendingWidgets = new OverviewWidgets(new Element("root"));
  pendingWidgets.register("async", {id:"async.summary",title:"Async",mount:()=>new Promise(resolve=>{finishMount=resolve;}),refresh:()=>{refreshCalls++;return new Promise(resolve=>{finishRefresh=resolve;});}});
  pendingWidgets.setActive(true);
  await pendingWidgets.refresh(); assert.equal(refreshCalls,0,"refresh overlapped initial mount");
  finishMount(); await new Promise(resolve=>setTimeout(resolve,0));
  const oldRefresh = pendingWidgets.refresh();
  pendingWidgets.setActive(false); pendingWidgets.setActive(true);
  finishRefresh(); await oldRefresh;
  await pendingWidgets.refresh(); assert.equal(refreshCalls,1,"stale refresh unlocked a new mount");
  finishMount(); await new Promise(resolve=>setTimeout(resolve,0));
  pendingWidgets.setActive(false);

  const dashboardRoot = new Element("root");
  renderStatus(dashboardRoot, fixture);
  const all = descendants(dashboardRoot);
  assert.equal(all.filter(node => node.className.includes("host-dashboard-card")).length, 6);
  assert.equal(all.filter(node => node.className === "host-dashboard-disk").length, 2);
  assert.ok(all.some(node => node.textContent === "75%"));
  assert.ok(all.some(node => node.textContent === "desk"));
  assert.ok(all.some(node => node.textContent === "16 logical CPUs"));
  assert.ok(all.some(node => node.textContent.includes("Load 1.70 / 1.40 / 1.20")));
  assert.ok(all.some(node => node.textContent.includes("video memory")));
  assert.equal(all.filter(node => node.attributes.role === "meter").length, 4);
  assert.equal(all.filter(node => node.className === "host-dashboard-bar").length, 2);
  const noGPU = new Element("root");
  renderStatus(noGPU, {...fixture, host:{...fixture.host,gpuAvailable:false,disks:[]}});
  assert.equal(descendants(noGPU).filter(node => node.className.includes("host-dashboard-card")).length, 5);
  assert.ok(descendants(noGPU).some(node => node.textContent === "No filesystem metrics available."));

  let calls=0, interval, stopped=false, pluginMounted=0;
  const dormant = new OverviewWidgets(new Element("detached"));
  dormant.register("tasks",{id:"tasks.summary",title:"Tasks",mount(){pluginMounted++;}});
  const dashboard = new HostDashboard(new Element("root"), async()=>{calls++;return fixture;},{setIntervalFn:fn=>{interval=fn;return 1;},clearIntervalFn:()=>{stopped=true;}});
  dashboard.setActive(true); await new Promise(resolve=>setTimeout(resolve,0));
  assert.equal(calls,1);assert.equal(pluginMounted,0);
  interval(); await new Promise(resolve=>setTimeout(resolve,0));assert.equal(calls,2);
  for(let i=0;i<40;i++) await dashboard.refresh();
  assert.equal(dashboard.samples.length,32,"browser history must remain bounded");
  assert.ok(dashboard.samples.every(sample=>sample.cpu===12.4),"charts must use actual host samples");
  dashboard.setActive(false);interval();await new Promise(resolve=>setTimeout(resolve,0));
  assert.equal(calls,42);assert.equal(stopped,true);
  console.log("Overview host rendering, missing GPU, disks and refresh lifecycle passed");
})().catch(error => {console.error(error);process.exitCode=1;});
