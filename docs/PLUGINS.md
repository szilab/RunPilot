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

## Backend: WebAssembly

Backend modules execute inside RunPilot through wazero. WASM is the binary runtime format, not the required source language: plugins may be authored in Go/TinyGo, Rust, C/C++ or another language targeting the supported WASM ABI.

The ABI is language-neutral and data-oriented. Plugins receive no native Go pointers or direct access to RunPilot internals. The backend lifecycle remains intentionally small:

```text
runpilot_init
runpilot_call
runpilot_shutdown
```

Calls exchange versioned structured data. Traps, malformed responses, initialization failures and timeouts must fail the plugin operation without taking down RunPilot.

The v1 ABI's concrete linear-memory allocation and packed-buffer convention is
implemented and documented in [PLUGIN_API.md](PLUGIN_API.md).

First-party backend sources are built with TinyGo 0.38.0. The checked-in
System plugin is the reference source implementation: run
`go generate ./plugins/system/backend` before packaging it. TinyGo is never a
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

The public namespaces and ownership boundary are defined now, but their
registration hosts are Phase 5 work. Existing Remote frontend compatibility is
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

The first complete reference implementation should be a small `system` plugin proving a real WASM backend, host capability calls, WebSocket RPC/events, a plugin-owned page, shared UI, Settings contribution, Overview cards, platform compatibility, packaging and restart activation.

Only after this vertical slice is stable should existing RunPilot features be migrated one by one.

## Reference system plugin

The bundled `system` plugin is installed and activated through the normal
`.rpplugin` validation and wazero runtime path. It provides the first plugin
page and Overview cards through the shared frontend extension API, and obtains
host metrics only via `host.system.status`. Generate its checked-in WASM module
with `go generate ./plugins/system/backend`, then package it normally with
`go run ./cmd/plugin-build plugins/system`.

The fresh-install System package embeds the checked-in, source-derived WASM
artifact used by `plugins/system/backend/plugin.wasm`; it is not a handwritten
fallback. A test compares the embedded bytes with that build artifact, so
end-user installation never requires TinyGo.
