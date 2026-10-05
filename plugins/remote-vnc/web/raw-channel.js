// Adapt RunPilot's authenticated binary stream handle to noVNC's raw-channel API.
export function createVNCChannelAdapter(stream) {
  if (!stream || typeof stream.send !== "function" || typeof stream.close !== "function") {
    throw new TypeError("an open RunPilot stream is required");
  }
  const channel = {
    binaryType: "arraybuffer",
    protocol: "",
    readyState: WebSocket.OPEN,
    onopen: null,
    onmessage: null,
    onerror: null,
    onclose: null,
    send(data) {
      if (channel.readyState !== WebSocket.OPEN) throw new Error("VNC stream is not open");
      const bytes = data instanceof Uint8Array ? data
        : data instanceof ArrayBuffer ? new Uint8Array(data)
        : ArrayBuffer.isView(data) ? new Uint8Array(data.buffer, data.byteOffset, data.byteLength)
        : null;
      if (!bytes) throw new TypeError("VNC data must be binary");
      try {
        for (let offset = 0; offset < bytes.length; offset += 32768) {
          stream.send(bytes.subarray(offset, Math.min(offset + 32768, bytes.length)));
        }
      }
      catch (error) { reportError(error); throw error; }
    },
    close() {
      if (channel.readyState >= WebSocket.CLOSING) return;
      channel.readyState = WebSocket.CLOSING;
      try { stream.close(); } finally { finishClose({ code: 1000, reason: "closed" }); }
    },
  };
  function reportError(error) {
    if (channel.readyState >= WebSocket.CLOSING) return;
    channel.onerror?.({ error, message: error?.message || "VNC stream failed" });
    finishClose({ code: 1011, reason: error?.message || "VNC stream failed" });
  }
  function finishClose(event = {}) {
    if (channel.readyState === WebSocket.CLOSED) return;
    channel.readyState = WebSocket.CLOSED;
    channel.onclose?.({ code: event.code || 1006, reason: event.reason || "VNC stream closed", wasClean: event.code === 1000 });
  }
  stream.ondata = data => {
    if (channel.readyState !== WebSocket.OPEN) return;
    const bytes = data instanceof Uint8Array ? data
      : data instanceof ArrayBuffer ? new Uint8Array(data)
      : ArrayBuffer.isView(data) ? new Uint8Array(data.buffer, data.byteOffset, data.byteLength)
      : null;
    if (!bytes) { reportError(new TypeError("VNC stream delivered non-binary data")); return; }
    const copy = bytes.slice();
    channel.onmessage?.({ data: copy.buffer });
  };
  stream.onerror = reportError;
  stream.onclose = finishClose;
  return channel;
}
