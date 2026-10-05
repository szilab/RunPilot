# Backlog

- **Migrate Xpra to `remote.xpra`.** Xpra intentionally remains in legacy core,
  while its UI is intentionally not exposed after the RDP/VNC plugin migration.
  The migration is deferred because Xpra owns subprocess/display lifecycle and
  a local HTTP/WebSocket HTML5 endpoint with reverse proxying. Before
  implementation, decide on reusable host capabilities for process, listener,
  and browser publication lifecycle; do not add Xpra semantics to core. Once
  plugin parity exists, remove the remaining legacy Remote/Xpra core code.

- **Web Apps Phase 2.** Add upstream application WebSocket tunnelling/shim and
  generic header-based base-path compatibility (`X-Script-Name`, motivated by
  Calibre-Web, `X-Forwarded-Prefix`, `X-Forwarded-Host`, `X-Forwarded-Proto`),
  optional/custom static upstream request headers, richer Location/cookie
  handling and JavaScript-visible cookie behavior (`document.cookie`). Build
  compatibility profiles from generic proxy behaviors, without application
  semantics in core.
