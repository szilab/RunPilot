import { copyFile, mkdir } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const destination = resolve(here, "../../internal/web/static/rdp");
const modules = resolve(here, "node_modules/@devolutions");
await mkdir(destination, { recursive: true });
const desktopAsset = resolve(destination, "iron-remote-desktop-0.11.0.js");
await copyFile(resolve(modules, "iron-remote-desktop/iron-remote-desktop.js"), desktopAsset);
await copyFile(resolve(modules, "iron-remote-desktop-rdp/iron-remote-desktop-rdp.js"), resolve(destination, "iron-remote-desktop-rdp-0.7.0.js"));
