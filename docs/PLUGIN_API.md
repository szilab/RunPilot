# RunPilot plugin contracts

This document is the technical reference for the implemented plugin contracts.
For architecture and migration direction see [ARCHITECTURE.md](ARCHITECTURE.md);
for package authoring see [PLUGINS.md](PLUGINS.md); for publication and catalog
behavior see [PLUGIN_REGISTRY.md](PLUGIN_REGISTRY.md).

## Version domains

The following versions are deliberately independent:

- manifest schema: `apiVersion: runpilot.plugin/v1`;
- raw WASM ABI: `requires.runpilotApi` (currently ABI 1 or 2);
- backend contract: SemVer range, host currently `1.0.0`;
- frontend contract: SemVer range, host currently `1.0.0`;
- plugin package: independent strict SemVer.

The manifest schema name does not imply raw WASM ABI 1.

## Manifest

`plugin.yaml` is decoded strictly. Supported top-level fields are
`apiVersion`, `id`, `name`, `version`, `description`, `requires`,
`platforms`, `backend`, and `frontend`.

```yaml
apiVersion: runpilot.plugin/v1
id: example
name: Example
version: 1.2.0
requires:
  backend: ">=1.0.0 <2.0.0"
  frontend: ">=1.0.0 <2.0.0"
  runpilotApi: 2
platforms: [linux, windows]
backend:
  module: backend/plugin.wasm
frontend:
  module: web/plugin.js
  stylesheet: web/plugin.css
```

Backend and frontend are optional independently. Their matching contract range
is required when the component exists. A backend also declares a supported raw
WASM ABI. Omitted `platforms` means platform-independent.

Declared assets must exist in the package. Package validation also rejects
unsafe paths, duplicate/reserved entries, symlinks, oversize archives and
incompatible platform/contract requirements.

## WASM lifecycle

Backend modules execute through wazero. RunPilot serializes lifecycle entry per
loaded module, so ordinary plugin in-memory state does not need concurrent
access protection.

The lifecycle exports are:

```text
runpilot_init
runpilot_call
runpilot_shutdown
runpilot_event     # optional
```

Calls are bounded by the runtime timeout. Missing exports, wrong signatures,
traps, malformed JSON and timeouts become controlled plugin failures rather
than process failures.

### ABI 1 compatibility

ABI 1 uses the original linear-memory allocation convention:

```text
runpilot_alloc(size: i32) -> i32
runpilot_init(ptr: i32, len: i32) -> i64
runpilot_call(ptr: i32, len: i32) -> i64
runpilot_shutdown(ptr: i32, len: i32) -> i64
runpilot_event(ptr: i32, len: i32) -> i64   # optional
```

The returned `i64` packs response pointer and length. The host copies the
response before the next lifecycle call. This path remains isolated for
compatibility and is not the preferred first-party ABI.

### ABI 2

ABI 2 is the preferred first-party path. Lifecycle exports receive one opaque
invocation handle and return no value:

```text
runpilot_init(handle: i32)
runpilot_call(handle: i32)
runpilot_shutdown(handle: i32)
runpilot_event(handle: i32)   # optional
```

Each invocation owns host-side input/output state. The `runpilot` import
module exposes:

```text
input_len(handle: i32) -> i32
input_read(handle: i32, dstPtr: i32, dstLen: i32) -> i32
output_write(handle: i32, srcPtr: i32, srcLen: i32) -> i32

host_call(handle: i32, reqPtr: i32, reqLen: i32) -> i32
response_len(handle: i32, response: i32) -> i32
response_read(handle: i32, response: i32, dstPtr: i32, dstLen: i32) -> i32
response_drop(handle: i32, response: i32) -> i32
```

Lifecycle and capability payloads are limited to 16 MiB. One lifecycle output
may be written per invocation. Capability response handles are scoped to the
invocation and are cleaned automatically when it ends.

The ownership rule is simple:

> A pointer passed across ABI 2 is valid only for that synchronous import call.
> The receiving side copies the bytes before returning and never retains the
> pointer or a memory-backed view.

