import test from "node:test";
import assert from "node:assert/strict";
import {activate} from "./plugin.js";

test("Tasks keeps its page navigation without registering an Overview card", async () => {
 let navigation, off=0;
 const api={ui:{escape:String,toast(){}},overview:{register(){throw new Error("Tasks should not register an Overview card");}},navigation:{register(value){navigation=value;},open(){}},ws:{call:async()=>({tasks:[]}),on:()=>()=>{off++;}}};
 const deactivate=activate(api);
 assert.equal(navigation.id,"tasks");
 deactivate();
 assert.equal(off,4);
});
