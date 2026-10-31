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

- RunPilot has a single deployment: changes need no data or configuration
  migration. Remove legacy code once its plugin replacement exists instead of
  migrating its state.
- Every product feature is a plugin; no legacy core feature remains. Backup is
  the first-party `backup` plugin with its own schedules, independent of
  Tasks; it keeps repository passwords and keys outside RunPilot (password or
  environment files read at run time) and restores only into a new or empty
  folder. Xpra is a protocol of the `remote` plugin. File management is the first-party `files` plugin on the generic
  `workspace.*` file, operation and transfer capabilities, without
  Docker-volume or backup-vault handling. Software management is not planned.
- New feature semantics belong in plugins, not in core. Core may expose narrow,
  reusable host capabilities.
- Fresh installations allow administrator setup for five minutes after startup;
  when the window expires, setup remains locked until the user restarts the
  service to open another window. Browser management uses password
  authentication and revocable server sessions. Bearer API access is a separate
  setting.
  Reverse-proxy SSO (Settings, overridden by `RUNPILOT_TRUSTED_PROXIES` and
  `RUNPILOT_PROXY_AUTH_HEADER`) trusts only the direct TCP peer, never
  forwarding headers, and must issue
  ordinary server sessions rather than bypass CSRF or session checks.
- Resolve the mutable application data root from explicit CLI override, environment,
  persisted deployment locator, then platform default. Keep the locator independent
  from the selected data root; never infer the root from process workingDirectory.
- Plugin history stays owner-scoped under plugin data. Never recreate the
  obsolete `<dataDir>/legacy` or `<dataDir>/internal/runs` layouts or delete
  existing data through an implicit startup cleanup.
- Before building a dedicated integration for an application with a capable web
  GUI, consider `web.apps` when secure access to that GUI is sufficient.
  Dedicated plugins fit structured control, automation, data integration,
  lifecycle management, or RunPilot-native behavior beyond the existing UI.
- Keep Web Apps compatibility generic: prefer HTTP, WebSocket and reverse-proxy
  standards over per-application hacks. RunPilot's own configured `basePath`
  remains supported at `/` and nested prefixes. Compatibility for proxied apps
  below paths such as `/p/app` is best effort; do not spend substantial time on
  it unless that exact application/path is requested. Do not add response-body
  URL rewriting for arbitrary apps.
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
- A plugin must package its feature-specific browser dependencies and must not
  depend on globals or static assets loaded by core pages.
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
  user-facing host-monitoring feature. Overview's built-in host dashboard owns
  the current host metrics UI; the target is an Overview built from widgets,
  including host metrics and the launcher.
- Prefer the Go standard library; add dependencies when they materially reduce
  complexity or correctness risk.
- Keep platform-specific behavior behind small adapters/build constraints and
  preserve Windows SCM and Linux systemd user-service behavior.
- The container image (root `Dockerfile` with `packaging/docker`) is the
  preferred Linux deployment, including NAS devices; vendor-specific NAS
  packages are not maintained. The image must run
  with defaults (same-path data mount, optional Docker socket); keep privilege
  drop, socket group and init handling in the entrypoint, and keep
  container-aware metrics generic in `internal/platform`.

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
