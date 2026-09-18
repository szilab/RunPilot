# RunPilot architecture

## Product direction

RunPilot is a lightweight Windows/Linux host-management control plane with one native service per platform and an embedded web UI. Windows uses Windows Service Control Manager; Linux uses a systemd user service. Its purpose is to give users one coherent GUI for managing applications, scheduled operations, backup tools, storage access and software installation.

RunPilot should integrate existing specialist tools instead of reimplementing them. The guiding rule is:

> **RunPilot manages tools and workloads; it does not become those tools.**

Examples:

- RunPilot supervises native processes; it does not replace the Windows process model.
- RunPilot schedules and configures Robocopy, Restic or rdiff-backup; it does not implement its own backup format, deduplication engine or retention model.
- RunPilot may expose Docker/Compose lifecycle operations in the future; it should not become a WSL or Docker orchestrator.
- RunPilot exposes software installation through external providers; it should not become a package manager.

The browser is only a management client. Operations run with the permissions of the account hosting RunPilot; RunPilot does not grant additional privileges.

## Domain boundaries

The product separates capabilities from tool integrations.

### Core capabilities

Core RunPilot capabilities provide the consistent user experience:

1. **Tasks** — one view for continuous commands, scheduled commands and typed backup jobs.
2. **Storage** — runtime-discovered Local filesystem and Docker-volume locations.
3. **Software management** — GUI over external installation/package providers.
4. **Health** — cross-cutting execution history, logs and host/workload status.

These capabilities should share RunPilot infrastructure such as configuration, scheduling, execution history, authorization and the embedded GUI, but they should not collapse into one generic shell-command abstraction.

### Integrations

An integration encapsulates knowledge about an external tool or runtime. One integration may support multiple RunPilot capabilities.

For example, the Restic integration currently provides backup jobs and can later add its distinct Storage/browser operations without duplicating repository configuration:

```text
                    Restic integration
                     /            \
                    /              \
                   v                v
             Backup jobs       Storage browser
             restic backup     snapshots / ls /
                               dump / restore
```

Repository configuration, executable discovery, password-file references and command construction stay with the typed Restic backup integration. Storage browsing and restore are not implemented yet and must not be implied by a backup definition.

Tool-specific configuration should remain typed. Avoid a universal configuration model containing every feature offered by every possible backend. Capabilities unsupported by a selected tool should not be emulated inside RunPilot merely to make all integrations look identical.

## Runtime

```text
                         +----------------------------+
                         |        runpilot            |
                         | Windows SCM / systemd user|
                         +-------------+--------------+
                                       |
                   +-------------------+--------------------+
                   |                   |                    |
          +--------v--------+  +-------v-------+   +--------v-------+
          | Process Manager |  |   Scheduler   |   |   HTTP / GUI   |
          +--------+--------+  +-------+-------+   +----------------+
                   |                   |
                   |           +-------v--------+
                   |           | One-shot Runner|
                   |           +-------+--------+
                   |                   |
                   |          +--------+---------+
                   |          |                  |
              executables  command jobs     integrations
                                               |
                                           Robocopy
```

External integrations should normally invoke the authoritative external tool through a typed adapter. RunPilot may parse structured output and present a richer GUI, but the external tool remains responsible for the underlying operation.

## Persistence

Persistence is deliberately transparent:

- `runpilot.yaml` — configuration
- `history.jsonl` — completed run records
- `runs/<run-id>.log` — captured stdout/stderr

Default location: `%ProgramData%\RunPilot` on Windows and `$XDG_DATA_HOME/runpilot` or `~/.local/share/runpilot` on Linux. `RUNPILOT_DATA_DIR` takes precedence. Linux service installation uses the current user's systemd unit directory and normal user permissions; `sudo loginctl enable-linger <user>` enables boot-time operation without an interactive login.

SQLite is a sensible next persistence step when query requirements, retention and migration needs justify it. REST/domain interfaces should remain stable when that migration happens.

Integration credentials and secrets require stronger storage than ordinary YAML configuration. Secret storage should use a Windows-appropriate protected mechanism such as DPAPI when implemented.

## Scheduling

The internal scheduler supports:

