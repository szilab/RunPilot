# RunPilot

RunPilot is a lightweight Windows host-management application written in Go. One native Windows service manages configured workloads and operations and exposes an embedded local web UI.

RunPilot provides process supervision, scheduling, backup jobs, storage access and isolated software management through a coherent Windows management GUI.

The project takes the useful operating model of Perch — one service, one dashboard, many managed processes — but uses its own implementation and broader typed capability/integration model.

A central architectural rule is:

> **RunPilot manages tools and workloads; it does not become those tools.**

RunPilot should integrate specialist tools such as Robocopy, Restic or future package/runtime providers instead of reimplementing their core semantics.

## Capabilities

- Native Windows service (`RunPilot Process Manager`)
- Long-running process supervision
- `.exe`, `.bat`/`.cmd`, `.ps1` and `.py` launch support
- Autostart and `never` / `on-failure` / `always` restart policies
- Exponential restart backoff
- Interval, daily and cron scheduled jobs
- Manual job execution
- Typed backup jobs using Robocopy, Restic and rdiff-backup
- Robocopy copy/update and mirror modes
- Captured stdout/stderr and execution history
- Embedded web GUI and REST API
- Overview dashboard with host CPU, memory, disk and workload health
- Token-authenticated API bound to `127.0.0.1` by default
- YAML configuration under `%ProgramData%\RunPilot`
- Software Management through a RunPilot-owned isolated Scoop provider
- Interactive Terminal tabs backed by a Linux PTY or Windows ConPTY
- Remote Access targets and sessions through the Linux Xpra provider

## Product direction

RunPilot separates user-facing capabilities from external tool integrations.

Planned/possible capabilities include:

- **Tasks** — one user-facing view for continuous commands, scheduled commands and typed scheduled backups.
- **Storage** — the always-available Local filesystem plus Docker volumes discovered at runtime.
- **Software** — install, upgrade and remove portable applications through the RunPilot-owned Scoop provider; WinGet may be a future provider for conventional Windows software.
- **Terminal** — short-lived interactive local shells in the web UI, using native PTY/ConPTY support rather than command execution pipes.
- **Health** — shared execution history, logs and host/workload status; logs remain available from their task.

An external integration may serve more than one capability. Restic currently provides backup execution, native retention and repository checking; repository browsing, historical versions and restore remain separate follow-up work.

RunPilot should not emulate unsupported backend features merely to make integrations look identical. If Robocopy does not provide versioned repository semantics, versioned backup should use a tool that natively provides them rather than adding a home-grown backup format to RunPilot.

Docker support attaches to an already functional external runtime and is limited to explicit Compose projects, Compose-labelled containers, named volumes, and named networks. Provisioning WSL, installing or operating Docker daemons, generic Docker execution, or recreating Docker orchestration are not part of the RunPilot product boundary.

See `ARCHITECTURE.md` for the detailed capability/integration model and guardrails.

## Development

```bash
go test ./...
go vet ./...
go run ./cmd/runpilot run --data-dir ./runpilot-data
```

Open `http://127.0.0.1:9070`. On first start, `run` prints a newly generated API
token once. Save it and use it on the web UI login screen. The token is stored in
`runpilot.yaml`.

## Web server and reverse proxy

The YAML `server` section controls the listener and UI URL prefix:

```yaml
server:
  bind: 127.0.0.1:9070 # Host part is used with port; full legacy address also works.
  port: 9070
  basePath: /runpilot
```

`--port` and `--base-path` override these values for one foreground run:

```powershell
.\runpilot.exe run --port 9080 --base-path /runpilot
```

Use the same flags with `service install` to persist them in the installed
Windows service command line. With the example above, publish the application
through a reverse proxy at `https://mydomain.com/runpilot/`, forwarding that
prefix unchanged to RunPilot. The UI, REST API, and Terminal WebSocket all use
this one RunPilot listener and configured prefix. No terminal-specific port or
proxy target is required.

The Terminal WebSocket endpoint is `api/v1/terminal/connect`. It is therefore
`/api/v1/terminal/connect` with the root base path and
`/runpilot/api/v1/terminal/connect` when `basePath: /runpilot` is configured.
The browser builds its `ws:`/`wss:` URL from the GUI page's base URI, so the
public host, port, TLS scheme, and prefix are preserved when TLS terminates at
a reverse proxy.

Configure a WebSocket-aware proxy to forward `/runpilot/*` (including Upgrade
requests) to the same RunPilot upstream port, preserving both the `/runpilot`
prefix and original `Host` header. Do not strip that prefix in the proxy when
RunPilot is configured with `basePath: /runpilot`.

## Interactive Terminal

