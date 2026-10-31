# Xpra HTML5 client (vendored)

- Upstream: <https://github.com/Xpra-org/xpra-html5>, release **v19**
  (the built client as shipped in the `xpra-html5` 19 package).
- License: MPL-2.0 (`LICENSE`). Bundled libraries keep their own license
  headers; jQuery 3.6.1 and jQuery UI 1.13.2 are MIT licensed.
- Only the files the client page loads are included: `index.html`, `css/`,
  `icons/`, `js/` and `favicon.png`. Compressed `.br`/`.gz` copies, source maps
  and the other upstream pages are omitted.

## RunPilot changes

- `index.html` loads `../../xpra-bridge.js` as a module script, which runs
  after the upstream scripts and before the client connects. The bridge keeps
  the Xpra protocol on the page thread and replaces the page's `WebSocket`
  with the Remote plugin's authenticated stream, so the client never opens its
  own network connection. No upstream JavaScript is modified.
- `default-settings.txt` is a RunPilot file without server-wide defaults;
  session settings are passed as URL parameters.

To update, copy the same files from a newer release, re-apply the one
`index.html` script tag, and run the Remote plugin's browser tests.
