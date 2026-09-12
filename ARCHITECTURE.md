# RunPilot architecture

## Product boundary

RunPilot has one privileged native daemon: Windows Service Control Manager on Windows and systemd on Linux. The daemon owns process execution, scheduling, run history and the HTTP API. The browser is only a management client; it never launches workloads directly.

Core RunPilot functionality must remain platform-neutral. OS-specific functionality belongs behind small platform, service, or provider adapters selected by Go build constraints or runtime capability detection. The capability API tells clients which native service manager, interpreters, and Windows-only integrations are available.

The initial product intentionally separates four concepts:

1. **Managed process** — expected to stay alive; restart policy applies.
2. **Command job** — one-shot command; can be run manually or by schedule.
3. **Backup job** — one-shot typed operation implemented by a backup-engine adapter; shares scheduling/history with command jobs but does not expose arbitrary backup command templates.
4. **Interactive terminal session** — a short-lived, PTY-backed runtime shell attached to one browser WebSocket; it is not configuration, a scheduled job, or a captured run.

This prevents the process supervisor and scheduler from becoming one large shell-command abstraction. Interactive Terminal sessions are PTY-backed runtime sessions and are separate from non-interactive Process and Scheduler command execution.

## Runtime

```text
                         ┌────────────────────────────┐
                         │       runpilot             │
                         │ Windows SCM / Linux systemd│
                         └─────────────┬──────────────┘
                                       │
                   ┌───────────────────┼────────────────────┐
                   │                   │                    │
          ┌────────▼────────┐  ┌───────▼───────┐   ┌──────▼──────┐
          │ Process Manager │  │   Scheduler   │   │ HTTP / GUI  │
          └────────┬────────┘  └───────┬───────┘   └─────────────┘
                   │                   │
                   │           ┌───────▼────────┐
                   │           │ One-shot Runner│
                   │           └───────┬────────┘
                   │                   │
                   │          ┌────────┴─────────┐
                   │          │                  │
              executables  command jobs     backup adapters
                                               │
                                           Robocopy
```

## Persistence

MVP persistence is deliberately transparent:

- `runpilot.yaml` — configuration
- `history.jsonl` — completed run records
- `runs/<run-id>.log` — captured stdout/stderr

Default location: `%ProgramData%\RunPilot` on Windows and `/var/lib/runpilot` for Linux daemon use. `RUNPILOT_DATA_DIR` is the highest-priority default override, while an explicit `--data-dir` remains available for foreground and service installation.

SQLite is a sensible next persistence step when query requirements, retention and migration needs justify it. The REST/domain interfaces should remain stable when that migration happens.

## Scheduling

The internal scheduler supports:

- interval (`20 minutes`)
- daily (`03:00`)
- 5- or 6-field cron expressions
- optional IANA timezone

Cron syntax in the MVP supports `*`, `*/N`, numeric values, lists and numeric ranges. Advanced Quartz/Vixie extensions such as `L`, `W`, `#` and named weekdays/months are intentionally out of scope.

Overlap defaults to `skip`, which is especially important for backup jobs.

## Backup adapters

Robocopy uses `robocopy.exe`, present in modern supported Windows versions. It is explicitly unavailable on Linux. Restic and rdiff-backup are portable typed adapters when their binaries are installed. RunPilot builds arguments itself instead of storing an opaque shell command.

Presets:

- `copy` → `/E`
- `mirror` → `/MIR`
- `/COPY:DAT`
- `/DCOPY:DAT`
- `/XJ`
- configurable `/R:n` and `/W:n`

Robocopy exit codes 0–7 are success/non-fatal states; 8+ is failure.

The next adapters can implement the same typed interface (for example `wbadmin`) without affecting scheduler or history code.

## Process lifecycle

A process definition contains:

- command/script
- interpreter selection (`auto`, direct, PowerShell, Python; CMD on Windows; sh/bash on Linux)
- arguments
- working directory
- environment additions
- autostart
- restart policy
- exponential restart backoff

Windows terminates process trees through `taskkill /T /F`. Linux starts each managed command in its own process group, sends SIGTERM to that group, then SIGKILL if descendants remain. This keeps stop/restart scoped to the managed workload rather than leaving child processes behind.

## Security

The service is privileged and can execute arbitrary configured programs. Current defaults therefore:

- bind only to `127.0.0.1`
- generate a random API token at first start
- require Bearer authentication for every management API request
- store config with restricted file permissions where supported

Terminal access is as privileged as arbitrary process execution by the service
identity. The REST API issues a cryptographically random, short-lived,
single-use ticket; only that ticket is present in the WebSocket URL. The
WebSocket retains the default same-origin validation and terminal contents are
neither logged nor persisted. A session ends with its browser WebSocket, shell
exit, or RunPilot shutdown; no detached or restored sessions exist.

Before remote management is added, introduce TLS/reverse-proxy guidance, explicit remote-bind opt-in and stronger auth/session handling. Secrets in environment variables should move to Windows DPAPI-backed encrypted storage.

## GUI

The MVP uses embedded static HTML/CSS/JavaScript. There is no frontend runtime dependency and no separate web server. The assets compile into `runpilot.exe`. The Terminal UI vendors xterm.js core, fit addon, CSS, and license notices as embedded production assets; it has no Node.js or CDN runtime dependency.

A future React/Vue/Svelte frontend can replace the static client while preserving the same REST contract.
