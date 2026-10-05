# Backlog

- **Migrate Storage next.** Move browsing and file operations to a plugin, then
  retire the hidden legacy Storage UI and backend after parity.
- **Remove transitional Docker code after Storage migration.** Delete the legacy
  Docker manager, HTTP handlers and attach endpoint once Storage and remaining
  state migration dependencies are gone. Restore Docker-volume browsing from
  the Docker plugin to the Storage plugin when both sides have stable contracts.
- **Migrate Software.** Move provider management to a plugin before removing
  the hidden legacy Software UI and backend.
- **Migrate Backup separately.** Preserve typed backup jobs and history until
  a Backup plugin has parity; do not fold them into Tasks.
- **Decide legacy Tasks migration and cleanup.** Legacy definitions in
  `runpilot.yaml` and Run history remain separate from plugin state. Decide if
  and how to migrate them before removing legacy Tasks UI, code and APIs.
- **Add host metrics widgets.** Design CPU, memory, disk and issue presentation
  as plugin/widget contributions to Overview; do not expand the System fixture.

- **Migrate Xpra to `remote.xpra`.** Xpra intentionally remains in legacy core,
  while its UI is intentionally not exposed after the RDP/VNC plugin migration.
  The migration is deferred because Xpra owns subprocess/display lifecycle and
  a local HTTP/WebSocket HTML5 endpoint with reverse proxying. Before
  implementation, decide on reusable host capabilities for process, listener,
  and browser publication lifecycle; do not add Xpra semantics to core. Once
  plugin parity exists, remove the remaining legacy Remote/Xpra core code.

- **Web Apps compatibility follow-up.** Keep JavaScript-visible server cookie
  behavior (`document.cookie`) deferred unless a concrete tested application
  needs it. Investigate unusual proxy compatibility only when found in real use.
- **Safely embedded Web Apps.** Investigate an isolated embedded view that can
  preserve the RunPilot shell without granting the application management
  authority; do not use a same-origin iframe.
