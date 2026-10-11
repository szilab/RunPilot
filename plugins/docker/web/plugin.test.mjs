import test from "node:test";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {activate, dockerOverviewSummary} from "./plugin.js";

const source = readFileSync(new URL("./plugin.js", import.meta.url), "utf8");
test("Docker frontend uses plugin RPC and packaged terminal assets", () => {
  assert.doesNotMatch(source, /api\/v1\/docker|legacyDockerPage|legacy\/app\.js/);
  assert.match(source, /runpilot\.ws\.call\(PLUGIN, method, params\)/);
  assert.match(source, /new URL\("\.\/vendor\/", import\.meta\.url\)/);
  assert.match(source, /process\.session\.output/);
  assert.match(source, /docker\.containers\.terminal\.open/);
});

test("project and resource controls follow snapshot ownership and usage", async () => {
  let nav, click, calls = [];
  const subscriptions = [];
  const snapshot = {runtime:{available:true,state:"ready"},projects:[
    {name:"owned",managed:true,state:"running",composeFileExists:true,containers:[{id:"abcdef012345",name:"web",state:"running",tone:"green"}]},
    {name:"outside",readOnly:true,state:"running",containers:[{id:"fedcba987654",name:"external",state:"running",tone:"green"}]},
  ],volumes:[{name:"data",inUse:true,driver:"local",scope:"local"},{name:"free",inUse:false,driver:"local",scope:"local"}],networks:[{name:"bridge",inUse:false},{name:"compose_net",inUse:false,composeProject:"app"},{name:"custom",inUse:false}]};
  const runpilot = {overview:{register:()=>{}},ui:{escape:value=>String(value).replaceAll("&","&amp;").replaceAll('"',"&quot;").replaceAll("<","&lt;"),toast:()=>{}},navigation:{register:value=>{nav=value;}},ws:{call:async (plugin,method,params)=>{calls.push([plugin,method,params]);if(method==="docker.snapshot")return snapshot;return {ok:true};},on:(...args)=>{subscriptions.push(args);return()=>{};}}};
  const deactivate = await activate(runpilot);
  assert.equal(nav.id,"docker");assert.equal(nav.title,"Docker");assert.equal(subscriptions.length,3);
  const root={innerHTML:"",addEventListener:(name,fn)=>{if(name==="click")click=fn;},removeEventListener(){},contains:()=>true};
  nav.render(root);await new Promise(resolve=>setTimeout(resolve,0));
  assert.match(root.innerHTML,/External project · read only/);
  assert.match(root.innerHTML,/data-action="project-up" data-key="owned"/);
  assert.match(root.innerHTML,/data-action="project-up" data-key="outside"[^>]*disabled/);
  assert.match(root.innerHTML,/External project · read only/);
  assert.match(root.innerHTML,/aria-label="Stop container"/);
  assert.match(root.innerHTML,/aria-label="View logs" data-action="logs" data-key="fedcba987654"/);
  assert.match(root.innerHTML,/docker-plugin-projects rp-masonry/);
  assert.match(root.innerHTML,/data-action="volumes-delete" data-key="data"[^>]*disabled/);
  assert.match(root.innerHTML,/data-action="networks-delete" data-key="bridge"[^>]*disabled/);
  assert.match(root.innerHTML,/data-action="networks-delete" data-key="compose_net"[^>]*disabled/);
  const freeDelete = root.innerHTML.match(/<button[^>]*data-action="volumes-delete" data-key="free"[^>]*>/)?.[0];
  assert.ok(freeDelete);
  assert.doesNotMatch(freeDelete,/disabled/);
  assert.match(root.innerHTML,/Resource is in use by a container/);
  assert.match(readFileSync(new URL("./plugin.css", import.meta.url), "utf8"),/\.docker-plugin-resource-delete:disabled\s*\{[^}]*opacity:/);
  click({target:{closest:()=>({dataset:{action:"project-up",key:"owned"}})}});
  await new Promise(resolve=>setTimeout(resolve,0));
  assert.ok(calls.some(([,method,params])=>method==="docker.projects.action"&&params.name==="owned"&&params.action==="up"));
  deactivate();
});

test("Docker Overview maps container state and health", () => {
  const summary=dockerOverviewSummary({projects:[{containers:[{state:"running",health:"healthy"},{state:"running",health:"unhealthy",name:"bad"},{state:"exited"}]}]});
  assert.equal(summary.total,3);assert.equal(summary.running,2);assert.equal(summary.stopped,1);assert.equal(summary.unhealthy,1);assert.equal(summary.problems[0].name,"bad");
});

async function renderRuntime(runtime) {
  let nav;
  const runpilot = {
    overview:{register:()=>{}},
    ui:{escape:value=>String(value).replaceAll("&","&amp;").replaceAll('"',"&quot;").replaceAll("<","&lt;"),toast:()=>{}},
    navigation:{register:value=>{nav=value;}},
    ws:{call:async ()=>({runtime,projects:[],volumes:[],networks:[]}),on:()=>()=>{}},
  };
  const deactivate=await activate(runpilot);
  const root={innerHTML:"",addEventListener(){},removeEventListener(){}};
  nav.render(root);
  await new Promise(resolve=>setTimeout(resolve,0));
  deactivate();
  return root.innerHTML;
}

test("ASUSTOR socket permission error links to the targeted guidance", async () => {
  const html=await renderRuntime({available:false,state:"permission-denied",deployment:"asustor-appcentral",message:"Docker Engine access was denied to the RunPilot service account (permission denied)."});
  assert.match(html,/permission denied/);
  assert.match(html,/href="https:\/\/github\.com\/szilab\/RunPilot\/blob\/feature\/asustor-appcentral\/docs\/platforms\/asustor-docker-permissions\.md"/);
  assert.match(html,/target="_blank" rel="noopener noreferrer">ASUSTOR Docker permission guidance/);
});

test("ASUSTOR guidance stays hidden for other platforms and non-permission errors", async () => {
  for (const runtime of [
    {available:false,state:"permission-denied",deployment:"linux-native",message:"permission denied"},
    {available:false,state:"cli-permission-denied",deployment:"asustor-appcentral",message:"Docker CLI permission denied"},
    {available:false,state:"daemon-unavailable",deployment:"asustor-appcentral",message:"Docker daemon is unavailable"},
    {available:false,state:"socket-missing",deployment:"asustor-appcentral",message:"Docker socket is missing"},
  ]) {
    const html=await renderRuntime(runtime);
    assert.doesNotMatch(html,/asustor-docker-permissions\.md/);
  }
});
