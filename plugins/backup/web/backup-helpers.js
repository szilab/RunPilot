// Pure helpers for the Backup frontend. They have no DOM or RunPilot
// dependencies so they can be unit tested directly.

export const ENGINE_LABELS = { restic: "Restic", rsync: "rsync", robocopy: "Robocopy" };
export const KIND_LABELS = { backup: "Backup", restore: "Restore", check: "Check", unlock: "Unlock", init: "Initialize" };
const UNIT_SECONDS = { minutes: 60, hours: 3600 };

export const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, character => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[character]));

export function lines(text) {
  return String(text || "").split(/\r?\n/).map(line => line.trim()).filter(Boolean);
}

// splitArgs parses an argument string. Quotes group words; a backslash is
// an ordinary character so Windows paths need no escaping.
export function splitArgs(text) {
  const out = [];
  let current = "", quote = null, started = false;
  for (const ch of String(text || "").trim()) {
    if (quote) { if (ch === quote) quote = null; else current += ch; }
    else if (ch === '"' || ch === "'") { quote = ch; started = true; }
    else if (/\s/.test(ch)) { if (started) { out.push(current); current = ""; started = false; } }
    else { current += ch; started = true; }
  }
  if (started) out.push(current);
  return out;
}

export function formatArgs(args = []) {
  return args.map(a => a === "" || /[\s"']/.test(a) ? (a.includes('"') ? `'${a}'` : `"${a}"`) : a).join(" ");
}

export function scheduleText(schedule) {
  if (!schedule) return "Manual only";
  if (schedule.type === "interval") {
    const n = schedule.intervalSeconds || 0;
    if (n % 86400 === 0 && n >= 86400) return `Every ${n / 86400} d`;
    if (n % 3600 === 0 && n >= 3600) return `Every ${n / 3600} h`;
    return `Every ${Math.round(n / 60)} min`;
  }
  const zone = schedule.timeZone ? ` (${schedule.timeZone})` : "";
  if (schedule.type === "daily") return `Daily at ${schedule.timeOfDay}${zone}`;
  return `Cron ${schedule.cron}${zone}`;
}

export function splitInterval(seconds) {
  if (seconds >= 3600 && seconds % 3600 === 0) return [seconds / 3600, "hours"];
  return [Math.max(1, Math.round(seconds / 60)), "minutes"];
}

export function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) return "";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes, unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

// relativeTime shows past and future times compactly; the exact time stays
// in the tooltip.
export function relativeTime(value, now = Date.now()) {
  if (!value) return "";
  const seconds = Math.round((new Date(value).getTime() - now) / 1000);
  if (!Number.isFinite(seconds)) return "";
  const abs = Math.abs(seconds), future = seconds > 0;
  if (abs < 45) return future ? "in a moment" : "just now";
  const unit = abs < 3600 ? [Math.round(abs / 60), "min"] : abs < 86400 ? [Math.round(abs / 3600), "h"] : null;
  if (!unit) return new Date(value).toLocaleDateString(undefined, { month: "short", day: "numeric" }) + (future ? ` ${new Date(value).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}` : "");
  return future ? `in ${unit[0]} ${unit[1]}` : `${unit[0]} ${unit[1]} ago`;
}

// The folder picker uses slash-separated paths ("srv/data", "C:/Users");
// jobs store native paths ("/srv/data", "C:\Users").
export function nativeFromBrowse(path, os) {
  if (os === "windows") return path.length === 2 ? `${path}\\` : path.replaceAll("/", "\\");
  return `/${path}`;
}

export function browseFromNative(value, os) {
  const text = String(value || "").trim();
  if (os === "windows") {
    if (!/^[A-Za-z]:[\\/]/.test(text)) return "";
    return text.replaceAll("\\", "/").replace(/\/+$/, "").replace(/\/{2,}/g, "/");
  }
  if (!text.startsWith("/")) return "";
  return text.replace(/\/{2,}/g, "/").replace(/^\/|\/$/g, "");
}

const wholeNumber = (value, label, min = 0) => {
  if (value === "" || value === null || value === undefined) return 0;
  const n = Number(value);
  if (!Number.isInteger(n) || n < min) throw new Error(`${label} must be a whole number of at least ${min}`);
  return n;
};

// jobFromForm converts plain form values into the job the backend stores.
// Semantic validation stays in the backend; this only parses.
export function jobFromForm(v) {
  const job = { name: v.name, engine: v.engine, enabled: !!v.enabled, timeoutSeconds: wholeNumber(v.timeoutMinutes, "Timeout") * 60 };
  if (v.id) job.id = v.id;
  if (v.scheduleType && v.scheduleType !== "manual") {
    const schedule = { type: v.scheduleType };
    if (v.scheduleType === "interval") schedule.intervalSeconds = wholeNumber(v.intervalValue, "Interval", 1) * (UNIT_SECONDS[v.intervalUnit] || 60);
    else if (v.scheduleType === "daily") schedule.timeOfDay = v.time;
    else schedule.cron = v.cron;
    if (v.timeZone) schedule.timeZone = v.timeZone.trim();
    job.schedule = schedule;
  }
  if (v.engine === "restic") {
    const retention = {
      keepLast: wholeNumber(v.keepLast, "Keep last"), keepDaily: wholeNumber(v.keepDaily, "Daily"),
      keepWeekly: wholeNumber(v.keepWeekly, "Weekly"), keepMonthly: wholeNumber(v.keepMonthly, "Monthly"),
      keepYearly: wholeNumber(v.keepYearly, "Yearly"), keepWithin: (v.keepWithin || "").trim(), prune: !!v.prune,
    };
    const hasRetention = retention.keepLast || retention.keepDaily || retention.keepWeekly || retention.keepMonthly || retention.keepYearly || retention.keepWithin;
    job.restic = {
      repositoryId: v.repositoryId || "",
      sources: lines(v.sources), excludes: lines(v.excludes), tags: String(v.tags || "").split(/[\s,]+/).filter(Boolean),
      useFsSnapshot: !!v.useFsSnapshot, checkAfterBackup: !!v.checkAfterBackup,
    };
    if (hasRetention) job.restic.retention = retention;
  } else {
    job.mirror = {
      source: v.source, destination: v.destination, delete: v.mode === "mirror",
      excludes: lines(v.excludes), additionalArgs: splitArgs(v.additionalArgs), executable: v.executable || "",
    };
    if (v.engine === "robocopy") {
      job.mirror.excludeFiles = lines(v.excludeFiles);
      job.mirror.retries = wholeNumber(v.retries, "Retries");
      job.mirror.retryWaitSeconds = wholeNumber(v.retryWait, "Retry wait");
    }
  }
  return job;
}

// repositoryFromForm converts plain form values into the repository the
// backend stores. Semantic validation stays in the backend.
export function repositoryFromForm(v) {
  const repository = { name: v.name, location: v.location, passwordFile: v.passwordFile || "", environmentFile: v.environmentFile || "", executable: v.executable || "" };
  if (v.id) repository.id = v.id;
  return repository;
}

// jobTarget is the one-line "where it goes" shown on a card: the name of the
// repository for restic jobs.
export function jobTarget(job, repositories = []) {
  if (job.engine === "restic") {
    const repository = repositories.find(view => view.repository.id === job.restic?.repositoryId)?.repository;
    return repository ? repository.name : "Repository missing";
  }
  return job.mirror ? `${job.mirror.source} → ${job.mirror.destination}` : "";
}

// runLabel names a history row: running, success, failure or skipped.
export function runState(run) {
  if (!run.finishedAt) return "running";
  if (run.success == null) return "skipped";
  return run.success ? "success" : "failure";
}

// toolHint explains how to install a missing engine.
export function toolHint(tool, os) {
  if (!tool || tool.available) return "";
  if (tool.engine === "robocopy") return "Robocopy is part of Windows; check that robocopy.exe is on the PATH of the RunPilot service.";
  const install = os === "windows" ? `install ${tool.engine} and add it to the PATH of the RunPilot service` : `in the RunPilot container set RUNPILOT_EXTRA_PACKAGES="${tool.engine}" and restart it; on other hosts install ${tool.engine} with the package manager`;
  return `${tool.message || `${tool.engine} is not installed`}. To use it, ${install}.`;
}
