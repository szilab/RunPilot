# RunPilot architecture

## Direction

RunPilot is a lightweight Windows/Linux host-management application that is
being refactored into a small **plugin host and management framework**.

> **RunPilot provides the framework; plugins provide the features.**

The target core owns infrastructure that multiple features need: plugin
installation/runtime, versioned host capabilities, one authenticated application
WebSocket, the web shell, shared UI/theme primitives, and framework settings.
Tasks, Docker, Terminal and Remote Desktop are plugin-owned in the normal UI. Storage,
Software and Backup remain transitional core implementations without normal
navigation. Xpra is the temporary Remote exception described below.

The migration is incremental. Legacy backends, APIs and configuration remain
operational until plugin parity and any required state migration decision. New
architecture work must not use that transitional state as a reason to add more
feature semantics to core.

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
| Backend contract | Host capability/runtime compatibility; currently `1.1.0` (1.1 added network tools) |
| Frontend contract | Public browser extension compatibility; currently `1.0.0` |
| Plugin version | Independent SemVer release of one plugin; the source of truth is `plugins/<id>/plugin.yaml` |

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

The exact published names are indexed in [PLUGIN_API.md](PLUGIN_API.md) and
served from one table in `internal/plugins/capabilities.go`. There is no plugin
configuration capability; plugins persist settings through `storage.*`.

`host.history.*` is a generic, owner-scoped execution record with captured
output. Each plugin's SQLite history and run logs live in its mutable data
directory. Plugins never supply filesystem paths.

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
New plugin features must not create feature-specific REST APIs or WebSockets.
The REST routes of unmigrated legacy features (Docker manager, Storage,
Software, Backup jobs, Xpra client) are not mounted; their handlers remain in
`internal/web/legacy_*.go`, registered only by tests through
`registerLegacyRoutes`, until each plugin migration reaches parity.

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

Overview currently presents a built-in, host-only monitoring dashboard with
CPU, memory, primary-filesystem and optional GPU cards, followed by always-visible
filesystem capacity cards using the same responsive columns. Host identity stays
in Settings. Its metrics reuse the existing platform
adapters. When RunPilot itself runs in a container, the Linux adapter reports
the container's cgroup CPU and memory use against its limits (host totals as
context), lists the container root and mounted directory filesystems, and
omits the host-wide load average; Overview labels the readings accordingly. Sparklines retain at most 32 actual successful samples in browser
local storage, scoped to the application base path and host name. Collection
continues on other pages while the authenticated app is visible; reloads restore
the retained samples. Hidden or closed browsers do not collect measurements.
This is recent browser history, not a server metrics archive; it does not use
execution history or synthesize measurements. Plugin summary widgets appear below the
host dashboard through the generic frontend widget registration contract. Their
mount, refresh, and disposal follow Overview visibility. Navigation shortcut
metadata remains accepted for compatibility; Overview has no quick-action panel.
Existing launcher configuration
remains separate; the visual redesign does not change its semantics. The
internal System package fixture is used for tests, not as a user-facing feature.

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
until restart. Mutable `plugins/<id>/data` is retained on uninstall.

Publication policy is repository-owned. The System fixture and incomplete Remote
scaffolds are intentionally excluded from the public catalog.

Operational details belong in [PLUGIN_REGISTRY.md](PLUGIN_REGISTRY.md); plugin
authoring belongs in [PLUGINS.md](PLUGINS.md); raw ABI/protocol details belong
in [PLUGIN_API.md](PLUGIN_API.md).

## Migration direction

Core authentication uses one local administrator account, Argon2id password
hashes, revocable HttpOnly server-managed browser sessions, CSRF checks and the
common application WebSocket. Browser sessions are stored as secret hashes
under `<dataDir>/internal`, survive service restarts, and expire 24 hours after
login; logout and password changes revoke them. Existing bearer API-token
clients remain a separately
controlled compatibility path. Fresh installations allow administrator setup
for five minutes after startup; when the window expires, setup remains locked
until the user restarts the service to open another window.

Optional reverse-proxy single sign-on is configured in Settings (stored in
`runpilot.yaml`, applied without restart) or through
`RUNPILOT_TRUSTED_PROXIES` and `RUNPILOT_PROXY_AUTH_HEADER`, which take
precedence and make the Settings values read-only. On `/api/` requests
whose direct TCP peer is a trusted proxy, a header value equal to the
administrator username creates an ordinary server session, so CSRF, WebSocket
tickets and revocation are unchanged; forwarding headers are never used to
identify the peer. A trusted proxy asserting a different user is
authoritative and the request is refused, even with a session or bearer token.
Proxy-created sessions are evicted first when the session table is full.

The mutable application data root is selected independently from executable
binaries and process working directories. Explicit CLI override, environment,
persisted deployment locator and platform default determine its location in that
order. The locator is deployment metadata outside the selected root. Settings
directory changes are restart-bound and only update the locator; no data is
copied or moved. Users prepare existing data in the selected directory
manually, or start with an empty directory. Initial administrator setup retains
its copy-verified relocation into a new directory. Docker Compose layouts with
relative bind mounts require a separate manual migration.

