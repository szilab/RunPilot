package plugins

import (
	"archive/zip"
	_ "embed"
	"os"
	"path/filepath"
)

// EnsureReferenceSystem installs the bundled first-party reference package by
// using the same archive validation and atomic installation path as any other
// plugin. It is distribution convenience only; its runtime is never special.
func EnsureReferenceSystem(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "system", "1.0.0", "plugin.yaml")); err == nil {
		return nil
	}
	file, err := os.CreateTemp(root, ".system-reference-*.rpplugin")
	if err != nil {
		return err
	}
	archivePath := file.Name()
	defer os.Remove(archivePath)
	writer := zip.NewWriter(file)
	assets := map[string]string{
		"plugin.yaml":    "apiVersion: runpilot.plugin/v1\nid: system\nname: System\ndescription: Host status and resource usage\nversion: 1.0.0\nrequires:\n  runpilotApi: 1\nplatforms: [linux, windows]\nbackend:\n  module: backend/plugin.wasm\nfrontend:\n  module: web/plugin.js\n  stylesheet: web/plugin.css\n",
		"web/plugin.js":  `export function activate(runpilot){let status={};const load=async()=>{const reply=await runpilot.ws.call("system","status.get",{});status=reply?.result||reply;render();};const bytes=v=>{v=Number(v)||0;const u=["B","KB","MB","GB","TB"];let i=0;while(v>=1024&&i<4){v/=1024;i++;}return v.toFixed(i?1:0)+" "+u[i];};const render=()=>{const root=document.getElementById("systemPluginPage");if(!root)return;const used=Math.max(0,(status.memoryTotalBytes||0)-(status.memoryFreeBytes||0));root.replaceChildren(runpilot.ui.Card({title:"Host",body:"<strong>"+runpilot.ui.escape(status.hostname||"Unknown host")+"</strong><span>"+runpilot.ui.escape(status.os||"")+"</span>"}),runpilot.ui.Card({title:"Memory",body:"<strong>"+bytes(used)+" / "+bytes(status.memoryTotalBytes)+"</strong>"}));};runpilot.navigation.register({id:"system",title:"System",icon:"▣",render:root=>{root.id="systemPluginPage";render();}});runpilot.overview.register({id:"system-cpu",render:()=>runpilot.ui.Card({title:"CPU",body:"<strong>"+Number(status.cpuPercent||0).toFixed(1)+"%</strong><span>current usage</span>"})});runpilot.overview.register({id:"system-memory",render:()=>{const used=Math.max(0,(status.memoryTotalBytes||0)-(status.memoryFreeBytes||0));return runpilot.ui.Card({title:"Memory",body:"<strong>"+bytes(used)+" / "+bytes(status.memoryTotalBytes)+"</strong>"});}});const stop=runpilot.ws.on("system","status.changed",value=>{status=value;render();runpilot.overview.render();});load().catch(error=>runpilot.ui.toast(error.message,"error"));return()=>stop();}`,
		"web/plugin.css": `.system-card{display:grid;gap:var(--rp-space-xs)}.system-card strong{color:var(--rp-text-strong);font-size:18px}.system-card span{color:var(--rp-text-muted);font-size:12px}`,
	}
	wasm := referenceSystemWASM
	for name, content := range assets {
		entry, err := writer.Create(name)
		if err != nil {
			return err
		}
		if _, err = entry.Write([]byte(content)); err != nil {
			return err
		}
	}
	entry, err := writer.Create("backend/plugin.wasm")
	if err != nil {
		return err
	}
	if _, err = entry.Write(wasm); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	_, err = InstallPackage(root, archivePath, "")
	return err
}

// referenceSystemWASM is built from plugins/system/backend, checked in so end-user
// installs never need TinyGo.  Build/release verifies this generated artifact.
//
//go:embed system_reference.wasm
var referenceSystemWASM []byte
