# Plugin framework refactor plan

This plan migrates RunPilot from a feature-heavy core to a small plugin host. Migration is incremental: existing functionality remains until an equivalent plugin path works and is tested.

## Target outcome

Core contains only plugin runtime/lifecycle, versioned host capabilities, one authenticated browser/server WebSocket protocol, web shell/navigation, shared UI/design system, Overview/Settings extension hosts, replaceable style/theme infrastructure, and framework/plugin configuration.

Feature migrations remain future work. Host/system metrics presentation is planned through plugin/widget contributions; the System reference fixture is not a permanent production feature.

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

Phase 1 implementation: package manifests, platform status, declared-asset
validation, a linear-memory WASM ABI, a minimal `log`/`config` capability
bridge, browser protocol envelopes and semantic style tokens are now present.
The WebSocket transport and frontend registration hosts remain in their later
phases; existing product APIs and Remote compatibility stay in place meanwhile.

ABI-v2 host infrastructure and the TinyGo SDK remain implemented alongside ABI
v1. System is a nonpublic technical test fixture. No Tasks migration or System
feature development is part of the current publication/registry milestone.

## Current priority — independent publication and installation

Implemented: independent backend/frontend `1.0.0` contracts, strict package
SemVer, contract constraints, deterministic packages and release records,
repository publication policy, version-driven GitHub publication, static catalog
generation, bounded catalog/download clients, SHA-256 verification, and Settings
install/update/enable/disable/uninstall. Activation is restart-only. See
[PLUGIN_REGISTRY.md](PLUGIN_REGISTRY.md) for operational details and test commands.

System remains excluded from publication. Existing Remote scaffolds and examples
are opt-in only; release policy must be deliberately enabled when a plugin is
ready. No production release is created by local validation. Before feature
migration resumes, continue ABI stress validation and define the separately
planned widget contribution model only when needed.

## Phase 2 — WebSocket transport

Replace feature-facing browser REST calls with a common authenticated WebSocket dispatcher.

Implement request correlation, plugin/method routing, structured results/errors, asynchronous events, bounded calls/timeouts, reconnect behavior, useful cancellation, and a path for framed binary streaming on the same socket.

Static asset/bootstrap HTTP is fine; application RPC uses WebSocket.

Acceptance: browser-to-WASM calls and backend-to-browser events work; malformed/unknown calls fail cleanly; one plugin failure does not break other plugins or the socket.

## Phase 3 — capability host

Implement only capabilities required by real plugins, starting with the reference plugin. Planned families are `host.system.*`, `host.config.*`, `host.log.*`, `host.process.*`, `host.fs.*`, `host.network.*`, `host.scheduler.*`, and `host.storage.*`.

There is no per-plugin permission enforcement. All capabilities are available to loaded plugins subject to the RunPilot process account's OS permissions and capability-level validation.

Acceptance: WASM calls host functions without native Go coupling; errors are structured; calls are bounded; tests cover traps, timeouts and invalid payloads.

The first source-based backend milestone is complete for System: TinyGo 0.38.0
produces a `wasm-unknown` module through the normal ABI, including state
retained across calls and the serialized optional `runpilot_event` callback.
Plugin-namespaced JSON key/value storage is available through `host.storage`.
Generic scheduler, process, and browser-event capabilities remain prerequisites
for the Tasks migration.

## Phase 4 — themeable shared GUI

Refactor the web shell around semantic design tokens, shared components, layout primitives, System/Light/Dark schemes and a selectable style abstraction.

RunPilot Default must use the same public tokens future external styles will override. Plugin CSS may handle specialized content but must not duplicate the application design system.

Prepare, but do not require yet, a non-executable `.rptheme` format.

Acceptance: changing style tokens updates plugin UI built from shared components; migrated plugins do not depend on hard-coded default colors; current compact RunPilot visual quality/density is preserved; theme switching does not require plugin rebuild.

## Phase 5 — frontend extension hosts

Implement stable registration APIs for navigation/full pages, Overview cards, Settings sections, shared UI access and WebSocket client access.

Acceptance: a plugin can add its menu/page without core feature code, use shared components, register settings inside common Settings and own Overview cards.

## Phase 6 — retain the System technical fixture

Keep the source-derived ABI fixture, repeated real-WASM/WebSocket tests and
frontend extension coverage. Do not expand System into a user-facing production
plugin, add metrics widgets, or migrate its ABI as part of registry work. It is
not bundled/auto-enabled on fresh installations or published to the catalog.

## Phase 7 — migrate existing features incrementally

Suggested order:

1. Tasks and execution history (after the async ABI-v2 stress gate);
2. Storage;
3. Docker;
4. Terminal;
5. Software Management/Scoop;
6. backup integrations;
7. Remote Access providers: Xpra, RDP, VNC.

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
