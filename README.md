# RunPilot

RunPilot is a lightweight cross-platform process manager, scheduler and host utility for Windows and Linux. One native daemon manages configured workloads and exposes an embedded local web UI.

The project takes the useful operating model of Perch — one service, one dashboard, many managed processes — but uses its own implementation and a broader typed job model.

## MVP capabilities

- Native daemon integration: Windows Service Control Manager and Linux systemd
- Long-running process supervision
- Direct executables, Python and PowerShell launch support; CMD on Windows and sh/bash on Linux
- Autostart and `never` / `on-failure` / `always` restart policies
- Exponential restart backoff
- Interval, daily and cron scheduled jobs
- Manual job execution
- Typed backup jobs: Windows Robocopy plus Restic and rdiff-backup where installed
- Captured stdout/stderr and execution history
- Embedded web GUI and REST API
- Overview dashboard with host CPU, memory, disk and workload health
- Token-authenticated API bound to `127.0.0.1` by default
- YAML configuration under `%ProgramData%\RunPilot` (Windows) or `/var/lib/runpilot` (Linux daemon)

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
native service command line. With the example above, publish the application
through a reverse proxy at `https://mydomain.com/runpilot/`, forwarding that
prefix unchanged to RunPilot. The UI and API both use the configured prefix.

## Build and install

```bash
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/runpilot.exe ./cmd/runpilot
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/runpilot-linux-amd64 ./cmd/runpilot
```

On Windows, from an elevated terminal:

```powershell
.\runpilot.exe service install
.\runpilot.exe service start
```

The default data directory is `%ProgramData%\RunPilot`.

On Linux, run service operations as root. `service install` writes and enables
`/etc/systemd/system/runpilot.service`, pointing at the current executable and
selected data directory. The default daemon data directory is `/var/lib/runpilot`.

```bash
sudo ./runpilot service install
sudo ./runpilot service start
sudo ./runpilot service stop
sudo ./runpilot service uninstall
```

`RUNPILOT_DATA_DIR` has priority over platform defaults; `--data-dir` overrides
the default for an individual foreground run or installed service.

## Configuration model

Processes are continuous workloads. Scheduled jobs are one-shot executions. Backups are a typed job subtype, so they use the same scheduler, history and manual-run machinery without becoming arbitrary shell-script templates.

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
runpilot
├── Windows SCM or Linux systemd host
├── Core controller
│   ├── Process manager
│   ├── Scheduler
│   ├── One-shot job runner
│   └── Backup engine adapters
├── YAML config + JSONL run history + per-run logs
└── Embedded HTTP API + web UI
```

The first MVP deliberately keeps persistence simple. A later milestone can migrate history/configuration to embedded SQLite once the domain/API has stabilized.

## Near-term roadmap

1. Replace the initial `taskkill /T` process-tree termination with native Windows Job Objects.
2. Add graceful stop policies and configurable stop timeouts.
3. Add live log streaming (SSE/WebSocket), rotation and retention.
4. Add job cancellation and richer running-job state.
5. Add more built-in backup engines (`wbadmin` where appropriate) and retention/verification policy.
6. Add encrypted secret/environment storage using Windows DPAPI.
7. Add a double-click/tray management shell for install/start/stop/open-UI actions.
8. Add signed Windows releases and GitHub Actions CI/release builds.
