# RunPilot plugins

RunPilot's plugin manager is built in; first-party Remote provider runtimes are separate local processes. Remote targets, sessions, browser tickets, Guacamole/noVNC handling and the Remote UI remain core features. Xpra, RDP/guacd and VNC runtime behavior live behind the plugin boundary.

First-party source manifests are in `plugins/remote-{xpra,rdp,vnc}` and their executables are built from `cmd/runpilot-plugin-remote-*`. Releases install them next to the RunPilot executable under `plugins/`. Mutable enablement is stored in `runpilot.yaml`, not in these manifests.

```yaml
plugins:
  remote.xpra:
    enabled: true
```

An omitted override uses the manifest's `defaultEnabled`, preserving enabled Remote providers after upgrade. A plugin can be known but unavailable when it has no executable for the current platform; that is not a manifest error.

## Control protocol

The control plane is versioned independently (`protocolVersion: 1`) and is loopback-only. RunPilot gives each spawn a fresh 256-bit secret through its private process environment. Requests require that secret and expose only `info`, `health`, `capabilities`, shutdown, and the typed Remote operations needed for provider status and session lifecycle. Addresses and secrets are never returned by the public API or logged.

RDP and VNC plugins create one loopback transport bridge per authorized session; the core can dial only that endpoint. Xpra returns its private loopback HTTP endpoint. No browser input is accepted as an upstream address, so these bridges cannot act as general TCP proxies.

Plugin startup has a bounded readiness handshake. Crashes and failed readiness become a failed plugin state; stdout/stderr is retained in a bounded `plugin.log`. Disabling a Remote plugin is rejected while it owns an active session. RunPilot shutdown stops Remote sessions before plugin processes.
