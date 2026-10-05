(() => {
  "use strict";
  const CONNECTING = 0, OPEN = 1, CLOSING = 2, CLOSED = 3, MAX_BUFFER = 1048576;
  const bytesOf = value => {
    if (typeof value === "string") return new TextEncoder().encode(value);
    if (value instanceof ArrayBuffer) return new Uint8Array(value.slice(0));
    if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer.slice(value.byteOffset, value.byteOffset + value.byteLength));
    return null;
  };
  class RunPilotWebSocket extends EventTarget {
    constructor(url, protocols) {
      super();
      if (arguments.length < 1) throw new TypeError("WebSocket URL is required");
      const parsed = new URL(String(url), document.baseURI);
      if ((parsed.protocol !== "ws:" && parsed.protocol !== "wss:") || parsed.host !== location.host || parsed.username || parsed.password || parsed.hash || !navigator.serviceWorker?.controller) throw new DOMException("WebSocket URL is outside this RunPilot publication", "SecurityError");
      let requested = [];
      if (typeof protocols === "string") requested = [protocols];
      else if (protocols !== undefined) {
        if (!Array.isArray(protocols)) throw new TypeError("WebSocket protocols must be a string or sequence");
        requested = [...protocols];
      }
      if (requested.some(value => typeof value !== "string" || !value || !/^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(value)) || new Set(requested).size !== requested.length) throw new DOMException("Invalid WebSocket subprotocol", "SyntaxError");
      this.url = parsed.href;
      this.protocol = "";
      this.extensions = "";
      this.readyState = CONNECTING;
      this.bufferedAmount = 0;
      this._binaryType = "blob";
      this._port = null;
      this._id = null;
      this._sendChain = Promise.resolve();
      this._handlers = Object.create(null);
      for (const name of ["open", "message", "error", "close"]) Object.defineProperty(this, "on" + name, {
        configurable: true, get: () => this._handlers[name] || null,
        set: handler => { if (this._handlers[name]) this.removeEventListener(name, this._handlers[name]); this._handlers[name] = typeof handler === "function" ? handler : null; if (this._handlers[name]) this.addEventListener(name, this._handlers[name]); }
      });
      const channel = new MessageChannel(); this._port = channel.port1;
      this._port.onmessage = ({ data }) => this._receive(data);
      this._port.start();
      navigator.serviceWorker.controller.postMessage({ type: "websocket.open", url: this.url, protocols: requested }, [channel.port2]);
    }
    get binaryType() { return this._binaryType; }
    set binaryType(value) { if (value !== "blob" && value !== "arraybuffer") throw new DOMException("Invalid binaryType", "SyntaxError"); this._binaryType = value; }
    send(value) {
      if (this.readyState === CONNECTING) throw new DOMException("WebSocket is not open", "InvalidStateError");
      if (this.readyState === CLOSING || this.readyState === CLOSED) return;
      if (typeof value !== "string" && !(value instanceof ArrayBuffer) && !ArrayBuffer.isView(value) && !(value instanceof Blob)) throw new TypeError("Unsupported WebSocket message type");
      const cost = typeof value === "string" ? new TextEncoder().encode(value).length : value instanceof Blob ? value.size : value?.byteLength ?? value?.length ?? 0;
      if (!Number.isFinite(cost) || cost > MAX_BUFFER || this.bufferedAmount + cost > MAX_BUFFER) throw new DOMException("WebSocket send buffer limit exceeded", "QuotaExceededError");
      this.bufferedAmount += cost;
      const sendValue = async () => {
        let bytes = bytesOf(value), binary = typeof value !== "string";
        if (!bytes && value instanceof Blob) { binary = true; bytes = new Uint8Array(await value.arrayBuffer()); }
        if (!bytes) throw new TypeError("Unsupported WebSocket message type");
        this.bufferedAmount += bytes.length - cost;
        this._port.postMessage({ type: "send", data: bytes, binary, amount: bytes.length }, [bytes.buffer]);
      };
      // Blob conversion is asynchronous; serialize it to preserve call order.
      this._sendChain = this._sendChain.then(sendValue).catch(error => { this.bufferedAmount = Math.max(0, this.bufferedAmount - cost); this._fail(error); });
    }
    close(code = 1000, reason = "") {
      if (code !== 1000 && (code < 3000 || code > 4999)) throw new DOMException("Invalid close code", "InvalidAccessError");
      if (new TextEncoder().encode(String(reason)).length > 123) throw new DOMException("Close reason is too long", "SyntaxError");
      if (this.readyState === CLOSING || this.readyState === CLOSED) return;
      this.readyState = CLOSING;
      this._port.postMessage({ type: "close", code, reason: String(reason) });
    }
    _receive(message) {
      if (message?.type === "open") { this.protocol = message.protocol || ""; if (this.readyState === CONNECTING) { this.readyState = OPEN; this.dispatchEvent(new Event("open")); } }
      else if (message?.type === "ready") { /* The upstream open event carries the selected protocol. */ }
      else if (message?.type === "drain") this.bufferedAmount = Math.max(0, this.bufferedAmount - (message.amount || 0));
      else if (message?.type === "message") {
        let data = message.data;
        if (message.binary && this.binaryType === "blob") data = new Blob([data]);
        this.dispatchEvent(new MessageEvent("message", { data, origin: location.origin }));
      } else if (message?.type === "close") this._finishClose(message);
      else if (message?.type === "error") this._fail(new Error(message.message || "WebSocket failed"));
    }
    _fail(error) {
      if (this.readyState === CLOSED) return;
      this.dispatchEvent(new Event("error"));
      this._finishClose({ code: 1006, reason: "", wasClean: false });
    }
    _finishClose(info) {
      if (this.readyState === CLOSED) return;
      this.readyState = CLOSED;
      this._port?.close();
      this.dispatchEvent(new CloseEvent("close", { code: info.code || 1006, reason: info.reason || "", wasClean: !!info.wasClean }));
    }
  }
  Object.assign(RunPilotWebSocket, { CONNECTING, OPEN, CLOSING, CLOSED });
  Object.defineProperties(RunPilotWebSocket.prototype, { CONNECTING: { value: CONNECTING }, OPEN: { value: OPEN }, CLOSING: { value: CLOSING }, CLOSED: { value: CLOSED } });
  globalThis.WebSocket = RunPilotWebSocket;
})();
