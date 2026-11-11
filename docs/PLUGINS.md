# RunPilot plugins

RunPilot plugins are immutable `.rpplugin` packages installed below `<dataDir>/plugins/<id>/releases/<version>`. Mutable data belongs below `<dataDir>/plugins/<id>/data`. Plugin enablement is framework configuration.

A package may contain:

```text
plugin.yaml
backend/plugin.wasm   # optional
web/plugin.js         # optional
web/plugin.css        # optional
```

A plugin may provide a backend, GUI contributions, settings, or any useful combination. Feature pages and navigation entries belong to plugins rather than core.

Before building a dedicated integration for an application with an existing
capable web GUI, consider `web.apps` when secure access to that GUI is enough.
Use a dedicated plugin when RunPilot needs structured control, automation, data
integration, lifecycle management, or native UI beyond the existing GUI.

## Manifest and compatibility

The manifest uses a versioned plugin API and declares metadata, compatibility and optional entry points. A plugin may support both Windows and Linux or only one platform. Incompatible plugins remain installed but are not activated.

There is no per-plugin permission list in the target architecture. All loaded plugins may use all published host capabilities. RunPilot normally runs unprivileged, so the service account's OS permissions remain authoritative.

The implemented manifest accepts an optional `platforms` list containing only
`linux` and `windows`; omitting it means platform-independent. Declared backend
and frontend files must be present in a package. The precise package, ABI, capability and envelope
contracts are in [PLUGIN_API.md](PLUGIN_API.md).

Backend and frontend compatibility use independent SemVer contract versions,
currently `1.0.0` each. Package versions are independent of RunPilot releases.
See [PLUGIN_REGISTRY.md](PLUGIN_REGISTRY.md) for manifest ranges, publication,
catalog schema and Settings install/update lifecycle. Raw WASM ABI selection
remains separate from these contract versions.

## Backend: WebAssembly

Backend modules execute inside RunPilot through wazero. WASM is the binary runtime format, not the required source language: plugins may be authored in Go/TinyGo, Rust, C/C++ or another language targeting the supported WASM ABI.

The ABI is language-neutral and data-oriented. Plugins receive no native Go pointers or direct access to RunPilot internals. ABI v1 and ABI v2 are selected explicitly by `requires.runpilotApi` in `plugin.yaml`. The backend lifecycle remains intentionally small:

```text
runpilot_init
runpilot_call
runpilot_shutdown
```

Calls exchange versioned structured data. Traps, malformed responses, initialization failures and timeouts must fail the plugin operation without taking down RunPilot.

ABI v1's concrete linear-memory allocation and packed-buffer convention
remains supported for existing plugins. ABI v2 uses invocation-scoped,
host-owned buffers and copy-only imports. First-party TinyGo code should use
`pluginapi.ReadInput`, `pluginapi.WriteOutput`, and `pluginapi.CallHost` rather
than managing WASM pointers or response handles. The precise wire contracts
are in [PLUGIN_API.md](PLUGIN_API.md).

First-party backend sources are built with TinyGo 0.38.0. The internal System
test fixture is the reference source implementation: run
`go generate ./internal/plugins/system_fixture/backend` to rebuild it. Shared
plugin-runtime test fixtures live under `internal/plugins/testdata`; they are
not distributable plugins. The Tasks plugin is the first feature backend: run
`go generate ./plugins/tasks/backend` after
changing it (the checked-in `plugin.wasm` is verified against the source when
TinyGo is available). Stateful backends must build with `-gc=conservative`:
TinyGo's default `wasm-unknown` collector never frees memory. TinyGo is never a
runtime dependency. The optional `runpilot_event` ABI export receives generic
host callbacks and is serialized with normal plugin calls.

## Host capability API

Backend plugins use RunPilot host capabilities for OS and framework operations:

```text
host.log.*
host.system.*
host.storage.*
host.scheduler.*
host.process.*          # including process.session.* (PTY/ConPTY)
host.workspace.*
host.history.*
host.events.*
host.network.stream.*
host.browser.* / host.http.gateway.*
```

See the capability index in [PLUGIN_API.md](PLUGIN_API.md) for the exact names.
Calls into one plugin are serialized; keep handlers short and run long work
through `process.start` or `process.session.*` events.