The first-party TinyGo SDK in `internal/pluginapi` wraps the raw ABI with
`ReadInput`, `WriteOutput`, and `CallHost`. Capability errors retain a
stable code/message/details object; ABI transport failures are reported
separately. First-party backend code should use the SDK rather than manipulate
linear-memory pointers or response handles directly.

The nonpublic System fixture is the reference ABI-2 implementation.

## Lifecycle envelope

RunPilot sends JSON lifecycle input containing the raw ABI/lifecycle API version
plus operation data. A normal call is conceptually:

```json
{"apiVersion":2,"operation":"status.get","request":{}}
```

`runpilot_event` uses the same envelope: `operation` is the generic event
name and `request` is its payload.

## Host capability protocol

Capability requests use JSON:

```json
{"apiVersion":1,"capability":"system.status","params":{}}
```

Responses are:

```json
{"ok":true,"result":{}}
```

or:

```json
{"ok":false,"error":{"code":"...","message":"...","details":{}}}
```

The capability protocol version is separate from the raw WASM ABI.

Implemented generic capability work includes logging, host status,
plugin-namespaced JSON storage, plugin-owned scheduler registrations, asynchronous
process execution/status/termination, owner-scoped execution history, and
browser event publication. Config
hooks exist in the host interface but are not yet a general plugin configuration
service. Add new capability methods only for real reusable plugin needs.

Validation-style failures (bad interpreter, invalid schedule, unknown
execution/process) return a stable code such as `invalid_argument` or
`not_found` with an actionable message; other failures return code `failed`.

### Scheduler callbacks

`scheduler.register` registers an ID, existing RunPilot schedule definition,
callback name and optional data. IDs are scoped to the calling plugin.
`scheduler.remove` and `scheduler.list` use the same scope.

A fire is delivered through `runpilot_event` as a generic scheduler event.
Registrations are runtime state; a plugin should reconstruct them from its own
durable state during initialization. A plugin has no timers of its own, so a
one-shot delay is an `interval` registration removed on its first fire.
`scheduler.validate` takes `{"schedule":{...}}` and applies the same rules as
registration without creating a timer. Reloading legacy job definitions never
removes plugin registrations.

### Processes

`process.start` accepts command, arguments, working directory and environment
and returns an opaque plugin-owned process ID and its `pid`. Optional fields:

- `interpreter`: the launcher's `CommandSpec` semantics (`auto`, `direct`,
  `powershell`, `cmd`, `sh`, `bash`, `sh-inline`, `python`), with the same
  Windows/Linux behavior as legacy commands. Empty means `direct`.
- `timeoutSeconds` (0 to 7 days): the process tree is killed when it elapses;
  the exit event and status report `timedOut`.
- `historyId`: an unfinished execution owned by the caller. Combined
  stdout/stderr is then captured losslessly (bounded, see history) into that
  execution and **no** `process.stdout`/`process.stderr` callbacks are sent.
  The host also publishes a coalesced `history.output` browser event
  (`{id,size}`, at most about four per second) that carries no output.

Inputs are bounded (arguments, environment, string lengths). Status and
termination are owner-scoped; `process.status` reports `running`, `pid`,
`exitCode`, `terminated` and `timedOut`, and finished processes stay queryable
for five minutes. The exit event carries `id`, `exitCode`, `success`,
`terminated`, `timedOut` and, when captured, `historyId`.

Without `historyId`, stdout/stderr are delivered through `runpilot_event`. Output
uses bounded queues; overflow is intentionally lossy rather than blocking a
child process or growing memory without bound.

### History

`history.*` records executions per owner. The owner is always the calling plugin;
IDs and log locations are host-generated and never accept paths. Execution
metadata lives in the existing SQLite `runs` table (legacy history never lists
plugin executions) and output in `runs/plugins/<owner>/<id>.log`.

```text
history.begin   {kind, subject, label}            -> execution
history.append  {id, text}          (<= 64 KiB)   -> null
history.finish  {id, exitCode?, success?, message?} -> execution (idempotent)
history.list    {subject?, limit?}  (<= 100)      -> {executions:[...]}
history.get     {id}                              -> execution
history.output  {id, maxBytes?}     (<= 1 MiB)    -> {id, output, size, truncated}
```

