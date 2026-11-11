import test from "node:test";
import assert from "node:assert/strict";
import { activate, browseFromNative, jobFromForm, nativeFromBrowse, relativeTime, repositoryFromForm, runState, scheduleText, splitArgs, toolHint } from "./plugin.js";

test("form values become a restic job", () => {
  const job = jobFromForm({ name: "Photos", engine: "restic", enabled: true, timeoutMinutes: "90", scheduleType: "daily", time: "03:00", timeZone: " Europe/Berlin ",
    repositoryId: "repo-1", sources: "/srv/photos\n\n /srv/home \n", excludes: "*.tmp", tags: "photos, offsite",
    keepDaily: "7", keepMonthly: "", prune: true });
  assert.equal(job.timeoutSeconds, 5400);
  assert.deepEqual(job.schedule, { type: "daily", timeOfDay: "03:00", timeZone: "Europe/Berlin" });
  assert.equal(job.restic.repositoryId, "repo-1");
  assert.equal(job.restic.repository, undefined);
  assert.equal(job.restic.passwordFile, undefined);
  assert.deepEqual(job.restic.sources, ["/srv/photos", "/srv/home"]);
  assert.deepEqual(job.restic.tags, ["photos", "offsite"]);
  assert.equal(job.restic.retention.keepDaily, 7);
  assert.equal(jobFromForm({ name: "x", engine: "restic", scheduleType: "manual" }).schedule, undefined);
  assert.equal(jobFromForm({ name: "x", engine: "restic", scheduleType: "manual" }).restic.retention, undefined);
  assert.throws(() => jobFromForm({ name: "x", engine: "restic", scheduleType: "interval", intervalValue: "0" }));
});

test("form values become a repository", () => {
  assert.deepEqual(repositoryFromForm({ id: "repo-2", name: "Offsite", location: "sftp:u@h:/r", passwordFile: "/etc/restic/password" }), { id: "repo-2", name: "Offsite", location: "sftp:u@h:/r", passwordFile: "/etc/restic/password", environmentFile: "", executable: "" });
  assert.equal(repositoryFromForm({ name: "x", location: "/r" }).id, undefined);
});

test("form values become a mirror job", () => {
  const job = jobFromForm({ name: "Docs", engine: "robocopy", scheduleType: "interval", intervalValue: "6", intervalUnit: "hours", source: "C:\\Users", destination: "\\\\server\\backup", mode: "mirror", excludes: "AppData", excludeFiles: "*.tmp", retries: "2", retryWait: "5", additionalArgs: '/MT:8 "/LOG:C:\\Logs\\a b.txt"' });
  assert.equal(job.schedule.intervalSeconds, 21600);
  assert.equal(job.mirror.delete, true);
  assert.deepEqual(job.mirror.additionalArgs, ["/MT:8", "/LOG:C:\\Logs\\a b.txt"]);
  assert.deepEqual(splitArgs("--bwlimit=1m 'a b'"), ["--bwlimit=1m", "a b"]);
});

test("picker paths map to native host paths", () => {
  assert.equal(nativeFromBrowse("srv/data", "linux"), "/srv/data");
  assert.equal(nativeFromBrowse("", "linux"), "/");
  assert.equal(nativeFromBrowse("C:", "windows"), "C:\\");
  assert.equal(nativeFromBrowse("C:/Users/me", "windows"), "C:\\Users\\me");
  assert.equal(browseFromNative("/srv//data/", "linux"), "srv/data");
  assert.equal(browseFromNative("relative", "linux"), "");
  assert.equal(browseFromNative("C:\\Users\\me\\", "windows"), "C:/Users/me");
  assert.equal(browseFromNative("\\\\server\\share", "windows"), "");
});

test("labels describe schedules, runs and missing tools", () => {
  assert.equal(scheduleText(null), "Manual only");
  assert.equal(scheduleText({ type: "interval", intervalSeconds: 7200 }), "Every 2 h");
  assert.equal(runState({ finishedAt: "x", success: null }), "skipped");
  assert.equal(runState({}), "running");
  const now = Date.parse("2026-10-31T12:00:00Z");
  assert.equal(relativeTime("2026-10-31T14:00:00Z", now), "in 2 h");
  assert.equal(relativeTime("2026-10-31T11:50:00Z", now), "10 min ago");
  assert.match(toolHint({ engine: "restic", available: false, message: "restic is not installed on this host" }, "linux"), /RUNPILOT_EXTRA_PACKAGES="restic"/);
  assert.equal(toolHint({ engine: "restic", available: true }, "linux"), "");
});

test("the page lists jobs and escapes names", async () => {
  const listeners = [];
  const node = () => ({ innerHTML: "", isConnected: true, addEventListener(type, fn) { listeners.push([type, fn]); }, querySelector() { return null; }, closest() { return { classList: { contains: () => true } }; } });
  let navigation, off = 0;
  const calls = [];
  const api = {
    ui: { toast() {}, confirm: async () => true },
    navigation: { register(value) { navigation = value; } },
    ws: {
      call: async (plugin, method, params) => {
        calls.push([plugin, method, params]);
        return { os: "linux", tools: [{ engine: "restic", available: false, message: "restic is not installed on this host" }, { engine: "rsync", available: true }],
          repositories: [{ repository: { id: "repo-1", name: "<i>Offsite</i>", location: "/repo", passwordFile: "/p" }, jobs: 1, status: { state: "idle" } }],
          jobs: [{ job: { id: "job-1", name: "<b>Photos</b>", engine: "restic", enabled: true, schedule: { type: "daily", timeOfDay: "03:00" }, restic: { repositoryId: "repo-1", sources: ["/a"] } }, status: { state: "failure", lastRunAt: "2026-10-31T10:00:00Z", lastSuccess: false, lastMessage: "backup failed with exit code 1", nextRunAt: "2026-11-01T03:00:00Z" } }] };
      },
      on: () => () => { off++; },
    },
  };
  const deactivate = activate(api);
  assert.equal(navigation.id, "backup");
  const page = node(), header = node();
  navigation.headerActions(header);
  navigation.render(page);
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.match(page.innerHTML, /&lt;b&gt;Photos&lt;\/b&gt;/);
  assert.doesNotMatch(page.innerHTML, /<b>Photos/);
  assert.match(page.innerHTML, /Restic is not available/);
  assert.doesNotMatch(page.innerHTML, /data-action="snapshots"/);
  assert.match(page.innerHTML, /data-tab="repositories"/);
  assert.match(page.innerHTML, /&lt;i&gt;Offsite&lt;\/i&gt;/); // the backup shows its repository by name
  assert.match(header.innerHTML, /Add backup/);
  // The Repositories tab lists the repository with its own operations.
  const tab = listeners.find(([type]) => type === "click")[1];
  tab({ target: { closest: selector => selector === "[data-tab]" ? { dataset: { tab: "repositories" } } : null } });
  assert.match(page.innerHTML, /data-repository="repo-1"/);
  assert.match(page.innerHTML, /data-action="repo-init"/);
  assert.match(page.innerHTML, /data-action="repo-snapshots"/);
  assert.match(page.innerHTML, /1 backup/);
  assert.match(header.innerHTML, /Add repository/);
  assert.ok(calls.every(([plugin]) => plugin === "backup"));
  deactivate();
  assert.equal(off, 5);
});

test("arguments keep Windows paths and round-trip", async () => {
  const { formatArgs } = await import("./backup-helpers.js");
  for (const args of [["/MT:8", "C:\\Logs\\a b.txt", "\\\\server\\share"], ['say "hi"', "it's", ""]]) assert.deepEqual(splitArgs(formatArgs(args)), args);
});
