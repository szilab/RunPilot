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
process execution/status/termination, owner-scoped execution history, browser
event publication, and owner-scoped TCP/TLS network streams. Config
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

### Network streams

`network.stream.*` is generic TCP/TLS byte-stream infrastructure with no feature
protocol or target semantics. IDs are cryptographically random and scoped to the
calling plugin; other plugins cannot read, write, close, or attach to them. A
plugin runtime stop closes every stream owned by that plugin.

```text
network.stream.open  {host, port, connectTimeoutSeconds, tls?} -> {id}
network.stream.read  {id, maxBytes, timeoutMilliseconds, peek?, offset?} -> {data, eof, state}
network.stream.write {id, data} -> {}
network.stream.close {id} -> {} (idempotent; foreign-owner IDs still fail)
```

`host` and optional TLS `serverName` are hostnames or IP literals, not URLs.
Ports are 1-65535; connection timeout is 1-30 seconds. TLS uses normal system
certificate and hostname validation with a TLS 1.2 minimum. Reads return
base64-encoded chunks of 1-65536 bytes and wait at most 5 seconds; writes accept
at most 65536 decoded bytes. Optional `peek: true` with a nonnegative `offset`
returns a bounded view into buffered input without consuming it; offset plus
requested bytes cannot exceed 65536. This supports bounded protocol
negotiation while leaving subsequent bytes available to a browser attachment.
Active stream counts and retained closed records are bounded. WASM receives no
Go connection or socket handle.

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

Navigation entries may use `icon: "monitor"` for the shared desktop icon.
Other icon strings continue to render as text glyphs. Settings forms appear
inside their owning plugin's card only while that plugin is enabled. Plugins
provide their heading, fields, and actions using the host's shared responsive
layout without imposing a custom section width.

A navigation entry may provide `headerActions(root)` to add page-specific
buttons to the application header beside the theme switcher. The host clears
this slot when the user leaves that page:

```javascript
runpilot.navigation.register({
    id: "example", title: "Example", render,
    headerActions(root) {
        const button = document.createElement("button");
        button.className = "button primary small";
        button.textContent = "Add item";
        button.addEventListener("click", addItem);
        root.append(button);
    },
});
```

Interactive plugins may use `runpilot.ui.createInteractiveSessionView()` for a
provider-neutral session shell and measured surface:

```javascript
const view = runpilot.ui.createInteractiveSessionView({
  container, title: "Desktop", onBack, onDisconnect,
  onFullscreenChange: isFullscreen => { /* provider-specific response */ },
});
view.setStatus("connecting"); // connecting, connected, disconnected, error
view.setLoading(true, "Connecting…");
view.setError("Connection failed");
const stopObserving = view.surface.onResize(({width, height}) => {
  // The plugin decides whether and how its protocol consumes these pixels.
});
view.surface.fitScale({width: remoteWidth, height: remoteHeight});
await view.surface.enterFullscreen();
view.surface.focus();
view.dispose(); // idempotent; removes observers and listeners
```

The returned view exposes its root `element` so a plugin can reattach it when
its navigation page is rerendered. `actions` is the toolbar slot for
provider-owned controls. `surface.element` is the focusable content region;
`getSize()` reports its available CSS-pixel width and height. Resize
notifications observe the surface, toolbar, containing layout, window and
fullscreen changes; changes are animation-frame coalesced, debounced by 60 ms,
and duplicate dimensions are suppressed. Fullscreen state and focus restoration
are shared behavior. The helper does not call RDP, VNC, Xpra, or other protocol
APIs; the provider owns all resize and rendering policy.

Interactive plugins may use `runpilot.ws.openStream(plugin, streamId)` to attach
one owned stream to that same authenticated socket:

```javascript
const stream = await runpilot.ws.openStream("remote.rdp", streamId);
stream.ondata = bytes => { /* Uint8Array */ };
stream.onclose = event => { /* closed or disconnected */ };
stream.send(bytes); // Uint8Array, ArrayBuffer, or typed-array view; <= 32 KiB
stream.close();
```

The API uses a text `stream.attach` control containing the plugin owner and
opaque stream ID, then binary frames for data. Frames contain `RPS1`, a direction
byte (1 client-to-server, 2 server-to-client), one stream-ID-length byte, the
UTF-8 stream ID, and raw payload. Existing optional payload encryption protects
these frames too. A text `stream.close` closes an attachment. Malformed,
unknown, or foreign-owned streams are rejected. Disconnects, failed writes, and
stalled readers close streams; frame sizes and buffering are bounded, and bytes
are not silently dropped. The owner receives `network.stream.closed` with only
the stream ID for feature-local state cleanup.

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

## Browser publications and restricted streams

These additive backend-contract 1.x capabilities provide reusable browser mounts
and HTTP infrastructure. The host supplies the calling plugin owner; none of
these operations grants a normal RunPilot login.

```text
browser.publication.register {mountPath, bootstrap, worker} -> {id, owner, mountPath, bootstrap, worker}
browser.publication.remove   {id} -> {}
http.gateway.open           {publicationId, upstreamURL, upstreamBasePath} -> {id, publicPrefix, publicationId}
http.gateway.close          {id} -> {}
browser.stream.ticket       {streamId} -> {ticket}
```

A publication mounts package-owned `web/` bootstrap HTML and worker JS under a
normalized relative mount. Root, overlapping mounts and framework paths (`api`,
`plugins`, `vendor`, `healthz`, root filenames) are reserved. Mount paths
use literal URL-safe characters, with no percent escapes or traversal. Host base
paths and upstream base paths also preserve Unicode/spaces with URL escaping. Mounts
are runtime state and must be reconstructed during initialization. The HTML's
`__RUNPILOT_PUBLICATION__` placeholder and the worker's
`self.RUNPILOT_PUBLICATION` receive `{owner, publicationId, publicPrefix,
basePath, assets}`. The worker script is served at
`publicPrefix/__runpilot__/sw.js?publication=<id>` with
`Service-Worker-Allowed: publicPrefix/`; stale generations return 410. HTTPS
serves only package initialization, never upstream payloads. Removing a mount
closes its gateways. Plugin shutdown removes all mounts and gateways.

