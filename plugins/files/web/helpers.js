// Pure helpers for the Files frontend. They have no DOM or RunPilot
// dependencies so they can be unit tested directly.

const TEXT_EXTENSIONS = new Set(["txt", "md", "log", "yaml", "yml", "json", "toml", "ini", "conf", "cfg", "env", "sh", "bash", "py", "js", "mjs", "ts", "go", "css", "html", "xml", "csv", "sql", "service", "properties", "gitignore", "dockerignore"]);
const IMAGE_EXTENSIONS = new Set(["png", "jpg", "jpeg", "gif", "webp", "bmp"]);
const VIDEO_EXTENSIONS = new Set(["mkv", "mp4", "avi", "mov", "webm", "m4v", "ts", "wmv"]);
const AUDIO_EXTENSIONS = new Set(["mp3", "flac", "ogg", "wav", "m4a", "aac", "opus"]);
const ARCHIVE_EXTENSIONS = new Set(["zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar", "zst"]);

export const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, character => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[character]));

export function formatSize(bytes) {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes, unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

// formatDate shows today's entries by time and older ones by date.
export function formatDate(value, now = new Date()) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  if (date.toDateString() === now.toDateString()) return date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  return date.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

export function joinPath(dir, name) { return dir ? `${dir}/${name}` : name; }
export function parentPath(path) { const index = path.lastIndexOf("/"); return index < 0 ? "" : path.slice(0, index); }
export function baseName(path) { return path.slice(path.lastIndexOf("/") + 1); }

export function extension(name) {
  const lower = name.toLowerCase();
  if (lower.startsWith(".") && !lower.slice(1).includes(".")) return lower.slice(1);
  const index = lower.lastIndexOf(".");
  return index > 0 ? lower.slice(index + 1) : "";
}
export function isTextName(name) {
  const lower = name.toLowerCase();
  return TEXT_EXTENSIONS.has(extension(name)) || ["dockerfile", "makefile", "readme", "license", "compose"].includes(lower) || lower.startsWith(".env");
}
export function isImageName(name) { return IMAGE_EXTENSIONS.has(extension(name)); }

// effectiveType treats a symlink like the file or folder it points to.
export function effectiveType(item) {
  return item.type === "symlink" ? (item.linkType === "file" || item.linkType === "directory" ? item.linkType : "symlink") : item.type;
}

// displayPath shows a browser path the way the host writes it: "/srv/data"
// on Linux and "C:/Users" on Windows.
export function displayPath(path) {
  return /^[A-Za-z]:(\/|$)/.test(path) ? path : `/${path}`;
}

// fileKind picks the icon family of an entry.
export function fileKind(item) {
  const type = effectiveType(item);
  if (type === "directory") return "folder";
  if (type === "symlink") return "link";
  const ext = extension(item.name);
  if (IMAGE_EXTENSIONS.has(ext)) return "image";
  if (VIDEO_EXTENSIONS.has(ext)) return "video";
  if (AUDIO_EXTENSIONS.has(ext)) return "audio";
  if (ARCHIVE_EXTENSIONS.has(ext)) return "archive";
  return isTextName(item.name) ? "text" : "file";
}

// uniqueName keeps a pasted item from replacing an existing one:
// "a.txt" becomes "a (copy).txt", then "a (copy 2).txt".
export function uniqueName(name, taken) {
  if (!taken.has(name)) return name;
  const dot = name.lastIndexOf(".");
  const stem = dot > 0 ? name.slice(0, dot) : name, ext = dot > 0 ? name.slice(dot) : "";
  for (let index = 1; ; index++) {
    const candidate = `${stem} (copy${index > 1 ? ` ${index}` : ""})${ext}`;
    if (!taken.has(candidate)) return candidate;
  }
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

// sortEntries keeps folders first, then orders by the chosen column.
export function sortEntries(entries, key = "name", ascending = true) {
  const direction = ascending ? 1 : -1;
  return [...entries].sort((left, right) => {
    const leftFolder = effectiveType(left) === "directory", rightFolder = effectiveType(right) === "directory";
    if (leftFolder !== rightFolder) return leftFolder ? -1 : 1;
    let order = 0;
    if (key === "size") order = (left.size || 0) - (right.size || 0);
    else if (key === "modified") order = Date.parse(left.modifiedAt) - Date.parse(right.modifiedAt);
    if (order === 0) order = collator.compare(left.name, right.name);
    return order * direction;
  });
}

export function filterEntries(entries, query) {
  const needle = query.trim().toLowerCase();
  return needle ? entries.filter(item => item.name.toLowerCase().includes(needle)) : entries;
}

// selectRange returns the names between anchor and target, inclusive, in
// the visible order, for shift-click selection.
export function selectRange(names, anchor, target) {
  const from = names.indexOf(anchor), to = names.indexOf(target);
  if (from < 0 || to < 0) return [target];
  return names.slice(Math.min(from, to), Math.max(from, to) + 1);
}

const span = (kind, value) => `<span class="files-syntax-${kind}">${escapeHTML(value)}</span>`;

// highlight returns escaped HTML with light syntax colors for config files.
export function highlight(content, name) {
  const ext = extension(name), lower = name.toLowerCase();
  const yaml = ext === "yaml" || ext === "yml", env = ext === "env" || lower.startsWith(".env");
  const structured = ["json", "yaml", "yml", "toml", "ini", "conf", "xml"].includes(ext);
  return content.split("\n").map(line => {
    if (/^\s*(#|\/\/)/.test(line)) return span("comment", line);
    return line.split(/("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')/g).map((part, index) => {
      if (index % 2) return span("string", part);
      let safe = escapeHTML(part);
      if (structured) safe = safe.replace(/\b(true|false|null|yes|no)\b/gi, '<span class="files-syntax-keyword">$1</span>').replace(/\b(-?\d+(?:\.\d+)?)\b/g, '<span class="files-syntax-number">$1</span>');
      if (yaml) safe = safe.replace(/^(\s*(?:-\s+)?)([A-Za-z0-9_.-]+)(\s*:)/, '$1<span class="files-syntax-key">$2</span>$3');
      if (env) safe = safe.replace(/^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)(=)/, '$1<span class="files-syntax-key">$2</span>$3');
      return safe;
    }).join("");
  }).join("\n") + "\n";
}

export const ICONS = {
  folder: '<path d="M3 7.5A1.5 1.5 0 0 1 4.5 6h4l2 2h9A1.5 1.5 0 0 1 21 9.5v8a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5z"/>',
  file: '<path d="M6 3h8l4 4v14H6z"/><path d="M14 3v4h4"/>',
  text: '<path d="M6 3h8l4 4v14H6z"/><path d="M14 3v4h4M9 12h6M9 16h6"/>',
  image: '<rect x="4" y="5" width="16" height="14" rx="1.5"/><circle cx="9" cy="10" r="1.5"/><path d="m5 18 5-5 3 3 2-2 4 4"/>',
  video: '<rect x="3" y="6" width="13" height="12" rx="1.5"/><path d="m16 10 5-3v10l-5-3"/>',
  audio: '<path d="M9 18V6l10-2v12"/><circle cx="6.5" cy="18" r="2.5"/><circle cx="16.5" cy="16" r="2.5"/>',
  archive: '<rect x="4" y="4" width="16" height="16" rx="1.5"/><path d="M12 4v2m0 2v2m0 2v2M10 14h4v3h-4z"/>',
  link: '<path d="M10 14a4 4 0 0 0 5.6 0l3-3a4 4 0 0 0-5.6-5.6l-1 1M14 10a4 4 0 0 0-5.6 0l-3 3a4 4 0 0 0 5.6 5.6l1-1"/>',
  drive: '<rect x="3" y="12" width="18" height="7" rx="1.5"/><path d="M5.5 12 8 5h8l2.5 7M7 15.5h.01"/>',
  computer: '<rect x="3" y="4" width="18" height="12" rx="1.5"/><path d="M8 20h8M12 16v4"/>',
  lock: '<rect x="5" y="11" width="14" height="9" rx="1.5"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
  up: '<path d="M12 19V5M5 12l7-7 7 7"/>',
  refresh: '<path d="M20 11a8 8 0 1 0-2.3 5.7M20 5v6h-6"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13M3.5 6h.01M3.5 12h.01M3.5 18h.01"/>',
  grid: '<rect x="4" y="4" width="7" height="7" rx="1"/><rect x="13" y="4" width="7" height="7" rx="1"/><rect x="4" y="13" width="7" height="7" rx="1"/><rect x="13" y="13" width="7" height="7" rx="1"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="m16 16 4.5 4.5"/>',
  download: '<path d="M12 4v11M7 10l5 5 5-5M5 20h14"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/><path d="m13 7 4 4"/>',
  rename: '<path d="M4 7h10M4 12h7M4 17h5"/><path d="m14 19 6-6-2-2-6 6v2z"/>',
  copy: '<rect x="8" y="8" width="12" height="13" rx="2"/><path d="M16 8V5a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h2"/>',
  move: '<path d="M5 12h14M13 6l6 6-6 6"/>',
  delete: '<path d="M5 7h14M10 7V5h4v2m-7 0 1 12h8l1-12"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>',
  close: '<path d="m6 6 12 12M18 6 6 18"/>',
  eye: '<path d="M2 12s3.5-6.5 10-6.5S22 12 22 12s-3.5 6.5-10 6.5S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  check: '<path d="m5 12 5 5 9-10"/>',
};

export const icon = (name, className = "") => `<svg class="files-svg${className ? ` ${className}` : ""}" viewBox="0 0 24 24" aria-hidden="true">${ICONS[name] || ICONS.file}</svg>`;
