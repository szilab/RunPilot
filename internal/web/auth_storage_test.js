const assert = require("node:assert/strict"), fs = require("node:fs"), vm = require("node:vm");
const context={};vm.runInNewContext(fs.readFileSync("internal/web/static/auth-storage.js","utf8"),context);
const storage={removed:[],removeItem(key){this.removed.push(key)}};context.RunPilotAuthStorage.clearShared(storage);
assert.deepEqual(storage.removed,["runpilot.token"]);
assert.equal(context.RunPilotAuthStorage.migrate,undefined);assert.equal(context.RunPilotAuthStorage.save,undefined);
const app=fs.readFileSync("internal/web/static/app.js","utf8"),html=fs.readFileSync("internal/web/static/index.html","utf8");
assert.ok(app.includes("api/v1/auth/login"));assert.ok(app.includes("credentials:\"same-origin\""));assert.ok(!app.includes("Authorization\`, \"Bearer"));
assert.ok(!html.includes("tokenInput"));assert.ok(!app.includes("localStorage.setItem(\"runpilot.token\""));