An HTTP gateway snapshots one HTTP(S) origin without userinfo/path/query/fragment,
its normalized upstream base path, and the publication's public prefix:
`publicPrefix + suffix -> upstreamBasePath + suffix`, preserving escaped resource
paths and queries. Browser requests supply only relative public paths. The Go
standard-library client disables automatic redirects, automatic decompression
and environment proxies. Same-upstream redirects inside the upstream base map
back to the public prefix; HTTP(S) external redirects remain external. Unsafe
schemes, userinfo and same-upstream redirects outside the configured base fail.
Hop-by-hop, proxy, browser cookie and forwarding headers are stripped; the
upstream Host is fixed, Origin is synthesized for unsafe methods or when supplied,
and in-prefix Referer URLs map to the upstream origin/base (others are removed). Application Authorization, Range, If-Range, status, content type and
cache headers are retained. There is no body rewriting.

Each gateway owns an ephemeral cookie jar: upstream Set-Cookie updates the jar,
subsequent requests receive its cookies, and synthetic browser responses never
receive Set-Cookie. Cookie state is bounded to 128 records / 64 KiB per session.
Cookies and active sessions are never durable. JavaScript
access to server-created cookies is outside this contract. Gateways expire
unattached after one minute, have a five-minute tunnel idle timeout, and close
on disconnect, explicit close, owner shutdown or host shutdown. Active gateways
are limited to 8 per owner and 64 globally; publications/tickets are bounded.
The owner receives `http.gateway.closed {id}` for plugin-local reconciliation.

`browser.stream.ticket` issues a random, owner/stream-scoped, single-use ticket
valid for one minute. Currently its stream provider is the HTTP gateway; later
providers may reuse the same restriction. The common `api/v1/ws` endpoint
consumes it and **requires** the existing RunPilot encryption handshake,
regardless of normal payload-mode settings or query overrides. That connection
can attach/close only the granted stream and exchange its binary data; it cannot
invoke plugin RPC, subscribe to plugin events, obtain REST authority, or become
a normal authenticated application session. Even a normal plaintext application
connection cannot attach these gateway streams. There is no feature endpoint.

### HTTP tunnel version 1

Inside the existing RPS1 binary stream, frames are concatenated without regard
to WebSocket message boundaries. All integers are unsigned big endian:

```text
version:u8 (=1), type:u8, requestId:u32, payloadLength:u32, payload:bytes
```

Request IDs are nonzero, monotonically increasing per gateway. At most 16
exchanges run concurrently; the browser admits at most 64 bounded waiting requests. Payloads are at most 16 KiB. Types:

| Type | Direction | Payload |
| --- | --- | --- |
| 1 request start | browser → host | JSON `{method,path,headers}`; headers map to string arrays |
| 2 request body | browser → host | raw bytes |
| 3 request end | browser → host | empty |
| 4 request cancel | browser → host | empty; cancels upstream request |
| 5 response start | host → browser | JSON `{status,headers}` |
| 6 response body | host → browser | raw bytes |
| 7 response end | host → browser | empty |
| 8 response error | host → browser | safe UTF-8 message |
| 9 upload credit | host → browser | byte count:u32 |
| 10 download credit | browser → host | byte count:u32 |

GET, HEAD, POST, PUT, PATCH, DELETE and OPTIONS share the transport; CONNECT and
TRACE are rejected. Start/header metadata is at most 16 KiB, headers at most
64 names and 16 KiB combined, request path/query at most 8 KiB. Each exchange has
a maximum 64 KiB unconsumed byte window in each direction (1 MiB per direction
per gateway). Upload credit is returned only when the upstream pipe consumes
bytes. The host waits for download credit before reading upstream bodies;
ReadableStream consumption returns browser credit. Parser buffers, send queues,
physical WebSocket buffering and stalled writes are also bounded. Invalid frames
or excess credit close the gateway; late frames for completed IDs are ignored.
Version/type fields allow a later protocol version to extend this contract.

The worker constructs synthetic Responses without persistent application caches.
It negotiates gzip/deflate (identity for Range), carries compressed bytes through
the tunnel, and uses streaming DecompressionStream for browser rendering because
synthetic Responses do not perform network content decoding. Decoded responses
remove invalid compressed Content-Length/Content-Encoding metadata. The normal
UI and worker load the same worker-safe `RunPilotSecureWebSocket` implementation.
Normal UI bearer authentication is held in sessionStorage: the current tab
migrates and removes legacy localStorage tokens. Browser features must launch
with `noopener` so their tab does not inherit that credential or an opener.

Web Apps binds document navigations through a single-use, one-minute worker-local
handoff under `publicPrefix/__runpilot__/navigate/`. Its redirect binds the
reserved `resultingClientId` before returning to the clean application URL;
redirect chains retain that client ID. This also works in browsers that omit
the initiating client ID on navigation. An opaque, target-session-only lineage
handle in the tab's `window.name` lets the package bootstrap resume that same
worker-local session after a reload or document navigation. It grants no new
gateway, login or target access. No handle is written to localStorage or
sessionStorage; worker loss, host restart or session close fails closed.
Browsers without `Request.body` use their native upload Blob's readable stream;
upload chunks still obey the tunnel credit window. Response bodies are streamed
without buffering or rewriting.
