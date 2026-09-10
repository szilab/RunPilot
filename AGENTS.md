# RunPilot agent notes

RunPilot is a Windows-first Go application. Keep the runtime as a single native executable with an embedded web UI.

## Product direction
- Treat RunPilot as a lightweight Windows host-management control plane, not merely a process supervisor.
- Keep a clear distinction between **RunPilot capabilities** (applications, jobs, backups, storage, software management, history/health) and **external integrations** (for example Robocopy, Restic, Docker/Compose or WinGet).
- RunPilot should provide a coherent Windows GUI, configuration, scheduling, lifecycle, status, logs and history around external tools.
- Prefer delegating specialist behavior to the authoritative external tool instead of recreating it inside RunPilot.
- One integration may serve multiple capabilities when that avoids duplicated configuration or execution logic. Example: a Restic integration may serve both backup jobs and storage browsing/version restore.

## Architectural guardrails
- Preserve the separation between process supervision, scheduled jobs, backup engines/integrations, storage providers, persistence and HTTP/UI layers.
- Prefer the Go standard library. Add dependencies only when they materially reduce complexity.
- Windows behavior is authoritative; keep non-Windows build stubs only so tests and static analysis can run elsewhere.
- Do not replace native Windows service integration with NSSM, WinSW or another wrapper.
- Do not turn backup definitions into arbitrary shell scripts. Backup tools use typed adapters/integrations; `robocopy` is the first one.
- Do not implement a feature inside RunPilot just to compensate for an external tool that does not natively support it. Use another suitable integration instead. In particular, do not build versioned backup semantics on top of Robocopy.
- Keep tool-specific configuration typed. Do not create a universal catch-all backup/runtime/software schema containing every option offered by every possible backend.
- Do not turn storage browsing into a general-purpose file manager unless that is explicitly approved as a product change. Prefer browse/download/version/restore-oriented access and explicitly configured filesystem roots.
- Docker/WSL support, if added, should integrate with an already working external runtime. Do not provision WSL, install/manage Docker daemons, implement container networking/port forwarding or recreate Docker/Compose orchestration semantics unless explicitly requested as a separate product decision.
- Software installation, when added, should use external package/install providers such as WinGet rather than implementing a package manager.
- Treat Robocopy exit codes 0-7 as successful/non-fatal and 8+ as failure.
- New GUI capabilities should use backend REST/domain integrations rather than duplicating execution logic in browser JavaScript.
- Privileged filesystem/process/tool operations stay server-side. The browser is a management client only.

## Security
- Environment variables and integration configuration may contain credentials. Do not claim ordinary YAML configuration is secret/encrypted storage.
- New credential-persisting features should use a Windows-appropriate protected storage mechanism such as DPAPI when implemented.
- Storage providers must validate configured roots and provider-relative paths server-side and protect against traversal.

## Validation
Before finishing a change run:
- `gofmt` on changed Go files
- `go test ./...`
- `go vet ./...`
- when Windows-specific code changed: `GOOS=windows GOARCH=amd64 go build -o dist/runpilot.exe ./cmd/runpilot`
