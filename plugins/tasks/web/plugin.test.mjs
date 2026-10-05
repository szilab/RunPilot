import test from "node:test";
import assert from "node:assert/strict";
import {activate, taskOverviewSummary} from "./plugin.js";

test("Tasks Overview maps enabled, running, failures and recent runs", () => {
 const views = [
  {task:{id:"a",name:"A",type:"scheduled",enabled:true},status:{state:"running",runningCount:2,lastRunAt:"2026-01-02T00:00:00Z",lastSuccess:true}},
  {task:{id:"b",name:"B",type:"scheduled",enabled:false},status:{state:"failure",lastRunAt:"2026-01-03T00:00:00Z",lastSuccess:false}},
  {task:{id:"c",name:"C",type:"continuous"},status:{state:"running"}},
 ];
 const summary=taskOverviewSummary(views);
 assert.equal(summary.enabled,2);assert.equal(summary.running,3);assert.equal(summary.failed,1);
 assert.equal(summary.recent[0].task.id,"b");assert.equal(summary.scheduled[0].task.id,"a");
});

test("Tasks registers a widget and releases plugin listeners", async () => {
 let widget, navigation, off=0;
 const api={ui:{escape:String,toast(){}},overview:{register(value){widget=value;}},navigation:{register(value){navigation=value;},open(){}},ws:{call:async()=>({tasks:[]}),on:()=>()=>{off++;}}};
 const deactivate=activate(api);
 assert.equal(widget.id,"tasks.summary");assert.equal(navigation.id,"tasks");
 widget.dispose();deactivate();
 assert.equal(off,4);
});
