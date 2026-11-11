// WebSocket-compatible channel over an authenticated RunPilot stream handle.
// noVNC uses it as its raw channel; the Xpra client page uses it in place of
// WebSocket. Objects are created in the realm that imports this module, so the
// Xpra client frame receives its own ArrayBuffers and events.

const CONNECTING = 0, OPEN = 1, CLOSING = 2, CLOSED = 3;
const FRAME_LIMIT = 32768;

// isView and the ArrayBuffer tag also recognize buffers from another realm.
const toBytes = data => {
  if (ArrayBuffer.isView(data)) return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  if (Object.prototype.toString.call(data) === "[object ArrayBuffer]") return new Uint8Array(data);
  return null;
};

export function createStreamChannel(stream, { label = "Remote", pending = [], announceOpen = false } = {}) {
  if (!stream || typeof stream.send !== "function" || typeof stream.close !== "function") {
    throw new TypeError("an open RunPilot stream is required");
  }
  const channel = new EventTarget();
  const handlers = Object.create(null);
  for (const name of ["open", "message", "error", "close"]) {
    // Enumerable so noVNC's Object.keys based raw channel check finds them.
    Object.defineProperty(channel, `on${name}`, {
      configurable: true,
      enumerable: true,
      get: () => handlers[name] || null,
      set: handler => { handlers[name] = typeof handler === "function" ? handler : null; },
    });
  }
  Object.assign(channel, { binaryType: "arraybuffer", protocol: "", extensions: "", bufferedAmount: 0, url: "", readyState: announceOpen ? CONNECTING : OPEN });
  const emit = (name, properties = {}) => {
    const event = new Event(name);
    for (const [key, value] of Object.entries(properties)) Object.defineProperty(event, key, { value, enumerable: true });
    handlers[name]?.call(channel, event);
    channel.dispatchEvent(event);
  };
  const finishClose = (event = {}) => {
    if (channel.readyState === CLOSED) return;
    channel.readyState = CLOSED;
    const code = Number.isInteger(event.code) ? event.code : 1006;
    emit("close", { code, reason: event.reason || `${label} stream closed`, wasClean: code === 1000 });
  };
  const reportError = error => {
    if (channel.readyState >= CLOSING) return;
    emit("error", { error, message: error?.message || `${label} stream failed` });
    finishClose({ code: 1011, reason: error?.message || `${label} stream failed` });
  };
  const receive = data => {
    if (channel.readyState !== OPEN) return;
    const bytes = toBytes(data);
    if (!bytes) { reportError(new TypeError(`${label} stream delivered non-binary data`)); return; }
    // Copy into this realm so consumers see a local ArrayBuffer.
    const copy = new Uint8Array(bytes.length); copy.set(bytes);
    emit("message", { data: copy.buffer });
  };
  channel.send = data => {
    if (channel.readyState !== OPEN) throw new Error(`${label} stream is not open`);
    const bytes = toBytes(data);
    if (!bytes) throw new TypeError(`${label} data must be binary`);
    try {
      for (let offset = 0; offset < bytes.length; offset += FRAME_LIMIT) {
        stream.send(bytes.subarray(offset, Math.min(offset + FRAME_LIMIT, bytes.length)));
      }
    } catch (error) { reportError(error); throw error; }
  };
  channel.close = () => {
    if (channel.readyState >= CLOSING) return;
    channel.readyState = CLOSING;
    try { stream.close(); } finally { finishClose({ code: 1000, reason: "closed" }); }
  };
  stream.ondata = receive;
  stream.onerror = reportError;
  stream.onclose = finishClose;
  const replay = () => { for (const chunk of pending.splice(0)) receive(chunk); };
  if (announceOpen) {
    queueMicrotask(() => {
      if (channel.readyState !== CONNECTING) return;
      channel.readyState = OPEN;
      emit("open");
      replay();
    });
  } else replay();
  return channel;
}

export const StreamChannelState = Object.freeze({ CONNECTING, OPEN, CLOSING, CLOSED });
