# Plugin framework refactor plan

This plan migrates RunPilot from a feature-heavy core to a small plugin host. Migration is incremental: existing functionality remains until an equivalent plugin path works and is tested.

## Target outcome

Core contains only plugin runtime/lifecycle, versioned host capabilities, one authenticated browser/server WebSocket protocol, web shell/navigation, shared UI/design system, Overview/Settings extension hosts, replaceable style/theme infrastructure, and framework/plugin configuration.

Tasks, System monitoring, Storage, Terminal, Docker, Software Management, Backup and Remote providers become first-party plugins.

## Phase 0 — restore a green baseline

Before structural work:

- fix current compile/test blockers on `develop`;
- run `gofmt`, `go test ./...`, `go vet ./...`;
- run race tests where supported;
- validate frontend JavaScript;
- build Linux and cross-build Windows.

Do not classify a checked-in source error as a stale local snapshot without verifying it.

## Phase 1 — freeze framework contracts

Define and document:

1. plugin manifest and platform compatibility;
2. WASM lifecycle/data ABI;
3. host capability naming and error model;
4. WebSocket request/response/event envelope;
5. frontend activation API;
6. navigation/page registration;
7. Settings registration;
8. Overview-card registration;
9. shared UI component API;
10. public design-token/theme contract.

Keep contracts deliberately small. Remove the target architecture's per-plugin permission matrix and introduce no new feature-specific REST APIs.

## Phase 2 — WebSocket transport

Replace feature-facing browser REST calls with a common authenticated WebSocket dispatcher.

Implement request correlation, plugin/method routing, structured results/errors, asynchronous events, bounded calls/timeouts, reconnect behavior, useful cancellation, and a path for framed binary streaming on the same socket.

Static asset/bootstrap HTTP is fine; application RPC uses WebSocket.

Acceptance: browser-to-WASM calls and backend-to-browser events work; malformed/unknown calls fail cleanly; one plugin failure does not break other plugins or the socket.

## Phase 3 — capability host

Implement only capabilities required by real plugins, starting with the reference plugin. Planned families are `host.system.*`, `host.config.*`, `host.log.*`, `host.process.*`, `host.fs.*`, `host.network.*`, `host.scheduler.*`, and `host.storage.*`.

There is no per-plugin permission enforcement. All capabilities are available to loaded plugins subject to the RunPilot process account's OS permissions and capability-level validation.

Acceptance: WASM calls host functions without native Go coupling; errors are structured; calls are bounded; tests cover traps, timeouts and invalid payloads.

## Phase 4 — themeable shared GUI

Refactor the web shell around semantic design tokens, shared components, layout primitives, System/Light/Dark schemes and a selectable style abstraction.

RunPilot Default must use the same public tokens future external styles will override. Plugin CSS may handle specialized content but must not duplicate the application design system.

Prepare, but do not require yet, a non-executable `.rptheme` format.

Acceptance: changing style tokens updates plugin UI built from shared components; migrated plugins do not depend on hard-coded default colors; current compact RunPilot visual quality/density is preserved; theme switching does not require plugin rebuild.

## Phase 5 — frontend extension hosts

Implement stable registration APIs for navigation/full pages, Overview cards, Settings sections, shared UI access and WebSocket client access.

Acceptance: a plugin can add its menu/page without core feature code, use shared components, register settings inside common Settings and own Overview cards.

## Phase 6 — reference `system` plugin

Build a small complete first-party plugin before complex migrations.

Suggested scope:

- real WASM backend;
- hostname/platform/basic host metrics via `host.system.*`;
- System navigation page;
- CPU/memory/disk Overview cards;
- one small Settings contribution if useful;
- WS RPC and at least one event/update path;
- Windows + Linux compatibility;
- deterministic package/release output.

The goal is architectural proof, not a large monitoring feature.

Acceptance: install/enable/restart loads WASM, renders plugin-owned page/cards/settings through shared UI, communicates only through common WS, and disables without feature-specific core code.

## Phase 7 — migrate existing features incrementally

Suggested order:

1. System/host information;
2. Tasks and execution history;
3. Storage;
4. Docker;
5. Terminal;
6. Software Management/Scoop;
7. backup integrations;
8. Remote Access providers: Xpra, RDP, VNC.

For each migration: identify generic capability needs; add only missing host methods; implement backend WASM; move UI/settings/Overview contributions into the plugin; add parity/regression tests; validate applicable platforms; remove old core code only after parity passes.

Do not keep permanent compatibility shims that reintroduce feature knowledge into core.

## Phase 8 — remote provider cleanup

Remote providers are intentionally late because they exercise process, network, streaming/session lifecycle and specialized UI. Migrate one provider first, likely Xpra; do not migrate Xpra/RDP/VNC simultaneously.

After parity, remove provider-specific core rendering and execution code while retaining only genuinely generic capabilities.

## Phase 9 — theme packages

After the public token/style contract has survived real plugins, consider external `.rptheme` installation. Theme packages contain metadata plus CSS/token overrides only and execute neither WASM nor arbitrary JavaScript.

## Validation throughout

Every phase should leave `develop` usable. Run where applicable:

```text
gofmt
go test ./...
go test -race ./...
go vet ./...
Linux build
Windows cross-build
node --check for frontend modules
```

Add focused automated tests for public contracts rather than relying on manual browser checks. Preserve the existing compact RunPilot visual direction unless a task explicitly requests redesign.

## Explicit non-goals

During this refactor do not add:

- per-plugin permissions;
- root/Administrator escalation;
- hot plugin loading/unloading;
- a generic syscall capability;
- plugin-specific HTTP/REST APIs;
- a frontend framework migration solely for this work;
- executable theme packages;
- simultaneous migration of every existing feature.

The objective is a small understandable framework with strong extension contracts, not a general-purpose plugin platform with every possible isolation or lifecycle feature.