An execution is `{id, kind, subject, label, startedAt, finishedAt?, exitCode?,
success?, message?}`. `kind`, `subject` are short identifiers, `label` is
truncated, `output` is the tail of the log. Each log is capped at 16 MiB (later
output is discarded after a truncation marker), the newest 200 finished
executions per owner and subject are kept, and executions left unfinished by a
previous RunPilot process are marked failed at startup. Access is serialized by
the store, so concurrent plugin calls and process capture are safe.

### Event delivery

Host callbacks are delivered per plugin in the order they were produced, through
a bounded queue. Output callbacks are dropped when the plugin falls behind;
process exit and scheduler callbacks wait for space. Callbacks produced while a
backend is still initializing (for example an autostarted process that exits
quickly) wait for initialization instead of being lost. A plugin should still
treat callbacks as at-least-once hints: make exit handling idempotent and
reconcile with `process.status` (Tasks does so on every list and every 30
seconds). On shutdown RunPilot calls `runpilot_shutdown` first, then
terminates remaining plugin processes and flushes captured logs before history
closes.

### Browser publication

`events.publish` publishes a plugin event through the shared application
WebSocket. Core supplies the plugin ID. There is no offline replay; disconnected
clients retain no backlog and slow-client queues are bounded.

## Browser protocol

The authenticated application WebSocket is `GET api/v1/ws`. The browser first
obtains a short-lived single-use ticket from authenticated
`POST api/v1/ws/ticket`; the connection reuses RunPilot's optional payload
encryption handshake.

Request:

```json
{"id":"42","plugin":"example","method":"items.list","params":{}}
```

Success:

```json
{"id":"42","result":{}}
```

Error:

```json
{"id":"42","error":{"code":"unknown_plugin","message":"..."}}
```

Event:

```json
{"plugin":"example","event":"items.changed","data":{}}
```

The common dispatcher provides correlation, structured errors, bounded calls,
plugin-failure isolation and event routing. Existing legacy feature REST
endpoints remain only while those features have not yet migrated.

## Frontend extension API

Frontend modules export:

```javascript
export function activate(runpilot) {
    // register plugin contributions
}
```

The public surface is:

```text
runpilot.ws
runpilot.navigation
runpilot.overview
runpilot.settings
runpilot.ui
```

`runpilot.ui.theme` exposes the active semantic design tokens and reports
changes when the user switches color schemes:

```javascript
const theme = runpilot.ui.theme.get();
const unsubscribe = runpilot.ui.theme.subscribe(nextTheme => {
    // Update colors in long-lived controls such as terminal emulators.
});
```

The returned object contains `scheme`, `resolvedScheme`, `fontFamily`, and a
`colors` object with `surface`, `surfaceElevated`, `text`, `textStrong`,
`textMuted`, `border`, `accent`, `selection`, and terminal palette keys
`terminalBlack`, `terminalRed`, `terminalGreen`, `terminalYellow`,
`terminalBlue`, `terminalMagenta`, `terminalCyan`, `terminalWhite`, and the
matching `terminalBright*` keys. Plugins should use these values rather than
define their own color schemes. `subscribe()` returns an unsubscribe function.

Plugins use `runpilot.ws.call()` for RPC and `runpilot.ws.on()` for events
instead of opening their own application WebSocket. Navigation/pages, Overview
cards and Settings sections are registered through the corresponding extension
hosts.

Core owns the shell and design system. Plugin CSS should use public semantic
tokens. System/Light/Dark is a color-scheme choice separate from the selected
style. Executable theme plugins are not part of the contract.

## First-party build contract

First-party WASM backends use TinyGo 0.38.0 with the repository's
`wasm-unknown` build settings (`-scheduler=none -gc=conservative`, and a larger
`-stack-size` for JSON-heavy backends such as Tasks) and are shipped precompiled inside
`.rpplugin` packages. TinyGo is build-time tooling only.

The System package is a nonpublic reference fixture for ABI, host capability,
WebSocket and frontend-extension tests. It uses ABI 2 and is excluded from the
public catalog; it should not grow into the permanent host-monitoring feature.
