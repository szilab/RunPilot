# Backlog

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
