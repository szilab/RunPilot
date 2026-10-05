const assert = require("node:assert/strict");
const OverviewWidgets = require("./overview-widgets.js");
const dashboard = require("./dashboard.js");
class Element {
  constructor(tag) { this.tag = tag; this.children = []; this.dataset = {}; this.parent = null; this.textContent = ""; }
  append(...nodes) { for (const node of nodes) { node.remove(); this.children.push(node); node.parent = this; } }
  replaceChildren(...nodes) { for (const child of this.children) child.parent = null; this.children = []; this.append(...nodes); }
  remove() { if (this.parent) { this.parent.children = this.parent.children.filter(child => child !== this); this.parent = null; } }
  setAttribute() {}
}
global.document = {createElement: tag => new Element(tag)};
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
  assert.equal(dashboard.validURL("https://example.com","external"),true);
  for (const unsafe of ["javascript:alert(1)","data:text/html,x","file:///tmp/x","//example.com"]) assert.equal(dashboard.validURL(unsafe,"external"),false);
  assert.equal(dashboard.validURL("/p/app/","runpilot"),true);
  assert.equal(dashboard.validURL("https://example.com","runpilot"),false);
  console.log("Overview lifecycle and URL validation passed");
})().catch(error => {console.error(error);process.exitCode=1;});
