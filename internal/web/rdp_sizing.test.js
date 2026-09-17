const assert=require("node:assert/strict");
const test=require("node:test");
require("./static/rdp-sizing.js");

const sizing=globalThis.RunPilotRDPSize;

test("initial sizing measures only after layout frames and falls back only for zero dimensions",async()=>{
  const events=[];
  const measured=await sizing.measureAfterLayout(()=>{ events.push("frame"); return Promise.resolve(); },()=>{ events.push("measure"); return {width:1220.4,height:1037.6}; });
  assert.deepEqual(events,["frame","frame","measure"]);
  assert.deepEqual(measured,{width:1220.4,height:1037.6});
  assert.deepEqual(sizing.resolveInitialSize({width:1220.4,height:1037.6}),{width:1220,height:1038,fallback:false});
  assert.deepEqual(sizing.resolveInitialSize({width:0,height:0}),{width:640,height:480,fallback:true});
  assert.equal(sizing.transportParams("ticket",{width:1220,height:1038,dpi:120,timezone:"Europe/Budapest"}),"ticket=ticket&width=1220&height=1038&dpi=120&timezone=Europe%2FBudapest");
});

test("display-update sends only the final distinct settled size after connection",()=>{
  let surface={width:1220,height:1038}; const sent=[];
  const controller=sizing.createController({resizeMethod:"display-update",initialSize:surface,getSurfaceSize:()=>surface,sendRemoteSize:size=>sent.push(size)});
  controller.setConnected(true);
  assert.deepEqual(sent,[]);
  surface={width:1512,height:1204}; controller.surfaceChanged();
  surface={width:1512,height:1284}; controller.surfaceChanged();
  surface={width:2560,height:1440}; controller.surfaceChanged();
  controller.flushPendingResize();
  assert.deepEqual(sent,[{width:2560,height:1440}]);
  controller.surfaceChanged(); controller.flushPendingResize();
  assert.deepEqual(sent,[{width:2560,height:1440}]);
});

test("fixed mode never changes the remote resolution and local fitting preserves aspect ratio",()=>{
  let surface={width:2560,height:1440}; const sent=[];
  const controller=sizing.createController({resizeMethod:"fixed",initialSize:{width:1220,height:1038},getSurfaceSize:()=>surface,sendRemoteSize:size=>sent.push(size)});
  controller.setConnected(true); controller.surfaceChanged(); controller.flushPendingResize();
  assert.deepEqual(sent,[]);
  assert.equal(sizing.fitScale({width:1220,height:1038},{width:640,height:480}),1.90625);
});

test("the first non-zero display resize reconciles a changed surface once",()=>{
  let surface={width:1280,height:800}; const sent=[];
  const controller=sizing.createController({resizeMethod:"display-update",initialSize:{width:640,height:480},getSurfaceSize:()=>surface,sendRemoteSize:size=>sent.push(size)});
  controller.setConnected(true); controller.displayResized({width:640,height:480}); controller.flushPendingResize();
  controller.displayResized({width:640,height:480}); controller.flushPendingResize();
  assert.deepEqual(sent,[{width:1280,height:800}]);
});