Every loaded plugin can call every published capability. Capabilities are an ABI boundary, not a per-plugin authorization system. Keep them explicit and stable; avoid generic syscall escape hatches. Capability implementations still validate arguments and RunPilot invariants.

## Browser integration

Enabled frontend modules export an activation entry point:

```javascript
export function activate(runpilot) {
    // Register pages, navigation, settings and Overview contributions.
}
```

The frontend runtime exposes versioned extension APIs such as:

```text
runpilot.ui
runpilot.navigation
runpilot.overview
runpilot.settings
runpilot.ws
```

Feature frontend code must not depend on private RunPilot JavaScript implementation details.

The public namespaces and their registration hosts are implemented. Remote
features register through the same public navigation, UI, and WebSocket APIs as
other plugins.

## WebSocket protocol

Interactive browser/server communication uses the single authenticated RunPilot WebSocket. Plugins do not create feature-specific REST APIs.

Example request:

```json
{"id":"42","plugin":"system","method":"status.get","params":{}}
```

Example event:

```json
{"plugin":"system","event":"status.changed","data":{}}
```

The common protocol owns correlation, structured errors, timeouts and event routing. Streaming features should extend the same WebSocket framing rather than create independent feature transports.

## GUI contributions

A plugin frontend may register navigation items, complete pages, Settings sections, Overview cards and supporting dialogs/components. The core owns only the application shell and extension hosts; it must not contain provider-specific rendering.

## Shared UI and styling

Plugins should use the RunPilot shared UI library wherever practical. Shared controls automatically follow the active visual style.

Custom CSS may use public semantic design tokens for surfaces, text, borders, spacing, radii and related properties. Plugins must not hard-code assumptions about the default style or light/dark mode.

Use the shared `rp-card`, `button` variants (including `button icon`),
`rp-segmented`, form controls, status indicators and notices for common
interface elements. Plugin CSS should
handle feature layout only; it should not redefine shared control sizing or
introduce a plugin-specific color palette.

Style selection is framework-owned. System/Light/Dark color scheme is separate from the selected style. The architecture is prepared for future non-executable `.rptheme` packages containing metadata and CSS/token overrides; themes are deliberately separate from WASM plugins.

## Overview and Settings

Overview currently shows a built-in host monitoring dashboard. The widget
extension contract still accepts plugin registrations with stable IDs, titles,
size hints and mount/refresh/dispose hooks, but does not mount plugin widgets
on this dashboard. See [PLUGIN_API.md](PLUGIN_API.md).

Settings is a core shell. Framework sections cover RunPilot, Appearance and plugin management; plugins register their own settings sections and own their schema/meaning/editor.

## Package lifecycle

The plugin manager downloads trusted catalog entries, verifies package size and SHA-256, validates manifests, rejects unsafe ZIP paths and installs atomically.

Installed, enabled, loaded, incompatible, failed, update-available and restart-required remain distinct states. Enable/disable changes desired state; activation remains restart-based unless a future requirement justifies safe hot loading.

## Development and release

First-party plugins live under `plugins/<plugin-id>` and build into deterministic `.rpplugin` archives. The package builder must fail when a manifest declares an entry point missing from the package.

The `system` package remains a nonpublic technical fixture for ABI, host
capability, WebSocket and frontend extension tests. It is not automatically
installed or enabled on fresh RunPilot startup. Its checked-in source and WASM
remain intact; generate them with `go generate ./internal/plugins/system_fixture/backend` when
changing the fixture. Existing ABI-v1/v2 tests remain useful and supported.

`plugins/publication.json` explicitly excludes System from public publication.
Production publication is an explicit version-driven action; application
release workflows no longer bundle plugin artifacts.

Host metrics presentation is built into Overview. No System plugin expansion is
part of the registry implementation.

`plugins/tasks` owns the Tasks page for continuous and scheduled command tasks.
It is publishable, but is not installed or enabled automatically. Backup is not
part of this plugin. Unit tests inject a fake host;
`internal/core/tasks_plugin_test.go` runs the real ABI-v2 WASM through the normal
controller path.

