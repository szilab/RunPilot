import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import { fileURLToPath } from "node:url";

// Load the plugin as a data: module so this works on any Node version
// regardless of package "type" detection.
const source = fs.readFileSync(fileURLToPath(new URL("../../tasks/web/plugin.js", import.meta.url)), "utf8");
const plugin = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);
const { splitArgs, formatArgs, scheduleText, splitInterval, taskFromForm, activate } = plugin;

test("argument parsing round-trips quoting like the legacy editor", () => {
  assert.deepEqual(splitArgs(`--port 8080 "two words" 'single quoted' plain\\ escaped ""`), ["--port", "8080", "two words", "single quoted", "plain escaped", ""]);
  assert.deepEqual(splitArgs("   "), []);
  const args = ["a", "b c", 'say "hi"', "back\\slash", ""];
  assert.deepEqual(splitArgs(formatArgs(args)), args);
});

test("schedule text and interval units", () => {
  assert.equal(scheduleText({ type: "interval", intervalSeconds: 7200 }), "Every 2 h");
  assert.equal(scheduleText({ type: "interval", intervalSeconds: 1200 }), "Every 20 min");
  assert.equal(scheduleText({ type: "interval", intervalSeconds: 90 }), "Every 90 s");
  assert.equal(scheduleText({ type: "daily", timeOfDay: "03:00", timeZone: "UTC" }), "Daily 03:00 (UTC)");
  assert.equal(scheduleText({ type: "cron", cron: "* * * * *" }), "Cron * * * * *");
  assert.deepEqual(splitInterval(7200), [2, "hours"]);
  assert.deepEqual(splitInterval(120), [2, "minutes"]);
  assert.deepEqual(splitInterval(61), [61, "seconds"]);
});

const base = { name: "n", description: "", path: "/bin/x", args: "-a 'b c'", cwd: "", interpreter: "auto", env: [["A", "1"], ["", ""], [" B ", ""]] };

test("continuous form becomes a backend task", () => {
  const task = taskFromForm({ ...base, type: "continuous", id: "task-1", autostart: true, restartMode: "always", restartInitial: "3", restartMax: "", restartRetries: "5" });
  assert.deepEqual(task, {
    id: "task-1", name: "n", description: "", type: "continuous", autostart: true,
    command: { path: "/bin/x", args: ["-a", "b c"], workingDirectory: "", environment: { A: "1", B: "" }, interpreter: "" },
    restart: { mode: "always", initialDelaySeconds: 3, maxDelaySeconds: 0, maxRetries: 5 },
  });
});

test("scheduled form converts units and keeps the chosen interpreter", () => {
  const task = taskFromForm({ ...base, interpreter: "sh-inline", type: "scheduled", enabled: true, overlap: "allow", timeout: "30", scheduleType: "interval", intervalValue: "2", intervalUnit: "minutes", timeZone: "" });
  assert.equal(task.schedule.intervalSeconds, 120);
  assert.equal(task.command.interpreter, "sh-inline");
  assert.equal(task.timeoutSeconds, 30);
  assert.equal(task.overlapPolicy, "allow");
  assert.equal("id" in task, false);
  const daily = taskFromForm({ ...base, type: "scheduled", enabled: false, overlap: "skip", timeout: "", scheduleType: "daily", time: "03:15", timeZone: "Europe/Budapest" });
  assert.deepEqual(daily.schedule, { type: "daily", timeOfDay: "03:15", timeZone: "Europe/Budapest" });
  assert.equal(taskFromForm({ ...base, type: "scheduled", overlap: "skip", scheduleType: "cron", cron: "0 * * * *" }).schedule.cron, "0 * * * *");
});

test("invalid numbers are rejected before any request", () => {
  assert.throws(() => taskFromForm({ ...base, type: "scheduled", overlap: "skip", scheduleType: "interval", intervalValue: "0", intervalUnit: "seconds" }), /Interval/);
  assert.throws(() => taskFromForm({ ...base, type: "scheduled", overlap: "skip", scheduleType: "interval", intervalValue: "1.5", intervalUnit: "seconds" }), /whole number/);
  assert.throws(() => taskFromForm({ ...base, type: "continuous", restartMode: "never", restartRetries: "-1" }), /Maximum retries/);
});