The next work is feature migration, not further expansion of the core plugin
framework. Migrate one feature at a time and remove its legacy core/API code only
after plugin parity and automated regression coverage.

Tasks, Docker, Terminal and Remote Desktop have migrated in the normal UI. Storage is the
next active feature migration. Software, Backup and Xpra follow as separate
plugin work; see the [backlog](../BACKLOG.md).

### Docker status

Docker has a Linux-only first-party plugin (`plugins/docker`,
ABI v2) for Compose projects, containers, volumes, networks and interactive
container terminals. It owns the normal `docker` page and uses generic bounded
`process.run`, owner-scoped `workspace.*`, and `process.session.*` capabilities.
Existing `<dataDir>/compose` control files are copied into the Docker plugin
workspace on startup without overwriting plugin-owned projects or deleting the
source. Container data stays at its original path. The copy records its legacy
origin so Compose actions retain relative bind mounts and existing containers
can be verified against their original Compose directory; external name
collisions remain read only.
External containers allow bounded log viewing but no lifecycle mutations or
interactive terminal.
The legacy Docker manager remains transitional because hidden Storage still
uses Docker-volume integration. Its HTTP handlers are kept but not mounted, and
the plugin UI does not use them.

### Remote status

`remote` is the publishable ABI-v2 Remote Desktop plugin for RDP and VNC.
It packages Guacamole and noVNC clients and owns targets for both protocols. Target
status and Wake-on-LAN are plugin semantics on top of the generic
`network.probe`, `network.neighbor.lookup` and `network.wake` capabilities
(backend contract 1.1).
The legacy RDP and VNC providers, browser transports, and combined Remote page
have been removed from core. Xpra's legacy provider/runtime remains
internal with no normal UI or navigation. Its plugin migration is deferred
until reusable process, listener, and browser-publication lifecycle capabilities
are designed; see the concise [backlog](../BACKLOG.md).

### Tasks status

Tasks has a publishable first-party plugin (`plugins/tasks`, ABI v2, never
auto-enabled). It covers continuous command tasks and
scheduled command tasks, owns their definitions and semantics, and talks to the
browser only through the common WebSocket. It runs on the generic storage,
scheduler, process, history and event capabilities without adding a Task host
capability.

The plugin owns the normal `tasks` page. The pre-plugin process manager, command
jobs, Tasks REST endpoints, history API, and Tasks/Backup dialogs have been
removed. Opening an older config drops its
obsolete process definitions and command jobs without changing its token.
**Backup is not part of the Tasks plugin**: typed Backup jobs, their scheduler,
provider implementations, and logs under `<dataDir>/internal/runs` remain in
core until their own migration. Startup never creates `<dataDir>/legacy` or a
global run history database.

The capability changes made for Tasks are generic framework infrastructure:
interpreter-aware `process.start` (launcher semantics), process timeouts and
captured output, `scheduler.validate`, the owner-scoped `history.*` family, and
ordered/bounded event delivery. See [PLUGIN_API.md](PLUGIN_API.md).

The async ABI-v2 process/scheduler/event path remains covered by real-WASM
race/integration tests.

First-party plugins do not contribute Overview widgets; Overview shows only
the built-in host dashboard. The widget registration contract remains available
to plugins, and a repository test keeps first-party frontends from using it.

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

### Web Apps

The publishable `web.apps` ABI-v2 plugin is a working first-party
capability. It owns target CRUD, persisted target configuration and its browser
UI, and established reusable browser publication,
restricted stream ticket and native Go HTTP gateway capabilities; core owns no
application-specific target behavior. Restricted browser publications bootstrap
from the RunPilot base URL using a fragment and transition to their virtual path
only after a basePath-scoped worker has taken control. The generic root hook
bypasses management authentication/initialization for these tabs. One owner-bound
browser runtime shares that worker across explicitly bound client sessions;
normal management clients retain ordinary networking. The plugin renders HTTP
applications locally through synthetic streaming responses. Upstream application
HTTP and WebSocket payload crosses the restricted, payload-encrypted common
application WebSocket, including when normal RunPilot payload encryption is
disabled. Root-fragment bootstrap supports deployment through URL-sensitive
proxies and security gateways. HTTPS serves
package bootstrap/worker assets only. Target tabs receive only a single gateway
capability; old API tokens are cleared from browser storage and target tabs
launch with `noopener`. Browser authentication credentials are never stored in
localStorage or sessionStorage. Web Apps can
avoid a dedicated integration when secure access to an application's existing
GUI is sufficient; dedicated plugins remain appropriate for structured
automation and control. RunPilot's own `basePath` remains supported. Arbitrary
nested application base paths are best effort; relative URLs, native base URL
settings and standard proxy headers are the intended compatibility model.
Response-body URL rewriting is an intentional boundary.
