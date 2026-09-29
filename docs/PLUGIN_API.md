# RunPilot plugin API v1

This document records the implemented Phase 1 contracts. It is deliberately
small: package installation, the WASM ABI, the generic protocol envelopes and
the capability bridge are stable enough to build against. Feature migration,
the browser WebSocket endpoint and frontend extension hosts are implemented; feature migrations remain separate work.

## Package manifest

`plugin.yaml` is decoded strictly. Its supported fields are `apiVersion`, `id`,
`name`, `version`, `description`, `requires`, `platforms`, `backend` and
`frontend`.

```yaml
apiVersion: runpilot.plugin/v1
id: example.hello
name: Hello
version: 1.0.0
requires:
  backend: ">=1.0.0 <2.0.0"
  frontend: ">=1.0.0 <2.0.0"
  runpilotApi: 1
platforms: [linux, windows] # omitted means platform-independent
backend:
  module: backend/plugin.wasm
frontend:
  module: web/plugin.js
  stylesheet: web/plugin.css # optional
```

`backend` and `frontend` are independently optional. Platforms may only be
`linux` and/or `windows`. A package installed on another platform is recorded
as `incompatible`, not `failed`, and is not activated. The old `permissions`
field is intentionally unsupported. All loaded plugins may use every host
capability that RunPilot publishes; the OS identity of RunPilot is the security
boundary.

Every declared module or stylesheet must exist in the archive. The builder and
installer reject packages that do not meet that requirement; installation also
retains ZIP traversal, checksum and atomic-install protections.

## WASM ABI

The manifest schema is `runpilot.plugin/v1`. Raw WASM ABI 1 and 2 are supported,
selected by `requires.runpilotApi`. Unsupported ABIs are incompatible and cannot
load; future catalog versions remain visible with an incompatibility reason.
The following allocation convention describes ABI 1; ABI 2 is documented below.

Each backend exports linear memory plus these functions:

```text
runpilot_alloc(size: i32) -> i32
runpilot_init(pointer: i32, length: i32) -> i64
runpilot_call(pointer: i32, length: i32) -> i64
runpilot_shutdown(pointer: i32, length: i32) -> i64
```

RunPilot serializes a JSON request, asks `runpilot_alloc` for space and writes
the bytes to module linear memory. Lifecycle functions return an `i64` with the
response pointer in the high 32 bits and response byte length in the low 32
bits. The plugin allocates that response with `runpilot_alloc`; RunPilot copies
it before the next call. Responses must be valid JSON. This convention works
with TinyGo, Rust and other WASM toolchains and does not expose native pointers
or Go objects.

`runpilot_init` and `runpilot_shutdown` receive `{"apiVersion":1}`.
`runpilot_call` receives:

```json
{"apiVersion":1,"operation":"status.get","request":{}}
```

Missing exports, wrong signatures, traps, malformed buffers/JSON, failed
initialization and timed-out calls become controlled plugin failures. Calls are
bounded to five seconds by default. A timeout terminates the affected WASM
execution rather than the RunPilot process.

First-party backends use TinyGo 0.38.0 with the `wasm-unknown` target. TinyGo
is a build-time dependency only: normal installations receive a prebuilt
`backend/plugin.wasm`. `go generate ./plugins/system/backend` builds the
source-based System reference and gives an actionable error when TinyGo is not
available. `internal/pluginapi` is an optional Go convenience layer over this
language-neutral ABI, not a second host interface.

Instances retain state for their loaded lifetime. RunPilot serializes all
entry into one instance, including the optional callback export:

```text
runpilot_event(pointer: i32, length: i32) -> i64
```

The event receives the same envelope as `runpilot_call`; `operation` is a
generic event name and `request` is event JSON. Delivery is bounded by the
normal call timeout. A trap fails that delivery without affecting other
plugins; shutdown stops new calls before closing the instance.

## ABI v2 host infrastructure

The runtime accepts `requires.runpilotApi: 2` and dispatches it to a separate
ABI v2 adapter. ABI v2 is active for the System reference plugin; ABI v1
remains supported for existing plugins and compatibility fixtures.
The v2 lifecycle exports are `runpilot_init(i32 handle)`,
`runpilot_call(i32 handle)`, `runpilot_event(i32 handle)` (optional), and
`runpilot_shutdown(i32 handle)`, each returning no value. A declared v2 module
must use these signatures; the runtime never falls back to ABI v1.