The Terminal page starts an interactive shell as the RunPilot service identity.
It uses a real Linux PTY or Windows ConPTY, with embedded xterm.js assets; no
Node.js runtime or public CDN is required after build. Each browser tab has one
short-lived session: closing the tab, browser connection, or RunPilot shuts
down its shell and process tree. Terminal bytes and commands are never saved by
RunPilot.

Terminal access is equivalent to arbitrary command execution as the account
running RunPilot (for example `root`, `SYSTEM`, Administrator, or a dedicated
service account). It is protected by the same API token boundary as process and
job management. The browser first obtains a short-lived, single-use connection
ticket through the authenticated REST API; the permanent token is never placed
in the WebSocket URL.

## Remote Access (Xpra)

Remote Access is a provider-based capability, not a remote-display protocol.
The first provider is Xpra on Linux hosts. Add an enabled application target
(for example `firefox`) or desktop target (for example `startxfce4`) from the
Remote page; its command, arguments, working directory and environment are
stored in `runpilot.yaml` like other configured workloads. Each Open action
creates a new, ephemeral isolated session.

RunPilot discovers `xpra` on `PATH` and reports the installed version, missing
binary, or unsupported platform without preventing the daemon from starting.
Install Xpra using your distribution's package source (for example, `apt install
xpra` on Debian/Ubuntu or `dnf install xpra` on Fedora where those packages are
available). The selected application and any desktop environment/window manager
must already be installed; RunPilot does not install or configure them.

Each target may opt in to **Forward RunPilot's D-Bus session**. This passes the
RunPilot process's `DBUS_SESSION_BUS_ADDRESS` to that target only, allowing it
to interact with desktop-session services. It is disabled by default because it
reduces target isolation; if the daemon has no D-Bus session address, RunPilot
reports a clear launch error and the address may instead be configured in the
target's Environment fields.

Xpra listens only on a per-session `127.0.0.1` WebSocket/HTTP port. The browser
loads Xpra's upstream HTML5 client through a same-origin, ticketed RunPilot
reverse proxy, so no Xpra port is exposed publicly. The ticket is exchanged for
an HttpOnly, path-scoped, bounded-lifetime cookie used only for the embedded client's assets and
WebSocket upgrade. RunPilot never accepts a browser-supplied upstream host or
port. Clipboard, dynamic resize, and fullscreen are supplied by the upstream
Xpra HTML5 client when the installed Xpra build supports them.

Sessions are intentionally ephemeral: RunPilot stops the Xpra processes it owns
during shutdown and does not adopt orphaned sessions after a restart. The first
supported deployment is RunPilot directly on a Linux host. Xpra server support
is reported as unsupported on Windows; future VNC/noVNC, RDP/Guacamole, or
other providers can implement the same Remote provider boundary.

## Windows build

```bash
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/runpilot.exe ./cmd/runpilot
```

On Windows, from an elevated terminal:

```powershell
.\runpilot.exe service install
.\runpilot.exe service start
```

The default data directory is `%ProgramData%\RunPilot`.

## Configuration model

Tasks are presented as one user-facing capability while continuous process supervision and scheduled execution retain separate internal lifecycle engines. Processes are continuous workloads; scheduled jobs are one-shot executions; backups are a typed job subtype sharing scheduler, history and manual-run machinery without becoming arbitrary shell-script templates.

Storage locations are runtime capabilities, not user-managed configuration records. `local` always exposes filesystem roots available to the RunPilot service identity. `docker-volumes` is one Docker-backed location whose root lists current volumes as directories. Volume contents are accessed through short-lived Docker helper containers, never through a Docker host mountpoint; running volumes are read-only.

Processes and command jobs can set per-command environment variables. These values
are stored in the normal RunPilot YAML configuration and are not encrypted secret
storage.

Example backup definition:

```yaml
id: job-example
name: Nightly data backup
enabled: true
type: backup
schedule:
  type: daily
  timeOfDay: "03:00"
overlapPolicy: skip
backup:
  engine: robocopy
  robocopy:
    source: D:\Data
    destination: F:\Backup\Data
    mode: copy
    retries: 2
    retryWaitSeconds: 5
```

`mirror` mode maps to Robocopy `/MIR` and can delete destination-only files. The UI displays an explicit warning before saving it.

Restic uses a repository and native snapshots; it can run `forget` retention (optionally with `--prune`) and a post-backup repository check. Passwords are not stored in configuration: use an optional `passwordFile` path or Restic's normal externally supplied environment.

```yaml
backup:
  engine: restic
  restic:
    repository: F:\Restic
    sources: [D:\Documents, D:\Photos]
    excludes: ["*.tmp"]
    tags: [home]
    useVss: true
    retention: { keepDaily: 7, keepMonthly: 12, prune: true }
    checkAfterBackup: true
