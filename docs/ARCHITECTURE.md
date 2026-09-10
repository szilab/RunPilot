# RunPilot architecture

## Product direction

RunPilot is a lightweight Windows host-management control plane with a single native Windows service and an embedded web UI. Its purpose is to give Windows users one coherent GUI for managing applications, scheduled operations, backup tools, storage access and, later, software installation.

RunPilot should integrate existing specialist tools instead of reimplementing them. The guiding rule is:

> **RunPilot manages tools and workloads; it does not become those tools.**

Examples:

- RunPilot supervises native processes; it does not replace the Windows process model.
- RunPilot schedules and configures Robocopy or Restic; it does not implement its own backup format or deduplication engine.
- RunPilot may expose Docker/Compose lifecycle operations in the future; it should not become a WSL or Docker orchestrator.
- RunPilot may expose software installation through tools such as WinGet; it should not become a package manager.

The browser is only a management client. Privileged operations remain owned by the RunPilot service and its backend integrations.

## Domain boundaries

The product separates capabilities from tool integrations.

### Core capabilities

Core RunPilot capabilities provide the consistent user experience:

1. **Applications** — long-running workloads with lifecycle, status and logs.
2. **Jobs** — one-shot operations that can run manually or on a schedule.
3. **Backups** — typed backup jobs delegated to external backup tools.
4. **Storage** — browsable storage sources with provider-specific capabilities such as download, versions or restore.
5. **Software management** — future GUI over external installation/package providers.
6. **History and health** — cross-cutting execution history, logs and host/workload status.

These capabilities should share RunPilot infrastructure such as configuration, scheduling, execution history, authorization and the embedded GUI, but they should not collapse into one generic shell-command abstraction.

### Integrations

An integration encapsulates knowledge about an external tool or runtime. One integration may support multiple RunPilot capabilities.

For example, a future Restic integration can provide both:

```text
                    Restic integration
                     /            \
                    /              \
                   v                v
             Backup jobs       Storage browser
             restic backup     snapshots / ls /
                               dump / restore
```

Repository configuration, executable discovery, credentials and command construction should be shared by that integration rather than duplicated independently by backup and storage code.

Tool-specific configuration should remain typed. Avoid a universal configuration model containing every feature offered by every possible backend. Capabilities unsupported by a selected tool should not be emulated inside RunPilot merely to make all integrations look identical.

## Runtime

```text
                         +----------------------------+
                         |      runpilot.exe          |
                         | Native Windows Service     |
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

MVP persistence is deliberately transparent:

- `runpilot.yaml` — configuration
- `history.jsonl` — completed run records
- `runs/<run-id>.log` — captured stdout/stderr

Default location: `%ProgramData%\RunPilot`.

SQLite is a sensible next persistence step when query requirements, retention and migration needs justify it. REST/domain interfaces should remain stable when that migration happens.

Integration credentials and secrets require stronger storage than ordinary YAML configuration. Secret storage should use a Windows-appropriate protected mechanism such as DPAPI when implemented.

## Scheduling

The internal scheduler supports:

- interval (`20 minutes`)
- daily (`03:00`)
- 5- or 6-field cron expressions
- optional IANA timezone

Cron syntax in the MVP supports `*`, `*/N`, numeric values, lists and numeric ranges. Advanced Quartz/Vixie extensions such as `L`, `W`, `#` and named weekdays/months are intentionally out of scope.

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

### Additional backup tools

When a feature requires semantics not offered by the active tool, use an integration for a tool that natively provides those semantics. A future Restic integration is the preferred direction for snapshot/versioned backup, retention, repository verification and restore workflows.

Each backup integration may expose a tool-specific editor and operations. A capability descriptor can be used for discovery and UI decisions, but it should not force unrelated tools into a lowest-common-denominator or artificial universal backup schema.

## Storage capability

Storage access is intentionally separate from backup execution.

A **Storage Provider** exposes a browsable data source to the RunPilot GUI. Initial/future providers include:

- **Local filesystem** — browse, upload, download and manage either an explicitly configured Windows root or all drives accessible to the RunPilot service identity.
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

Local filesystem providers explicitly choose either a configured hard root boundary or host-filesystem mode. Root mode resolves symlinks and verifies effective paths remain beneath the configured root. Host mode enumerates available drives and accepts only controlled provider-relative paths; Windows permissions remain authoritative.

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

The MVP terminates Windows process trees through `taskkill /T /F`. This is intentionally an implementation seam. The production supervisor should move to native Windows Job Objects so descendants are owned and terminated deterministically.

### External runtimes

Future application integrations may expose workloads managed by an external runtime, for example Docker containers or Compose projects. Such integrations should attach to an already functional runtime and provide useful GUI operations such as status, start/stop/restart and logs.

Unless explicitly approved as a separate feature, RunPilot should **not**:

- provision or configure WSL distributions,
- install or operate a Docker daemon,
- implement container networking or port forwarding,
- recreate Docker/Compose orchestration semantics,
- require Docker Desktop specifically.

Docker/WSL integration is therefore deferred until its minimal product boundary is agreed. The architecture should preserve an integration seam without speculatively building an orchestrator.

## Software management direction

A later Software area can provide a Windows-oriented GUI for installing, upgrading and removing applications through external providers such as WinGet.

RunPilot should own discovery, presentation, user intent, status and history. The installation provider remains responsible for package resolution and installation semantics.

A curated RunPilot application catalog may later combine installation with optional RunPilot application configuration, but it should build on provider integrations rather than introduce a new package-management implementation.

## Security

The service is privileged and can execute arbitrary configured programs. Current defaults therefore:

- bind only to `127.0.0.1`
- generate a random API token at first start
- require Bearer authentication for every management API request
- store config with restricted file permissions where supported

Before remote management is added, introduce TLS/reverse-proxy guidance, explicit remote-bind opt-in and stronger auth/session handling.

Environment variables may contain credentials but are currently ordinary configuration values. They are not encrypted secret storage. Secrets and integration credentials should move to DPAPI-backed or equivalently protected storage before RunPilot exposes workflows that encourage credential persistence.

Storage/download endpoints require the same authentication boundary as other management APIs and must validate provider/root/path access server-side.

## GUI principles

The MVP uses embedded static HTML/CSS/JavaScript. There is no frontend runtime dependency and no separate web server. Assets compile into `runpilot.exe`.

The long-term goal is a coherent Windows host-management GUI rather than a collection of unrelated tool pages. Integrations may expose tool-specific options, but navigation, status, execution feedback, history and common interactions should remain consistent.

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