Each lifecycle call gets an opaque, nonzero invocation handle. The host owns
that invocation's input and output bytes and removes the invocation after the
call, including when the call fails or traps. Imports are exposed through the
`runpilot` module:

```text
input_len(handle: i32) -> i32
input_read(handle: i32, dstPtr: i32, dstLen: i32) -> i32
output_write(handle: i32, srcPtr: i32, srcLen: i32) -> i32
host_call(handle: i32, reqPtr: i32, reqLen: i32) -> i32
response_len(handle: i32, response: i32) -> i32
response_read(handle: i32, response: i32, dstPtr: i32, dstLen: i32) -> i32
response_drop(handle: i32, response: i32) -> i32
```

Length and handle-producing imports return zero on invalid input or transport
failure; `output_write` and `response_drop` return one on success and zero on
failure. Input/output and capability payloads are limited to 16 MiB. One
output may be written per invocation. Capability errors remain structured
JSON responses, separate from ABI transport failures. Response handles belong
to their invocation and are removed automatically when it ends; explicit
drop returns zero for an invalid or already-dropped handle.

> Any pointer supplied by WASM to an ABI-v2 host import is valid only for that
> synchronous import call. The host copies the referenced bytes before
> returning and never retains the pointer or a view backed by WASM linear
> memory.

ABI v2 host imports are not exposed through ABI-v1 modules, whose retained
allocator and packed-buffer behavior remains isolated in the v1 adapter.

First-party TinyGo code should use `internal/pluginapi` rather than raw imports
or linear-memory offsets. The v2 SDK provides `ReadInput(handle, &value)`,
`WriteOutput(handle, value)`, and `CallHost(method, params, &result)`. It copies
input locally, copies outputs and capability requests during the import, reads
responses into local slices, and drops each response handle before
`CallHost` returns. `pluginapi.Error` preserves the capability code, message,
and optional structured details; `pluginapi.ABIError` identifies transport
and protocol failures.

A minimal v2 lifecycle implementation looks like:

```go
package main

import "github.com/szilab/RunPilot/internal/pluginapi"

type failure struct {
    Error *pluginapi.Error `json:"error"`
}

//export runpilot_call
func runpilot_call(handle uint32) {
    var call struct {
        Operation string `json:"operation"`
    }
    if err := pluginapi.ReadInput(handle, &call); err != nil {
        _ = pluginapi.WriteOutput(handle, failure{Error: &pluginapi.Error{Code: "invalid_argument", Message: err.Error()}})
        return
    }
    if call.Operation == "status.get" {
        var status map[string]any
        if err := pluginapi.CallHost("system.status", map[string]any{}, &status); err != nil {
            if capabilityErr, ok := pluginapi.AsCapabilityError(err); ok {
                _ = pluginapi.WriteOutput(handle, failure{Error: capabilityErr})
            } else {
                _ = pluginapi.WriteOutput(handle, failure{Error: &pluginapi.Error{Code: "abi_error", Message: err.Error()}})
            }
            return
        }
        _ = pluginapi.WriteOutput(handle, status)
    }
}
```

The host serializes lifecycle entry into each module. The System backend is the
first production TinyGo plugin using these SDK calls. Its build uses TinyGo
0.38.0 with the conservative collector so temporary JSON buffers can be
reclaimed during repeated lifecycle calls.

## Capability bridge

Backends may import:

```text
runpilot.host_call(pointer: i32, length: i32) -> i64
```

It uses the same memory and packed-response convention. Its request envelope
is:

```json
{"apiVersion":1,"capability":"log.write","params":{"message":"started"}}
```

Responses are either `{"ok":true,"result":...}` or
`{"ok":false,"error":{"code":"...","message":"..."}}`. Phase 1
publishes `log.write`, `config.get`, `config.set`, `system.status`, and the
plugin-namespaced `storage.get` / `storage.set` JSON key-value operations; all other namespaces are
reserved for real future operations:

```text
host.system.* host.config.* host.log.* host.process.* host.fs.*
host.network.* host.scheduler.* host.storage.*
```

There is no generic execution, syscall or raw-host capability. Inputs are
decoded strictly and errors stay structured.

## Browser protocol

The common protocol envelope is versioned separately as browser protocol v1.
The future authenticated application WebSocket will route:

