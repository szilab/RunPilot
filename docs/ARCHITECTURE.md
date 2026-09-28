# RunPilot architecture

## Product direction

RunPilot is a lightweight Windows/Linux **plugin host and management framework**. The core provides the runtime, browser shell, shared UI system, WebSocket transport and host capabilities required by plugins. Product features such as Tasks, Docker, Storage, Terminal, Software Management and Remote Access are plugins rather than core domains.

> **RunPilot provides the framework; plugins provide the features.**

RunPilot normally runs as an unprivileged user. Plugins can only cause host-side operations through RunPilot and remain bounded by the operating-system permissions of the RunPilot service account. RunPilot does not add a second per-plugin authorization model.

## Core boundary

The core contains only:

- plugin package installation, validation, catalog/update handling and lifecycle;
- WebAssembly backend runtime;
- a versioned host capability API;
- one authenticated browser/server WebSocket transport;
- the web application shell and navigation host;
- shared GUI components and design tokens;
- Overview and Settings extension hosts;
- theme/style selection and application;
- framework configuration and plugin management.

The core must not contain product-specific knowledge such as Docker containers, backup repositories, scheduled tasks, terminals, Xpra, RDP, VNC or package managers.

```text
RunPilot Core
├── Plugin Runtime
│   ├── .rpplugin install/update
│   ├── WASM runtime
│   ├── lifecycle
│   └── platform compatibility
├── Capability Host
│   ├── process / filesystem / network
│   ├── scheduler / storage / system
│   └── config / logging
├── WebSocket Transport
└── Web Shell
    ├── navigation + shared UI
    ├── Overview + Settings hosts
    └── Theme Engine
```

## Plugin model

A plugin may contain three kinds of contribution:

1. **Backend** — optional WASM using RunPilot host capabilities.
2. **GUI** — optional JavaScript/CSS owning navigation entries, pages, Overview cards and specialized UI.
3. **Settings** — plugin-owned settings rendered inside the common Settings shell.

A plugin may be platform-independent, Linux-only or Windows-only. Compatibility is declared in its manifest; incompatible plugins are not activated.

## Host capabilities

WASM modules do not receive native Go objects, unrestricted memory access or direct access to RunPilot internals. Host functionality is exposed through a small versioned API, expected to grow around real needs:

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

All loaded plugins may use all published capabilities. There is intentionally no manifest permission matrix. The OS account running RunPilot remains the security boundary.

Capabilities must nevertheless be narrow, explicit and stable. Avoid generic syscall-style escape hatches and validate inputs at the capability boundary.

## Browser/server communication

RunPilot uses one authenticated WebSocket connection for browser/server application communication. Feature-specific REST APIs are not part of the target architecture.

A request identifies a plugin and method:

```json
{"id":"42","plugin":"docker","method":"containers.list","params":{}}
```

Responses use the same correlation ID; plugins may also publish asynchronous events. The protocol owns structured errors, timeouts, event routing and cancellation where useful. Binary or streaming workloads should extend the same WebSocket with framed binary messages rather than introduce feature-specific transports.

## GUI ownership

The web shell owns layout mechanics, not feature pages. Plugins register navigation entries and complete pages. An empty installation may contain only framework-owned surfaces such as Overview, Settings and plugin management.

Feature-specific rendering must not be hard-coded into the core shell.

## Shared GUI library

RunPilot provides a shared frontend design system. Plugins should use common components such as Card, Button, Badge, Table, Modal, FormField, Tabs, EmptyState, confirmation and notification controls whenever practical.

The frontend API should expose stable extension surfaces such as:

```text
runpilot.ui
runpilot.navigation
runpilot.overview
runpilot.settings
runpilot.ws
```

Custom plugin CSS is allowed for specialized UI, but it must consume public design tokens instead of hard-coding the application style.

## Theme and style system

Visual style is a core service and must be replaceable without changing feature plugins. Plugins must not assume concrete colors, fonts, spacing, border radii, shadows or light/dark backgrounds.

Style and color scheme are separate concepts. A **style** controls typography, density, spacing, component shape and visual character; a **color scheme** selects System, Light or Dark within the active style.

The first implementation may ship only RunPilot Default, but it must use the same public token contract that future external styles can override.

A future non-executable theme package may look like:

```text
my-theme.rptheme
├── theme.yaml
└── theme.css
```

Themes are not normal WASM plugins and execute neither backend code nor JavaScript.

## Overview

Overview is a core layout/extension host; its cards are plugin contributions. CPU/memory/disk cards belong to a system plugin, Docker status to Docker, and task status to Tasks. The core does not understand card semantics.

## Settings

Settings is a core shell with framework-owned sections for RunPilot, Appearance and plugin management. Plugins register their own settings sections. Plugins own feature-specific schemas and editors; the core provides persistence primitives and common presentation.

## Persistence

Framework configuration and plugin enablement remain under the RunPilot data directory. Mutable plugin data belongs below a plugin-scoped data location. Plugins should use host storage/config capabilities rather than depend on internal file layouts.

## Security model

RunPilot normally runs without root/Administrator privileges. The service account's OS permissions are authoritative. WASM still isolates plugin memory and prevents direct coupling to Go internals, but it is not intended as a per-plugin trust policy.

Host capabilities must validate inputs and preserve RunPilot invariants. Browser/server authentication applies to the shared WebSocket.

## Architectural guardrails

1. Keep feature semantics out of core whenever they can live in a plugin.
2. Prefer a small stable capability API over Go internals or generic syscalls.
3. Do not add per-plugin permission complexity without a concrete future requirement.
4. Keep browser/server application communication on the common WebSocket.
5. Make navigation pages, plugin settings and Overview cards plugin-owned.
6. Keep shared layout, UI components and theme tokens core-owned.
7. Never hard-code the default visual style into feature plugins.
8. Preserve Windows/Linux support and declare plugin platform constraints explicitly.
9. Migrate incrementally; remove old core implementations only after plugin parity and tests.
10. Treat the plugin API, WebSocket protocol, capability API and design tokens as versioned public contracts.
