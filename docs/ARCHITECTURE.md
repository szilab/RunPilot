# RunPilot architecture

## Direction

RunPilot is a lightweight Windows/Linux host-management application that is
being refactored into a small **plugin host and management framework**.

> **RunPilot provides the framework; plugins provide the features.**

The target core owns infrastructure that multiple features need: plugin
installation/runtime, versioned host capabilities, one authenticated application
WebSocket, the web shell, shared UI/theme primitives, and framework settings.
Tasks, Storage, Docker, Terminal, Software, Backup and Remote Access belong in
plugins once their migrations reach parity. RDP and VNC have migrated to
first-party plugins. Xpra is the temporary Remote exception described below.

The migration is incremental. Those features still exist in the legacy core
today and must remain operational until the equivalent plugin path is implemented
and tested. New architecture work must not use that transitional state as a
reason to add more feature semantics to core.

## Core boundary

Target core responsibilities are:

- immutable `.rpplugin` package validation, installation and lifecycle;
- catalog-based plugin discovery/update handling;
- wazero backend runtime with explicit raw WASM ABI versions;
- narrow host capabilities for process, scheduler, storage, system and similar
  generic operations;
- one authenticated browser/server application WebSocket;
- web shell, navigation host, Overview and Settings extension hosts;
- shared frontend components, semantic design tokens and appearance handling;
- framework configuration and plugin enablement.

Core must not permanently understand domain concepts such as Task, Docker
container, backup repository, terminal session type, Xpra target or Scoop
package. Xpra remains in core temporarily because its subprocess/display
lifecycle and local HTTP/WebSocket browser publication do not map cleanly to
current capabilities. Generic engines may remain in core when exposed as
reusable host capabilities.

## Plugin model

First-party plugin source lives in this repository, normally below
`plugins/<id>/`, but each plugin is a logically independent package with its
own SemVer version and release lifecycle.

A plugin may contain any combination of:

```text
plugin.yaml
backend/plugin.wasm   # optional
web/plugin.js         # optional
web/plugin.css        # optional
```

Backend modules run in-process through wazero. First-party backends are compiled
with TinyGo and shipped as prebuilt WASM; TinyGo is never a production runtime
dependency. Frontend modules register pages, navigation, Overview contributions
and Settings sections through the public frontend API.

Plugins may be platform-independent, Linux-only or Windows-only. Incompatible
packages can remain installed but are not activated. Enablement is desired state;
activation is intentionally restart-based rather than hot-loaded.

## Version domains

These versions are independent and must not be collapsed into one number:

| Domain | Current role |
| --- | --- |
| RunPilot version | Native application release |
| Raw WASM ABI | Binary calling convention; v1 and v2 are currently supported |
| Backend contract | Host capability/runtime compatibility; currently `1.0.0` |
| Frontend contract | Public browser extension compatibility; currently `1.0.0` |
| Plugin version | Independent SemVer release of one plugin |

Plugin manifests use SemVer ranges for backend/frontend contracts and
`requires.runpilotApi` only for the raw WASM ABI. Compatibility is therefore
based on contracts, not on the RunPilot application version.

ABI v1 remains for compatibility. ABI v2 is the preferred first-party path:
lifecycle data and capability responses are host-owned and copied synchronously,
so the host never retains pointers or slices backed by WASM linear memory after
an ABI import returns. The nonpublic System fixture exercises ABI v2 and the
TinyGo SDK.

## Host capabilities

Plugins do not receive Go objects, raw handles or unrestricted access to
RunPilot internals. Generic functionality is exposed through explicit host
capabilities such as:

```text
host.process.*
host.scheduler.*
host.storage.*
host.history.*
host.system.*
host.config.*
host.log.*
```

`host.history.*` is a generic, owner-scoped execution record with captured
output, backed by RunPilot's existing SQLite history and run-log files. Plugins
never supply filesystem paths and never store growing logs in `plugin-data`.

Additional families such as filesystem or network access should be added only
when a real plugin needs a narrow, reusable operation. Do not add generic
syscall/command escape hatches.

There is intentionally no per-plugin permission matrix. Plugins are trusted
packages and operate within the OS permissions of the RunPilot service account.
Capability implementations still validate inputs, ownership and RunPilot
invariants.

## Browser and frontend model

Application RPC and plugin events use the common authenticated WebSocket. A
request identifies a plugin and method:

```json
{"id":"42","plugin":"tasks","method":"tasks.list","params":{}}
```

