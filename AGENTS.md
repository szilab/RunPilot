# RunPilot agent notes

RunPilot is a Windows and Linux Go application with an embedded web UI. The core remains one native executable; installed first-party provider plugins are deliberately separate loopback-only child processes shipped beside it.

## Priorities
- Preserve the separation between process supervision, scheduled jobs, backup engines, persistence and HTTP/UI layers.
- Prefer the Go standard library. Add dependencies only when they materially reduce complexity.
- Core RunPilot functionality must remain platform-neutral. OS-specific functionality belongs behind small platform, service, or provider adapters selected by Go build constraints or runtime capability detection.
- Use Windows SCM on Windows and systemd on Linux; do not replace either with a wrapper.
- Do not turn backup definitions into arbitrary shell scripts. Backup engines are typed adapters; Robocopy is Windows-only and Restic/rdiff-backup remain portable adapters.
- Treat Robocopy exit codes 0-7 as successful/non-fatal and 8+ as failure.
- New GUI capabilities should use the existing REST API/domain model rather than duplicating execution logic.
- Interactive Terminal sessions are PTY-backed runtime sessions and remain separate from non-interactive Process and Scheduler command execution.
- Docker support is Linux-only and restricted to explicit Compose-project operations plus fixed, managed-Compose-container lifecycle, logs, and PTY terminal actions. Keep managed projects under `<dataDir>/compose`, leave external Compose projects read-only, and never add a generic Docker command API or automatic Docker privilege escalation.
- Docker volumes are lifecycle-managed only in the Docker domain. Storage exposes one `docker-volumes` virtual location plus `local`; it accesses volume contents only through short-lived Docker helper containers, never Docker host mountpoints, and preserves read-only access while running containers use a volume. Storage locations are runtime capabilities, not user-managed configuration records. Backup engines remain provider-neutral.
- Docker network support is limited to discovery, named bridge-network creation, and deletion after a no-container-reference check. Do not add arbitrary network options, container attachment controls, or generic Docker networking APIs.
- Remote targets, sessions, tickets, browser transports, diagnostics and UI are core. Technology-specific Xpra, RDP/guacd and VNC runtime implementations are first-party plugins; never recreate an in-process provider fallback.

## Validation
Before finishing a change run:
- `gofmt` on changed Go files
- `go test ./...`
- `go vet ./...`
- when platform-specific code changed: `GOOS=windows GOARCH=amd64 go build -o dist/runpilot.exe ./cmd/runpilot` and `GOOS=linux GOARCH=amd64 go build -o dist/runpilot-linux ./cmd/runpilot`
