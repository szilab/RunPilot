# RunPilot agent notes

RunPilot is a Windows and Linux Go application. Keep the runtime as a single native executable with an embedded web UI.

## Priorities
- Preserve the separation between process supervision, scheduled jobs, backup engines, persistence and HTTP/UI layers.
- Prefer the Go standard library. Add dependencies only when they materially reduce complexity.
- Core RunPilot functionality must remain platform-neutral. OS-specific functionality belongs behind small platform, service, or provider adapters selected by Go build constraints or runtime capability detection.
- Use Windows SCM on Windows and systemd on Linux; do not replace either with a wrapper.
- Do not turn backup definitions into arbitrary shell scripts. Backup engines are typed adapters; Robocopy is Windows-only and Restic/rdiff-backup remain portable adapters.
- Treat Robocopy exit codes 0-7 as successful/non-fatal and 8+ as failure.
- New GUI capabilities should use the existing REST API/domain model rather than duplicating execution logic.
- Interactive Terminal sessions are PTY-backed runtime sessions and remain separate from non-interactive Process and Scheduler command execution.

## Validation
Before finishing a change run:
- `gofmt` on changed Go files
- `go test ./...`
- `go vet ./...`
- when platform-specific code changed: `GOOS=windows GOARCH=amd64 go build -o dist/runpilot.exe ./cmd/runpilot` and `GOOS=linux GOARCH=amd64 go build -o dist/runpilot-linux ./cmd/runpilot`
