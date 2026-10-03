# RunPilot plugins

RunPilot plugins are immutable `.rpplugin` packages installed below `<dataDir>/plugins/<id>/<version>`. Mutable data belongs below `<dataDir>/plugin-data/<id>`. Plugin enablement is framework configuration.

A package may contain:

```text
plugin.yaml
backend/plugin.wasm   # optional
web/plugin.js         # optional
web/plugin.css        # optional
```

A plugin may provide a backend, GUI contributions, settings, or any useful combination. Feature pages and navigation entries belong to plugins rather than core.

## Manifest and compatibility

The manifest uses a versioned plugin API and declares metadata, compatibility and optional entry points. A plugin may support both Windows and Linux or only one platform. Incompatible plugins remain installed but are not activated.

There is no per-plugin permission list in the target architecture. All loaded plugins may use all published host capabilities. RunPilot normally runs unprivileged, so the service account's OS permissions remain authoritative.

The implemented manifest accepts an optional `platforms` list containing only
`linux` and `windows`; omitting it means platform-independent. Declared backend
and frontend files must be present in a package. The legacy `permissions` field
is no longer accepted. The precise package, ABI, capability and envelope
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

First-party backend sources are built with TinyGo 0.38.0. The checked-in
System plugin is the reference source implementation: run
`go generate ./plugins/system/backend` before packaging it. The Tasks plugin is
the first feature backend: run `go generate ./plugins/tasks/backend` after
changing it (the checked-in `plugin.wasm` is verified against the source when
TinyGo is available). Stateful backends must build with `-gc=conservative`:
TinyGo's default `wasm-unknown` collector never frees memory. TinyGo is never a
runtime dependency. The optional `runpilot_event` ABI export receives generic
host callbacks and is serialized with normal plugin calls.

## Host capability API

Backend plugins use RunPilot host capabilities for OS and framework operations:

```text
host.process.*
host.fs.*
host.network.*
host.scheduler.*
host.storage.*
host.history.*
host.system.*
host.config.*
host.log.*
```

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

The public namespaces and their registration hosts are implemented. Existing Remote frontend compatibility is
temporary and is not a public API for new plugins.

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

Style selection is framework-owned. System/Light/Dark color scheme is separate from the selected style. The architecture is prepared for future non-executable `.rptheme` packages containing metadata and CSS/token overrides; themes are deliberately separate from WASM plugins.

## Overview and Settings

Overview is an extension surface: cards are registered by plugins and core does not know their feature semantics.

Settings is a core shell. Framework sections cover RunPilot, Appearance and plugin management; plugins register their own settings sections and own their schema/meaning/editor.

## Package lifecycle

The plugin manager downloads trusted catalog entries, verifies package size and SHA-256, validates manifests, rejects unsafe ZIP paths and installs atomically.

Installed, enabled, loaded, incompatible, failed, update-available and restart-required remain distinct states. Enable/disable changes desired state; activation remains restart-based unless a future requirement justifies safe hot loading.

## Development and release

First-party plugins live under `plugins/<plugin-id>` and build into deterministic `.rpplugin` archives. The package builder must fail when a manifest declares an entry point missing from the package.

The `system` package remains a nonpublic technical fixture for ABI, host
capability, WebSocket and frontend extension tests. It is not automatically
installed or enabled on fresh RunPilot startup. Its checked-in source and WASM
remain intact; generate them with `go generate ./plugins/system/backend` when
changing the fixture. Existing ABI-v1/v2 tests remain useful and supported.

`plugins/publication.json` explicitly excludes System from public publication.
The frontend-only `plugins/examples/hello` example exercises local packaging and
registry testing. Production publication is an explicit version-driven action;
application release workflows no longer bundle plugin artifacts.

Host metrics presentation will use future plugin/widget contributions. No System
feature expansion is part of the registry implementation.

`plugins/tasks` is the first feature plugin (continuous and scheduled command
tasks). It is registered in `plugins/publication.json` with `publish: false`,
is not installed or enabled automatically, and does not replace the legacy
Tasks page (its navigation entry is "Tasks (plugin)"). Backup is not part of
it. Unit tests inject a fake host; `internal/core/tasks_plugin_test.go` runs the
real ABI-v2 WASM through the normal controller path.

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

`plugins/remote-rdp` includes a publishable ABI-v2 backend and a plugin-
owned target/connect page. Target definitions and guacd settings use
plugin-namespaced storage; the guacd endpoint is edited in a Settings card.
Passwords are supplied for each session only. The
plugin owns Guacamole negotiation over the generic TCP/TLS stream capability
and uses the shared interactive-session view for sizing, fullscreen, focus and
cleanup. Its Diagnose action reads a bounded plugin-owned stage log; live
diagnostic events include endpoint and handshake progress but no usernames or
passwords. Deleting a target with an active session is rejected. The legacy
Remote/RDP UI and provider remain available as the production path and are not
replaced by this migration phase.