Responses use the same correlation ID; asynchronous events use the same
connection. Static HTML/JS/CSS and plugin assets remain ordinary HTTP resources.
Legacy feature REST endpoints may remain during migration, but new plugin
features must not create feature-specific REST APIs or WebSockets.

The public frontend surface is built around:

```text
runpilot.ws
runpilot.navigation
runpilot.overview
runpilot.settings
runpilot.ui
```

Core owns layout and visual primitives; plugins own feature pages and feature
semantics. Plugin CSS should use public semantic design tokens rather than
hard-code RunPilot's current colors or spacing.

Overview is an extension host rather than a fixed dashboard. Host CPU/memory/disk
presentation is planned as widget contributions; the current System package is a
technical fixture, not the intended user-facing System feature.

## Plugin publication and installation

The main repository is also the source repository for first-party plugins, but
plugin releases are independent from application releases.

GitHub Releases store immutable
`plugin-<id>-v<version>` artifacts. A generated `plugin-catalog` release
publishes `catalog.json`. RunPilot reads that catalog, selects the newest
platform/contract-compatible version, verifies SHA-256 and package metadata, and
uses the existing atomic installer.

Settings exposes installed/available/update/incompatible state plus explicit
install, update, enable/disable and uninstall operations. New installations are
not silently enabled, updates are not automatic, and activation remains pinned
until restart. Mutable `plugin-data/<id>` is retained on uninstall.

Publication policy is repository-owned. The System fixture and incomplete Remote
scaffolds are intentionally excluded from the public catalog.

Operational details belong in [PLUGIN_REGISTRY.md](PLUGIN_REGISTRY.md); plugin
authoring belongs in [PLUGINS.md](PLUGINS.md); raw ABI/protocol details belong
in [PLUGIN_API.md](PLUGIN_API.md).

## Migration direction

The next work is feature migration, not further expansion of the core plugin
framework. Migrate one feature at a time and remove its legacy core/API code only
after plugin parity and automated regression coverage.

Current intended order:

1. Tasks and execution history;
2. Storage;
3. Docker;
4. Terminal;
5. Software and backup integrations;
6. Finish Remote provider migrations (one provider at a time).

### Remote status

`remote.rdp` and `remote.vnc` are independently packaged, publishable ABI-v2
plugins. The legacy RDP and VNC providers, browser transports, and combined
Remote page have been removed from core. Xpra's legacy provider/runtime remains
internal with no normal UI or navigation. Its plugin migration is deferred
until reusable process, listener, and browser-publication lifecycle capabilities
are designed; see the concise [backlog](../BACKLOG.md).

### Tasks status

Tasks has a real first-party plugin (`plugins/tasks`, version `0.1.0`, ABI v2,
unpublished and never auto-enabled). It covers continuous command tasks and
scheduled command tasks, owns their definitions and semantics, and talks to the
browser only through the common WebSocket. It runs on the generic storage,
scheduler, process, history and event capabilities; core has no Task concept.

**The legacy Tasks implementation remains the active production path** until an
explicit, separate cutover. Both can be installed side by side (the plugin page
is "Tasks (plugin)"), and legacy Tasks, `runpilot.yaml` and Run history are
unaffected by it. **Backup is not part of the Tasks plugin**: Backup jobs stay
in the legacy job engine until their own migration.

The capability changes made for Tasks are generic framework infrastructure:
interpreter-aware `process.start` (launcher semantics), process timeouts and
captured output, `scheduler.validate`, the owner-scoped `history.*` family, and
ordered/bounded event delivery. See [PLUGIN_API.md](PLUGIN_API.md).

The async ABI-v2 process/scheduler/event path should remain covered by real-WASM
race/integration tests before stateful feature migrations depend on it.

The widget contribution model should be designed when the first real widgets are
implemented, rather than by expanding the System fixture now.

## Security and guardrails

- RunPilot normally runs without root/Administrator privileges; the service
  account is the primary OS security boundary.
- Keep host capabilities narrow and versioned.
- Do not add per-plugin permissions without a concrete future requirement.
- Do not add root/Admin escalation, native Go plugins or generic syscalls.
- Keep browser application traffic on the common WebSocket.
- Keep plugin frontend code on public extension APIs and design tokens.
- Preserve Windows/Linux behavior and explicit platform compatibility.
- Preserve existing legacy feature behavior while it is still the production
  implementation.
- Prefer incremental migrations over simultaneous rewrites.
- Keep themes non-executable; a future theme package should be CSS/metadata, not
  another WASM plugin.
