# RunPilot plugins

RunPilot plugins are immutable `.rpplugin` ZIP archives installed below `<dataDir>/plugins/<id>/<version>`. Mutable plugin data belongs below `<dataDir>/plugin-data/<id>` and enablement is persisted in `runpilot.yaml`.

A package contains:

```text
plugin.yaml
backend/plugin.wasm   # optional
web/plugin.js         # optional
web/plugin.css        # optional
```

The manifest uses `apiVersion: runpilot.plugin/v1`, declares `requires.runpilotApi`, capabilities and permissions, and may include a backend, a frontend, or both. Backend modules execute in RunPilot through the pure-Go wazero runtime. The ABI is versioned independently from plugin versions and uses data-oriented JSON operations. Plugins do not receive filesystem handles, native Go pointers, child processes, loopback control sockets, or unrestricted host access.

Host operations are mediated by a narrow API. The manager checks declared permissions before privileged operations such as process, network, and plugin configuration access. Calls have bounded contexts; traps, invalid responses, initialization failures, and timeouts mark the plugin failed without preventing RunPilot startup.

## Lifecycle

The backend downloads the official first-party catalog from GitHub, verifies the package size and SHA-256, validates the manifest, rejects unsafe ZIP paths, and installs through a temporary directory followed by an atomic move. The browser never downloads GitHub assets directly. Installed, enabled, loaded, incompatible, failed, update-available, and restart-required are separate states.

Enabling or disabling a plugin only changes the desired state in `runpilot.yaml`; activation is restart-only. The Settings page can batch changes and reports `Restart required`. No hot loading or unloading is performed.

Enabled frontend modules are returned by `/api/v1/plugins/runtime` and loaded as ES modules from `/plugins/<id>/...`. A module exports `activate(runpilot)`. Plugin CSS must be scoped to its own root and may use RunPilot CSS tokens. The frontend runtime, not core Remote rendering, owns provider-specific UI registration.

## Development and release

First-party sources live in `plugins/remote-xpra`, `plugins/remote-rdp`, and `plugins/remote-vnc`. Build an archive with:

```text
go run ./cmd/plugin-build ./plugins/remote-xpra -out dist
```

The release workflow publishes RunPilot binaries, `.rpplugin` archives, and a catalog for `main-latest` or `develop-latest`. Development package loading is opt-in through an installed package directory; production never executes arbitrary source-tree files.

The current ABI wrapper is intentionally small. Remote provider-specific WASM modules still need to be supplied by the first-party plugin build before those providers can be loaded in production.
