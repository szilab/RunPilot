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

The GUI presents the first three execution concepts as **Tasks**: Continuous commands, Scheduled commands, and Backups. This is a presentation/domain boundary only; `processmgr` supervises persistent processes while `scheduler` triggers one-shot `jobs` executions.

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

## Remote Access

Remote Access is a capability with provider adapters. It owns persistent,
administrator-configured Remote targets and ephemeral runtime sessions; it is
not a generic command API and does not implement a display protocol. Targets
reuse `CommandSpec` for command arguments, working directory, and environment.
The current Xpra provider supports Linux application (`xpra start`) and desktop
(`xpra start-desktop`) sessions. A provider advertises availability and explicit
capabilities so a later VNC/noVNC, RDP/Guacamole, or Windows-oriented provider
does not require redesigning the Remote API or UI.

Each Xpra session gets a RunPilot runtime directory, an Xpra-selected display,
and a localhost-only HTTP/WebSocket listener. Xpra's supplied HTML5 client is
served through a RunPilot same-origin reverse proxy under the session resource;
its WebSocket upgrades remain on that proxy path. The public browser never sees
or chooses the internal endpoint. An authenticated API request creates the
embedded-client ticket, which becomes a bounded-lifetime, HttpOnly, path-scoped
cookie. This keeps the ordinary API bearer-token boundary and avoids exposing a
separate Xpra listener. Xpra itself is launched with mDNS disabled, HTML enabled,
new command execution disabled, and output bounded for diagnostics.

Remote sessions are owned by the current RunPilot process. On controlled
shutdown it requests the child process stop and falls back to termination after
a timeout; it does not reconcile or adopt unrelated/orphaned Xpra sessions on
restart. Linux Xpra support is optional: absence is a discoverable provider
state, while Windows returns a clear unsupported state. The implementation uses
Xpra's `--displayfd` for collision-free display allocation. Xpra does not report
an automatically selected WebSocket port to its parent, so RunPilot reserves a
localhost ephemeral port immediately before start; there remains the usual
small same-host bind race, contained to a private listener and reported as a
session-start failure if it occurs.

## Docker Compose boundary

`internal/dockercompose` is a Linux-only capability behind the existing platform capability model. It uses the Docker CLI with fixed argument arrays and an injectable runner, rather than the Docker SDK or a shell. Managed Compose projects persist as direct children of `<data-dir>/compose`; external Docker-discovered Compose projects are observable but read-only.

RunPilot manages Docker only through explicit Compose-project operations plus constrained actions for containers in managed projects. No generic Docker command execution API is exposed. The Docker authority is the RunPilot process identity, with no automatic sudo, socket-permission, or docker-group changes. Project deletion is directory-only and requires Docker to confirm that no project containers remain; lifecycle `down` never implicitly removes volumes or images. Container actions accept only a validated hexadecimal ID, re-inspect and verify the Compose label and managed-project directory, and use fixed `docker container start`, `stop`, `rm`, `logs --tail`, or PTY-backed `exec -i -t <id> /bin/sh` arguments. Deletion is rejected while the container runs.

Docker volumes are a sibling Docker domain, not a special Backup-engine setting. Storage is a runtime registry, not persisted configuration: `local` is always available and `docker-volumes` is one Docker-backed virtual location whose root lists discovered volume names. A short-lived, unprivileged helper container mounts only the requested volume at a fixed container path for browsing and download; RunPilot never accesses Docker host mountpoints. Dynamic running-container usage makes a volume read-only and is rechecked by backend mutation calls. Docker-volume backup sources intentionally report unsupported until Docker-mediated export/staging exists. A volume is removable when Docker confirms no container references it; Storage visibility does not block deletion.

Networks follow the same narrow Docker boundary: discovery, Compose-label association, named bridge-network creation, and deletion only when no container references the network. RunPilot does not expose arbitrary network drivers/options or attach, detach, firewall, and routing controls.