```json
{"id":"42","plugin":"system","method":"status.get","params":{}}
```

Successful responses are `{"id":"42","result":{}}`; errors are
`{"id":"42","error":{"code":"unknown_plugin","message":"..."}}`.
Events use `{"plugin":"system","event":"status.changed","data":{}}`.
The implemented parser rejects malformed/unknown envelope fields and the
dispatcher provides correlation, unknown-plugin errors, bounded calls and
plugin-failure isolation. The authenticated WebSocket transport, cancellation
and binary/stream frames are intentionally Phase 2 work; existing REST and
specialized transport endpoints remain until their migrations.

## Frontend and design system direction

Frontend modules export `activate(runpilot)`. The stable public namespace is
being shaped around `runpilot.ui`, `runpilot.navigation`, `runpilot.overview`,
`runpilot.settings` and `runpilot.ws`; registration hosts are not implemented
in Phase 1. Existing first-party Remote packages retain their temporary legacy
compatibility hook until Remote migration, and new plugins must not depend on
it or on private application globals.

Core owns the shell, navigation container, Settings/Overview hosts, shared UI
library and theme engine. Plugins will own feature pages, menu entries, cards,
settings and feature dialogs. Public custom CSS uses semantic tokens such as
`--rp-surface`, `--rp-surface-elevated`, `--rp-text`, `--rp-text-muted`,
`--rp-border`, `--rp-space-*` and `--rp-radius-*`; it must not assume a concrete
style or scheme. Style (typography, density, radii and components) is separate
from System/Light/Dark scheme. `.rptheme` packages remain a future,
non-executable metadata-and-CSS format.

## Implemented reference transport and system plugin

The authenticated common application WebSocket is `GET api/v1/ws`. A browser
first requests a short-lived, single-use ticket through authenticated `POST
api/v1/ws/ticket`; the connection then uses the existing optional encrypted
payload handshake. It routes the request/response/event envelopes above,
including malformed-message, unknown-plugin and plugin-failure errors.

The public browser surface now implements `runpilot.ws.call(plugin, method,
params)`, `runpilot.ws.on(plugin, event, listener)`, and small registration
hosts for navigation, Overview and Settings. It reconnects automatically and
does not expose the raw WebSocket to plugins.

## Asynchronous capabilities

`scheduler.register` accepts `{id,schedule,callback,data}` (the existing
interval, daily, and cron schedule forms); IDs are scoped to the calling
plugin. `scheduler.remove` and `scheduler.list` operate only in that scope.
Firing is delivered as `runpilot_event` operation `scheduler.fired` with the
registration ID, callback, and data. Registrations are runtime-only and a
plugin reconstructs them from its storage after initialization.

`process.start` accepts `command`, `args`, `workingDirectory`, and
`environment`, returning an opaque managed ID. `process.status` and
`process.terminate` are owner-scoped. Output and termination arrive through
`runpilot_event` as `process.stdout`, `process.stderr`, and `process.exit`.
Each output stream has an 8-chunk queue (8 KiB chunks); new chunks are dropped
when a plugin cannot consume them, rather than blocking the child or growing
memory. Completed status is retained briefly for status/event delivery.

`events.publish` accepts `{event,data}` and sends the normal `{plugin,event,data}`
envelope to all current shared-WebSocket clients. The host supplies the plugin
ID; there is no replay or offline backlog and slow client queues are dropped.
Calls and `runpilot_event` remain serialized per WASM instance; scheduler and
process work never run while WASM is entered.

The nonpublic `system` technical reference package uses the normal installer and runtime,
not a core bypass. Its real `backend/plugin.wasm` is reproducibly generated by
`go generate ./plugins/system/backend` using TinyGo 0.38.0. It
calls `host.system.status`, which publishes hostname, OS/architecture, CPU,
memory and filesystem metrics through the existing platform collectors.

## Independent compatibility contracts

Package versions are strict SemVer. `requires.backend` and `requires.frontend`
contain SemVer ranges for the declared components; current host contracts are
both `1.0.0`. `requires.runpilotApi` retains its historical name solely for raw
WASM ABI selection (1 or 2), independent of those contracts and of RunPilot's
application version. System is a nonpublic technical fixture, not a production
feature plugin. See [PLUGIN_REGISTRY.md](PLUGIN_REGISTRY.md).
