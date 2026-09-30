# RunPilot agent notes

RunPilot is a Windows/Linux Go application with an embedded web UI. The target
architecture is plugin-first: core provides the runtime/framework and plugins
provide product features.

Before architectural work, read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
For plugin implementation details use [docs/PLUGINS.md](docs/PLUGINS.md),
[docs/PLUGIN_API.md](docs/PLUGIN_API.md), and
[docs/PLUGIN_REGISTRY.md](docs/PLUGIN_REGISTRY.md). Do not create a second
architecture description elsewhere.

## Engineering rules

- Preserve existing behavior while a feature still uses the legacy core
  implementation. Remove legacy code only after plugin parity and tests.
- New feature semantics belong in plugins, not in core. Core may expose narrow,
  reusable host capabilities.
- Backend contracts and host capabilities are intentionally extensible. When a
  plugin needs functionality that existing capabilities cannot correctly
  provide, first design a new reusable host capability. A capability is
  appropriate when it represents generic infrastructure reusable by multiple
  features; do not duplicate platform-specific infrastructure in plugins just
  to avoid extending a contract, or move feature semantics into core because a
  capability is missing. For example, an interactive PTY-backed process session
  may be a core capability for terminal-like features, while terminal page
  behavior remains plugin-owned.
- New plugin browser RPC/events use the common authenticated application
  WebSocket. Do not add feature-specific REST APIs or WebSockets.
- Keep raw WASM ABI, backend contract, frontend contract, RunPilot version and
  plugin SemVer as separate version domains.
- ABI v2 is the preferred first-party backend path; ABI v1 remains compatibility
  code. Do not reintroduce retained ABI-v2 WASM pointer lifetimes or static
  linear-memory arenas. Stateful TinyGo backends build with `-gc=conservative`
  (the `wasm-unknown` default collector never frees memory).
- Plugin execution logs/history use the generic owner-scoped `history.*`
  capability, never `plugin-data/<id>/storage.json`.
- Plugins are trusted packages. Do not add a per-plugin permission system,
  generic syscall escape hatch, native Go plugin loading, or automatic
  privilege escalation.
- Plugin activation is restart-based. Do not add hot loading without an explicit
  architectural decision.
- First-party plugins stay in this repository but are independently versioned
  and published through the plugin catalog. Publication is explicit and
  immutable per plugin version.
- The System package is a nonpublic ABI/frontend test fixture, not the intended
  user-facing host-monitoring feature. Host metrics UI will be designed as
  widgets when that work starts.
- Prefer the Go standard library; add dependencies when they materially reduce
  complexity or correctness risk.
- Keep platform-specific behavior behind small adapters/build constraints and
  preserve Windows SCM and Linux systemd user-service behavior.
- When touching unmigrated Docker, Backup, Terminal or Remote code, preserve its
  existing safety boundaries and regression tests rather than expanding the
  legacy core domain.

## Validation

Before finishing a change, run what applies:

- `gofmt` on changed Go files
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- Linux build and Windows amd64 cross-build for platform/runtime changes
- `node --check` for changed frontend JavaScript
- TinyGo build/generation when a WASM backend changes
- `git diff --check`

Prefer automated contract/integration tests over manual verification.
