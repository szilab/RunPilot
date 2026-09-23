# RunPilot plugins

RunPilot plugins are external executables managed by the RunPilot process. The core discovers them from `<dataDir>/plugins/<plugin>/plugin.yaml`, starts enabled plugins, reports runtime status, and stops managed plugin processes during shutdown.

The first plugin protocol version intentionally covers discovery and lifecycle only. Capability-specific RPC is added separately so the process boundary and manifest format can stabilize before Software, Backup, Files, and Remote providers are migrated.

## Manifest

Each plugin directory contains a `plugin.yaml` file and one or more platform executables.

```yaml
apiVersion: runpilot/v1
id: files.sftpgo
name: SFTPGo
version: 1.0.0
protocolVersion: 1
enabled: true

capabilities:
  - managed-service
  - file-browser
  - storage-source-consumer

executables:
  windows-amd64: runpilot-sftpgo-plugin.exe
  linux-amd64: runpilot-sftpgo-plugin

args: []
```

Executable paths must be relative to the plugin directory. Absolute paths and paths that escape the plugin directory are rejected.

Supported executable selectors are checked in this order:

1. `<goos>-<goarch>`
2. `<goos>`
3. `default`

## Runtime environment

RunPilot starts the plugin with its plugin directory as the working directory and adds:

- `RUNPILOT_PLUGIN=1`
- `RUNPILOT_PLUGIN_PROTOCOL=1`
- `RUNPILOT_PLUGIN_ID=<manifest id>`

Plugin stdout and stderr are appended to `plugin.log` inside the plugin directory.

## API

The authenticated RunPilot API exposes:

- `GET /api/v1/plugins`
- `POST /api/v1/plugins/rescan`
- `POST /api/v1/plugins/{id}/start`
- `POST /api/v1/plugins/{id}/stop`

## Planned capability model

The plugin boundary is intended for technology-specific integrations while core RunPilot functions remain built in.

Built in:

- process supervision
- tasks and scheduling
- Docker
- terminal
- plugin runtime

Initial plugin candidates:

- Software management
- Backup providers
- Files providers such as SFTPGo and FileBrowser Quantum
- Remote provider implementations where the provider-specific boundary proves useful

The core must not depend on a specific plugin implementation. Capability-specific contracts will be versioned independently of individual plugin versions.