```

rdiff-backup keeps its own directly browsable current mirror and historical increments at the destination. RunPilot can ask it to remove older increments and verify a completed backup.

```yaml
backup:
  engine: rdiff-backup
  rdiffBackup:
    source: D:\Photos
    destination: F:\Backup\Photos
    retention: { olderThan: 3M }
    verifyAfterBackup: true
```

Repository/version browsing and restore workflows are intentionally not part of the Backup capability yet.

Software Management is configured with a typed, RunPilot-owned Scoop provider:

```yaml
software:
  providers:
    - id: scoop
      name: RunPilot Scoop
      type: scoop
      scoop:
        root: D:\RunPilotApps # optional; empty uses <data-dir>\software\scoop
```

RunPilot downloads and bootstraps this isolated Scoop instance on first use, including Scoop's managed portable Git prerequisite under the same root. It never uses, changes, or imports an existing user Scoop installation; it does not permanently add Scoop shims to PATH or set global Scoop environment variables. Changing `root` selects a new isolated installation and leaves the old root untouched. Packages are standard portable Scoop packages and remain separate from RunPilot Process definitions. The Software page can list, add and remove Scoop buckets through typed controls; the required `main` bucket, Git and 7-Zip remain visible but their individual package controls are disabled because Scoop manages them.

## Architecture

```text
runpilot.exe
├── Windows Service host
├── Core controller
│   ├── Application/process supervision
│   ├── Scheduler
│   ├── One-shot job runner
│   └── Typed external integrations
├── Future storage providers / software providers
├── Runtime PTY/ConPTY terminal sessions (not persisted)
├── YAML config + JSONL run history + per-run logs
└── Embedded HTTP API + web UI
```

The first implementation deliberately keeps persistence simple. A later milestone can migrate history/configuration to embedded SQLite once the domain/API has stabilized.

## Docker Compose (Linux)

RunPilot has a deliberately narrow, Linux-only Docker Compose v2 feature. Managed projects are directories below `<data-dir>/compose/<project-name>/`; each can contain `compose.yaml` and a secret-bearing `.env` file. Project directories are the registry, so a managed project appears before its first `up`.

The Docker page has projects, volumes, and networks. It discovers other Compose projects through Docker too, but leaves them read-only. RunPilot never adopts or edits them. Managed projects expose only `up -d`, `start`, `stop`, and `down`; commands always include the project name, project directory, and Compose file. `down` does not remove volumes or images. A managed directory can be deleted only after Docker confirms there are no containers with its Compose project label. Containers in managed projects can be started, stopped, and deleted only when stopped; their logs can be viewed and a running container can open a PTY-backed `docker exec -it <id> /bin/sh` terminal. These actions validate the container ID, Compose project label, and managed-project directory before using fixed Docker arguments; they are not a general container-control or command API.

Volumes are discovered with Docker metadata and show Compose ownership only when Compose labels exist. Every discoverable volume is automatically a Storage location. RunPilot can create named local-driver volumes and may delete an unreferenced volume; deletion never uses force. Non-local drivers remain visible but unavailable for browsing. Running-volume Storage is read-only.

Docker volume mountpoints are resolved only through `docker volume inspect` internally and are never sent through the Storage API. This prepares a future provider-neutral Backup source flow; no Docker volume backup job exists yet. A filesystem backup of a volume used by a running application, especially a database, is not automatically application-consistent. Future backup support must explicitly require downtime or provide a separate quiescing strategy.

Docker networks are also visible on the Docker page. RunPilot can create named bridge networks and delete an unused network only after Docker confirms that no running or stopped container references it. Compose ownership is shown only from Compose labels. It does not expose network driver options, attach/detach controls, firewall configuration, or generic Docker networking commands.

Docker authority is exactly the authority of the process running RunPilot. The page tests the Docker CLI, Compose v2 plugin, and daemon access under that identity, distinguishing missing CLI/plugin, unavailable daemon, and permission denial. It never invokes sudo, changes Docker socket permissions, or modifies group membership.

## Near-term roadmap

1. Replace the initial `taskkill /T` process-tree termination with native Windows Job Objects.
2. Add graceful stop policies and configurable stop timeouts.
3. Add live log streaming (SSE/WebSocket), rotation and retention.
4. Add job cancellation and richer running-job state.
5. Add Restic snapshot/version browsing, download and restore. Its read/version capabilities will intentionally differ from the writable Local Filesystem provider.
7. Add encrypted secret/environment/integration credential storage using Windows DPAPI or an equivalent protected mechanism.
8. Add a double-click/tray management shell for install/start/stop/open-UI actions.
9. Add further typed Software providers such as WinGet for conventional Windows software where appropriate.
10. Add signed Windows releases and GitHub Actions CI/release builds.
