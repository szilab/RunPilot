// Loaded by the vendored Xpra HTML5 client page (vendor/xpra-html5/index.html)
// inside the Remote plugin's session frame. The page never opens a network
// connection of its own: its WebSocket is the authenticated RunPilot stream
// that the parent session view attached for this Xpra session.
import { createStreamChannel, StreamChannelState } from "./stream-channel.js";

const host = window.frameElement?.runpilotXpra;

// Module scripts run after the client's classic scripts but before it
// connects. Keep the Xpra protocol on this thread so it uses the stream below
// instead of a worker that would create a real WebSocket.
window.WORKER = false;
// The page-thread protocol lacks the worker host's terminate(), which the
// client calls when it closes; treat it as close.
if (typeof XpraProtocol === "function" && typeof XpraProtocol.prototype.terminate !== "function") {
  XpraProtocol.prototype.terminate = function terminate() { this.close?.(); };
}

// Match the session surface instead of the upstream wallpaper, which is not
// packaged; with the image overridden the browser never requests it.
if (globalThis.document?.head) {
  const surface = document.createElement("style");
  surface.textContent = "html, body { background: #0f172a none !important; }";
  document.head.append(surface);
}

let used = false;
function RunPilotXpraSocket() {
  if (!host?.stream || used) {
    // A reconnect attempt cannot reuse the stream; fail like a refused socket.
    const refused = { send() { throw new Error("Xpra stream is closed"); }, close() {} };
    const channel = createStreamChannel(refused, { label: "Xpra", announceOpen: true });
    queueMicrotask(() => refused.onclose?.({ code: 1006, reason: "The RunPilot Xpra stream is no longer available" }));
    return channel;
  }
  used = true;
  const channel = createStreamChannel(host.stream, { label: "Xpra", pending: host.pending || [], announceOpen: true });
  channel.addEventListener("message", () => host.onReady?.(), { once: true });
  channel.addEventListener("close", event => host.onClose?.(event), { once: true });
  return channel;
}
Object.assign(RunPilotXpraSocket, StreamChannelState);
window.WebSocket = RunPilotXpraSocket;
