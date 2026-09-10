# RunPilot architecture

## Product boundary

RunPilot has one privileged native Windows service. The service owns process execution, scheduling, run history and the HTTP API. The browser is only a management client; it never launches workloads directly.

The initial product intentionally separates three concepts:

1. **Managed process** — expected to stay alive; restart policy applies.
2. **Command job** — one-shot command; can be run manually or by schedule.
3. **Backup job** — one-shot typed operation implemented by a backup-engine adapter; shares scheduling/history with command jobs but does not expose arbitrary backup command templates.

This prevents the process supervisor and scheduler from becoming one large shell-command abstraction.

## Runtime

```text
                         ┌────────────────────────────┐
                         │      runpilot.exe          │
                         │ Native Windows Service     │
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

Default location: `%ProgramData%\RunPilot`.

SQLite is a sensible next persistence step when query requirements, retention and migration needs justify it. The REST/domain interfaces should remain stable when that migration happens.

## Scheduling

The internal scheduler supports:

- interval (`20 minutes`)
- daily (`03:00`)
- 5- or 6-field cron expressions
- optional IANA timezone

Cron syntax in the MVP supports `*`, `*/N`, numeric values, lists and numeric ranges. Advanced Quartz/Vixie extensions such as `L`, `W`, `#` and named weekdays/months are intentionally out of scope.

Overlap defaults to `skip`, which is especially important for backup jobs.

## Backup adapter: Robocopy

The first adapter uses `robocopy.exe`, present in modern supported Windows versions. RunPilot builds arguments itself instead of storing an opaque shell command.

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
- interpreter selection (`auto`, direct, PowerShell, CMD, Python)
- arguments
- working directory
- environment additions
- autostart
- restart policy
- exponential restart backoff

The MVP terminates Windows process trees through `taskkill /T /F`. This is intentionally an implementation seam. The production supervisor should move to native Windows Job Objects so descendants are owned and terminated deterministically.

## Security

The service is privileged and can execute arbitrary configured programs. Current defaults therefore:

- bind only to `127.0.0.1`
- generate a random API token at first start
- require Bearer authentication for every management API request
- store config with restricted file permissions where supported

Before remote management is added, introduce TLS/reverse-proxy guidance, explicit remote-bind opt-in and stronger auth/session handling. Secrets in environment variables should move to Windows DPAPI-backed encrypted storage.

## GUI

The MVP uses embedded static HTML/CSS/JavaScript. There is no frontend runtime dependency and no separate web server. The assets compile into `runpilot.exe`.

A future React/Vue/Svelte frontend can replace the static client while preserving the same REST contract.