- interval (`20 minutes`)
- daily (`03:00`)
- 5- or 6-field cron expressions
- optional IANA timezone

Cron syntax supports `*`, `*/N`, numeric values, lists and numeric ranges. Advanced Quartz/Vixie extensions such as `L`, `W`, `#` and named weekdays/months are intentionally out of scope.

Overlap defaults to `skip`, which is especially important for backup jobs.

## Backup capability

Backup jobs are orchestration around specialist backup tools. RunPilot owns configuration, scheduling, execution, logs and history; the selected backup tool owns the backup semantics and data format.

### Robocopy

The first adapter uses `robocopy.exe`, present in modern supported Windows versions. RunPilot builds arguments itself instead of storing an opaque shell command.

Current presets:

- `copy` -> `/E`
- `mirror` -> `/MIR`
- `/COPY:DAT`
- `/DCOPY:DAT`
- `/XJ`
- configurable `/R:n` and `/W:n`

Robocopy exit codes 0-7 are success/non-fatal states; 8+ is failure.

Robocopy should remain a copy/mirror integration. RunPilot must not build its own versioned-backup layer on top of Robocopy simply because Robocopy does not provide repository snapshots or version history.

### Restic and rdiff-backup

Restic provides repository snapshot backups, native `forget` retention (optionally pruning) and repository checks. rdiff-backup provides a directly browsable current destination mirror plus native historical increments, increment removal and verification. Their commands are emitted as sequential provider-owned execution plans, so a backup can be followed by retention and verification while remaining one RunPilot job run/history record.

Restic repository browsing, historical-version browsing, download and restore, and rdiff-backup restore/version UI are intentionally separate Storage/follow-up work. Backup destinations are not automatically exposed on the Storage page.

Each backup integration may expose a tool-specific editor and operations. A capability descriptor can be used for discovery and UI decisions, but it should not force unrelated tools into a lowest-common-denominator or artificial universal backup schema.

## Storage capability

Storage access is intentionally separate from backup execution.

A **Storage location** exposes a browsable data source to the RunPilot GUI. Locations are runtime capabilities, not user-managed configuration records. Initial locations include:

- **Local filesystem** — always available as `local`, browsing all roots accessible to the RunPilot service identity.
- **Docker volumes** — one `docker-volumes` virtual location; its root dynamically lists volume directories. Contents are accessed through Docker-mounted helper containers, and running volumes are read-only.
- **Restic repository** — browse snapshots, inspect historical file versions, download a selected version and restore through Restic.

Conceptually, providers may offer capabilities such as:

```text
Browse
Download
Versions
Restore
```

Not every provider must implement every capability.

The Local Filesystem provider supports practical scoped browser operations: upload, create, rename, move and delete. Other providers, especially Restic, expose only their native read/version/restore capabilities rather than artificial writable operations.

### Local filesystem security

The user-visible Local filesystem is host-scoped and enumerates available roots; OS permissions remain authoritative. The internal root-scoped Local adapter remains available to provider-backed locations such as Docker volumes, resolving symlinks and enforcing its root boundary.

### Version-oriented UX

For versioned providers such as Restic, the GUI should be able to present historical versions of a file without requiring the user to understand repository-internal snapshot identifiers. Snapshot browsing remains useful, but a file-oriented "previous versions" workflow is a first-class target.

Downloading a historical file should stream the provider/tool output where practical instead of first implementing a second restore/copy engine inside RunPilot. Restoring to the host filesystem should delegate restoration semantics to the provider tool.

## Application capability

A managed process is currently the primary application type. It is expected to stay alive and has restart policy, status and logs.

A process definition contains:

- command/script
- interpreter selection (`auto`, direct, PowerShell, CMD, Python)
- arguments
- working directory
- environment additions
- autostart
- restart policy
- exponential restart backoff

RunPilot currently terminates Windows process trees through `taskkill /T /F`. This is intentionally an implementation seam. The production supervisor should move to native Windows Job Objects so descendants are owned and terminated deterministically.

### External runtimes

Future application integrations may expose workloads managed by an external runtime, for example Docker containers or Compose projects. Such integrations should attach to an already functional runtime and provide useful GUI operations such as status, start/stop/restart and logs.

