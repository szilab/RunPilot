# RunPilot

RunPilot is a lightweight Windows host-management application written in Go. One native Windows service manages configured workloads and operations and exposes an embedded local web UI.

The current MVP focuses on process supervision, scheduling and backup jobs. The longer-term product direction is a coherent Windows management GUI for applications, scheduled operations, backup tools, storage access and, later, software installation.

The project takes the useful operating model of Perch — one service, one dashboard, many managed processes — but uses its own implementation and broader typed capability/integration model.

A central architectural rule is:

> **RunPilot manages tools and workloads; it does not become those tools.**

RunPilot should integrate specialist tools such as Robocopy, Restic or future package/runtime providers instead of reimplementing their core semantics.

## MVP capabilities

- Native Windows service (`RunPilot Process Manager`)
- Long-running process supervision
- `.exe`, `.bat`/`.cmd`, `.ps1` and `.py` launch support
- Autostart and `never` / `on-failure` / `always` restart policies
- Exponential restart backoff
- Interval, daily and cron scheduled jobs
- Manual job execution
- Typed backup jobs using Windows `robocopy.exe`
- Robocopy copy/update and mirror modes
- Captured stdout/stderr and execution history
- Embedded web GUI and REST API
- Overview dashboard with host CPU, memory, disk and workload health
- Token-authenticated API bound to `127.0.0.1` by default
- YAML configuration under `%ProgramData%\RunPilot`

## Product direction

RunPilot separates user-facing capabilities from external tool integrations.

Planned/possible capabilities include:

- **Applications** — lifecycle, status and logs for long-running workloads.
- **Jobs** — manually or automatically scheduled one-shot operations.
- **Backups** — GUI and scheduling around specialist backup tools.
- **Storage** — writable Local Filesystem browsing (a restricted folder or all drives accessible to the service identity), plus future versioned providers.
- **Software** — later GUI over external installation/package providers such as WinGet.
- **History and health** — shared execution history, logs and host/workload status.

An external integration may serve more than one capability. For example, a future Restic integration can provide both backup execution and storage browsing/version restore while sharing repository configuration, executable discovery and credentials.

RunPilot should not emulate unsupported backend features merely to make integrations look identical. If Robocopy does not provide versioned repository semantics, versioned backup should use a tool that natively provides them rather than adding a home-grown backup format to RunPilot.

Similarly, future Docker/Compose support should attach to an already functional external runtime and expose useful GUI lifecycle/status/log operations. Provisioning WSL, installing or operating Docker daemons, implementing container networking or recreating Docker orchestration are not currently part of the RunPilot product boundary.

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
prefix unchanged to RunPilot. The UI and API both use the configured prefix.

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

Processes are continuous workloads. Scheduled jobs are one-shot executions. Backups are a typed job subtype, so they use the same scheduler, history and manual-run machinery without becoming arbitrary shell-script templates.

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
  source: D:\Data
  destination: F:\Backup\Data
  mode: copy
  retries: 2
  retryWaitSeconds: 5
```

`mirror` mode maps to Robocopy `/MIR` and can delete destination-only files. The UI displays an explicit warning before saving it.

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
├── YAML config + JSONL run history + per-run logs
└── Embedded HTTP API + web UI
```

The first MVP deliberately keeps persistence simple. A later milestone can migrate history/configuration to embedded SQLite once the domain/API has stabilized.

## Near-term roadmap

1. Replace the initial `taskkill /T` process-tree termination with native Windows Job Objects.
2. Add graceful stop policies and configurable stop timeouts.
3. Add live log streaming (SSE/WebSocket), rotation and retention.
4. Add job cancellation and richer running-job state.
5. Refine the integration boundary and add additional typed backup tools, with Restic as the preferred direction for versioned/snapshot backup semantics.
6. Add Restic snapshot/version browsing, download and restore. Its read/version capabilities will intentionally differ from the writable Local Filesystem provider.
7. Add encrypted secret/environment/integration credential storage using Windows DPAPI or an equivalent protected mechanism.
8. Add a double-click/tray management shell for install/start/stop/open-UI actions.
9. Explore software installation/upgrade GUI through external providers such as WinGet.
10. Add signed Windows releases and GitHub Actions CI/release builds.
