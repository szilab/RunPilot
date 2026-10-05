export function activate(runpilot) {
  let status = null;
  const load = async () => {
    const reply = await runpilot.ws.call("system", "status.get", {});
    status = reply?.result || reply;
    renderPage();
  };
  const fmtBytes = value => { value=Number(value)||0; const units=["B","KB","MB","GB","TB"]; let unit=0; while(value>=1024&&unit<units.length-1){value/=1024;unit++;} return `${value.toFixed(unit?1:0)} ${units[unit]}`; };
  const percent = (part,total) => total > 0 ? Math.max(0,Math.min(100,part*100/total)) : 0;
  const card = (title, value, detail, tone="blue") => runpilot.ui.Card({title, body:`<strong>${runpilot.ui.escape(value)}</strong><span>${runpilot.ui.escape(detail)}</span>`, className:`system-card ${tone}`});
  const host = () => status || {};
  const renderPage = () => {
    const root=document.getElementById("systemPluginPage"); if(!root) return;
    const value=host(), used=Math.max(0,(value.memoryTotalBytes||0)-(value.memoryFreeBytes||0));
    root.replaceChildren(runpilot.ui.Card({title:"Host", body:`<div class="system-host"><strong>${runpilot.ui.escape(value.hostname||"Unknown host")}</strong><span>${runpilot.ui.escape(`${value.os||""} ${value.architecture||""}`.trim())}</span></div>`}), runpilot.ui.Card({title:"Memory", body:`<strong>${fmtBytes(used)} / ${fmtBytes(value.memoryTotalBytes)}</strong><span>${percent(used,value.memoryTotalBytes).toFixed(0)}% used</span>`}));
  };
  runpilot.navigation.register({id:"system", title:"System", icon:"▣", render: root => { root.id="systemPluginPage"; renderPage(); }});
  runpilot.overview.register({id:"system-cpu", render: () => card("CPU", `${(host().cpuPercent||0).toFixed(1)}%`, "current usage")});
  runpilot.overview.register({id:"system-memory", render: () => { const value=host(), used=Math.max(0,(value.memoryTotalBytes||0)-(value.memoryFreeBytes||0)); return card("Memory", `${fmtBytes(used)} / ${fmtBytes(value.memoryTotalBytes)}`, `${percent(used,value.memoryTotalBytes).toFixed(0)}% used`, "violet"); }});
  runpilot.overview.register({id:"system-disks", render: () => { const disks=host().disks||[]; const root=document.createElement("section"); root.className="overview-section"; root.append(runpilot.ui.SectionTitle("Disk usage")); const list=document.createElement("div"); list.className="system-disks"; list.innerHTML=disks.map(d=>`<div><strong>${runpilot.ui.escape(d.path)}</strong><span>${fmtBytes(d.freeBytes)} free / ${fmtBytes(d.totalBytes)}</span></div>`).join("") || "<span>No disk metrics available.</span>"; root.append(list); return root; }});
  const unsubscribe=runpilot.ws.on("system","status.changed", event=>{status=event?.result||event; renderPage(); runpilot.overview.render();});
  load().catch(error=>runpilot.ui.toast(error.message,"error"));
  return () => unsubscribe();
}