`plugins/backup` (plugin ID `backup`, backend contract 1.4) owns the Backup page,
which switches between **Backups** and **Repositories** like the Remote plugin's
protocol tabs. Backups are Restic, rsync (Linux) and Robocopy (Windows) jobs with
their own schedules and run history. A Restic repository is a separate stored
object (`backup.repository.*`): its location, password file, environment file
and executable, plus the repository operations (initialize, check, unlock), its
snapshots, restore into a new or empty folder, and its own run history. A
Restic backup only stores a `repositoryId` chosen from the repositories; that
assignment is fixed when the backup is created and can only be undone by
deleting the backup. A repository cannot be deleted while backups use it, and
its location cannot change then. Definitions use plugin `storage.*`; the engine
commands, exit-code rules and path checks live in the plugin
(`backend/engines.go`, `backend/paths.go`). Its folder picker lists folders
through `workspace.list` with an external `root`, like Files. Passwords and keys
stay in host files that repositories reference. Unit tests inject a fake host;
`internal/core/backup_plugin_test.go` runs the real WASM with a fake restic
script and, when installed, the real rsync. Regenerate the WASM with
`go generate ./plugins/backup/backend`.

`plugins/terminal` provides interactive sessions through the generic process
session capability. Its tab strip uses `+` to open a session and per-tab close
buttons. The Terminal settings card accepts an executable path and plain-text
arguments, with quotes for values containing spaces. Arguments are converted
to an array and saved through plugin-namespaced `storage.*`; changes apply to new
sessions without interrupting existing ones. An empty command preserves the
platform default, including the Linux service's `SHELL` selection and the
Windows `cmd.exe` fallback. The `terminal.settings.get`,
`terminal.settings.set`, and `terminal.settings.test` methods use the common
application WebSocket. Testing starts and immediately closes a temporary
process session with the entered command and arguments, without saving them
or interrupting existing sessions. Regenerate its WASM backend with
`go generate ./plugins/terminal/backend` after backend changes.

`plugins/remote` is published as plugin ID `remote`, providing RDP, VNC and
Xpra. It owns all target lists and session types, uses the generic
`network.stream.*` capability, packages Guacamole, noVNC 1.7.0 and the Xpra
HTML5 client v19 with their license files, and provides one protocol-tabbed
navigation page. The protocols share `session-toolbar.js`,
`diagnostics-dialog.js` and `stream-channel.js`. Xpra servers run through
`process.start` with `workspaceDirectory` and are reached through a
`network.stream.open` `workspaceSocket`; `web/xpra-bridge.js` connects the
packaged client to that stream (see `web/vendor/xpra-html5/README.runpilot.md`).
Passwords are never persisted. Core contains no Remote provider, transport or
Xpra runtime. Regenerate its WASM backend with
`go generate ./plugins/remote/backend` after backend changes.

`plugins/files` (plugin ID `files`, backend contract 1.3) provides the file
manager for the whole host filesystem. It has no settings: its backend maps
every browser path onto `workspace.*` with `/` as `root` (on Windows the drive
of the path, with the drives listed from `system.status` at the top), and the
service account's permissions decide what succeeds. The top folder and drive
roots can be listed but never renamed, moved or deleted. Background operation events are relayed
to the browser as `files.operation`, and the browser uploads and downloads
directly through the host transfer route. Regenerate its WASM backend with
`go generate ./plugins/files/backend` after backend changes.

Browser regression checks are opt-in: set `RUNPILOT_PLAYWRIGHT_MODULE` to a local
Playwright module and optionally `RUNPILOT_BROWSER_EXECUTABLE` to its browser,
then run `go test ./internal/web -run 'Test(UI|WebApps)RealBrowser'`.
`RUNPILOT_UI_SCREENSHOTS` saves the UI test's light/dark desktop, tablet, and
mobile captures. These tests use temporary application data and local fixtures.

For a development instance from this checkout run `tools/dev.sh`: it installs
every plugin under `plugins/` from source into the git-ignored `./data`,
enables them on the first start, keeps its locator in `./data/.data-root` and
runs on port 9070 (`PORT=…` to change; `--wasm` also rebuilds the TinyGo
backends, using Docker when TinyGo is not installed). For any other manual
isolated instance, also set `RUNPILOT_DATA_ROOT_FILE`
to a locator inside its temporary deployment directory. Separate `--data-dir`
and `--config-dir` flags do not isolate the deployment locator: completing
initial setup persists the selected data root there.