Unless explicitly approved as a separate feature, RunPilot should **not**:

- provision or configure WSL distributions,
- install or operate a Docker daemon,
- implement container networking or port forwarding,
- recreate Docker/Compose orchestration semantics,
- require Docker Desktop specifically.

Docker/WSL integration is therefore deferred until its minimal product boundary is agreed. The architecture should preserve an integration seam without speculatively building an orchestrator.

## Software management

Software Management is a RunPilot capability. Its first provider is a **RunPilot-owned isolated Scoop installation**, not a user's existing Scoop and not a machine PATH lookup. The configured provider has a stable ID, display name, typed Scoop settings and a configurable root. An empty root resolves from the active RunPilot data directory as `<data-dir>\software\scoop` (normally `%ProgramData%\RunPilot\software\scoop`).

RunPilot lazily bootstraps the managed Scoop runtime from Scoop's official installer after downloading it to a controlled provider directory. Bootstrap failure is isolated to Software Management; it does not prevent the service or other capabilities from starting. All Scoop commands invoke the exact managed `apps\scoop\current\bin\scoop.ps1` entrypoint and pass a child-process-only Scoop environment rooted at that directory. RunPilot never permanently changes PATH or global/user `SCOOP`, `SCOOP_GLOBAL`, or `SCOOP_CACHE` settings.

The initial policy uses Scoop's standard portable model and the normal main bucket. Scoop's managed Git prerequisite is automatically installed under the same managed root so provider commands never depend on Git from a user Scoop or host PATH. The UI exposes typed list/add/remove bucket operations rather than raw Scoop command execution; the required `main` bucket cannot be removed. Git and 7-Zip remain visible in package results but their individual install, upgrade and uninstall controls are disabled because they are Scoop-managed prerequisites. Global installs, the nonportable bucket, importing/reusing existing Scoop packages, and automatic Process creation are excluded. Changing the configured root selects another isolated instance; RunPilot does not migrate, copy, delete or modify the previous root. Package install remains separate from Process Management.

WinGet remains a possible future provider for conventional Windows software, behind the same Software capability boundary.

## Security

The service can execute configured programs with the permissions of its hosting account. Current defaults therefore:

- bind only to `127.0.0.1`
- generate a random API token at first start
- require Bearer authentication for every management API request
- store config with restricted file permissions where supported

Before remote management is added, introduce TLS/reverse-proxy guidance, explicit remote-bind opt-in and stronger auth/session handling.

Environment variables may contain credentials but are currently ordinary configuration values. They are not encrypted secret storage. Secrets and integration credentials should move to DPAPI-backed or equivalently protected storage before RunPilot exposes workflows that encourage credential persistence.

Storage/download endpoints require the same authentication boundary as other management APIs and must validate provider/root/path access server-side.

## GUI principles

The embedded GUI uses static HTML/CSS/JavaScript. There is no frontend runtime dependency and no separate web server. Assets compile into `runpilot.exe`.

The long-term goal is a coherent Windows/Linux host-management GUI rather than a collection of unrelated tool pages. Integrations may expose tool-specific options, but navigation, status, execution feedback, history and common interactions should remain consistent.

New GUI features should call backend/domain integrations and must not duplicate execution logic in browser JavaScript.

A future React/Vue/Svelte frontend can replace the static client while preserving the same REST/domain boundaries if frontend complexity eventually justifies it.

## Architectural guardrails

When adding a new capability or external tool:

1. Decide whether it is a **RunPilot capability** or an **external integration**.
2. Prefer delegating specialist behavior to the authoritative external tool.
3. Keep tool-specific configuration typed instead of growing a universal catch-all schema.
4. Allow one integration to serve multiple capabilities when that avoids duplicated configuration or execution logic.
5. Do not implement unsupported tool features inside RunPilot merely for feature parity between adapters.
6. Keep privileged filesystem, process and tool execution on the service side; the browser remains a management client.
7. Avoid turning storage browsing into a general file manager, backup support into a backup engine, software management into a package manager, or external-runtime support into an orchestrator without an explicit product decision.