function fakeRuntime(replies) {
  const registered = [], subscriptions = new Map(), calls = [], toasts = [];
  const runpilot = {
    ws: {
      call: async (pluginId, method, params) => { calls.push([pluginId, method, params]); const reply = replies[method]; return typeof reply === "function" ? reply(params) : reply; },
      on: (pluginId, event, listener) => { subscriptions.set(`${pluginId}:${event}`, listener); return () => subscriptions.delete(`${pluginId}:${event}`); },
    },
    navigation: { register: entry => registered.push(entry) },
    ui: { escape: value => String(value).replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]), toast: (message, level) => toasts.push([message, level]) },
  };
  return { runpilot, registered, subscriptions, calls, toasts };
}

const view = (id, name, type, status = {}) => ({ task: { id, name, type, command: { path: "/bin/x" }, restart: { mode: "on-failure" }, schedule: type === "scheduled" ? { type: "interval", intervalSeconds: 60 } : undefined, enabled: true }, status: { state: type === "scheduled" ? "idle" : "stopped", ...status } });

test("the page renders escaped task data, reacts to events and unsubscribes", async () => {
  const runtime = fakeRuntime({ "tasks.list": { tasks: [view("task-1", `<img src=x onerror=alert(1)>`, "continuous", { message: "<b>boom</b>" }), view("task-2", "Nightly", "scheduled")] } });
  const cleanup = activate(runtime.runpilot);
  assert.equal(runtime.registered.length, 1);
  assert.equal(runtime.registered[0].id, "tasks");
  assert.equal(runtime.registered[0].title, "Tasks");
  assert.deepEqual([...runtime.subscriptions.keys()].sort(), ["tasks:history.output", "tasks:tasks.changed", "tasks:tasks.run.completed", "tasks:tasks.status"]);
  const page = { isConnected: true, innerHTML: "", addEventListener() {} };
  runtime.registered[0].render(page);
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.ok(page.innerHTML.includes("Nightly"));
  assert.ok(page.innerHTML.includes("&lt;img src=x onerror=alert(1)&gt;"));
  assert.ok(!page.innerHTML.includes("<img src=x"));
  assert.ok(page.innerHTML.includes("&lt;b&gt;boom&lt;/b&gt;"));
  assert.match(page.innerHTML, /data-action="start"/);
  assert.match(page.innerHTML, /data-action="run"/);

  runtime.subscriptions.get("tasks:tasks.status")(view("task-1", "Renamed", "continuous", { state: "running", pid: 42 }));
  assert.ok(page.innerHTML.includes("Renamed") && page.innerHTML.includes(">42<") && page.innerHTML.includes('data-action="stop"'));
  runtime.subscriptions.get("tasks:tasks.run.completed")({ id: "task-9", name: "Job", success: false, message: "exit code 2" });
  runtime.subscriptions.get("tasks:tasks.run.completed")({ id: "task-9", name: "Job", success: false, message: "stopped" });
  assert.deepEqual(runtime.toasts, [["Job failed: exit code 2", "error"]]);
  cleanup();
  assert.equal(runtime.subscriptions.size, 0);
});

test("unreadable stored configuration disables editing and explains itself", async () => {
  const runtime = fakeRuntime({ "tasks.list": { tasks: [], loadError: "stored task configuration is unreadable: bad" } });
  activate(runtime.runpilot);
  const page = { isConnected: true, innerHTML: "", addEventListener() {} };
  runtime.registered[0].render(page);
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.match(page.innerHTML, /Task configuration is unavailable/);
  assert.match(page.innerHTML, /data-action="add" data-type="continuous" disabled/);
  assert.ok(!page.innerHTML.includes("No tasks yet"));
});

test("plugin-level errors surface as request failures", async () => {
  const runtime = fakeRuntime({ "tasks.list": { error: { code: "failed", message: "backend unavailable" } } });
  activate(runtime.runpilot);
  const page = { isConnected: true, innerHTML: "", addEventListener() {} };
  runtime.registered[0].render(page);
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.ok(page.innerHTML.includes("backend unavailable"));
});
