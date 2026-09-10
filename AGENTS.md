# RunPilot agent notes

RunPilot is a Windows-first Go application. Keep the runtime as a single native executable with an embedded web UI.

## Priorities
- Preserve the separation between process supervision, scheduled jobs, backup engines, persistence and HTTP/UI layers.
- Prefer the Go standard library. Add dependencies only when they materially reduce complexity.
- Windows behavior is authoritative; keep non-Windows build stubs only so tests and static analysis can run elsewhere.
- Do not replace native Windows service integration with NSSM, WinSW or another wrapper.
- Do not turn backup definitions into arbitrary shell scripts. Backup engines are typed adapters; `robocopy` is the first one.
- Treat Robocopy exit codes 0-7 as successful/non-fatal and 8+ as failure.
- New GUI capabilities should use the existing REST API/domain model rather than duplicating execution logic.

## Validation
Before finishing a change run:
- `gofmt` on changed Go files
- `go test ./...`
- `go vet ./...`
- when Windows-specific code changed: `GOOS=windows GOARCH=amd64 go build -o dist/runpilot.exe ./cmd/runpilot`
