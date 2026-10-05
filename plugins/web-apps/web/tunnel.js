(() => {
  const encoder = new TextEncoder(), decoder = new TextDecoder();
  const CHUNK = 16384, WINDOW = 65536, MAX = 16, MAX_WS = 16;
  function frame(type, id, data = new Uint8Array()) {
    const bytes = typeof data === "object" && !(data instanceof Uint8Array) ? encoder.encode(JSON.stringify(data)) : data;
    if (!id || bytes.length > CHUNK) throw new Error("Invalid HTTP tunnel frame");
    const out = new Uint8Array(10 + bytes.length), view = new DataView(out.buffer);
    out[0] = 2; out[1] = type; view.setUint32(2, id); view.setUint32(6, bytes.length); out.set(bytes, 10); return out;
  }
  function credit(n) { const out = new Uint8Array(4); new DataView(out.buffer).setUint32(0, n); return out; }
  class HTTPGatewayTunnel {
    constructor(config, ticket, streamId) {
      this.config = config; this.streamId = streamId; this.exchanges = new Map(); this.sockets = new Map(); this.waiters = []; this.nextId = 1; this.nextSocketId = 1; this.buffer = new Uint8Array(); this.dead = false;
      this.idBytes = encoder.encode(streamId);
      if (this.idBytes.length !== 32) throw new Error("Invalid stream ID");
      const url = new URL(config.basePath + "/api/v1/ws", location.origin);
      url.protocol = url.protocol === "https:" ? "wss:" : "ws:"; url.searchParams.set("ticket", ticket);
      this.socket = new RunPilotSecureWebSocket(url, "required");
      this.ready = new Promise((resolve, reject) => {
        this.rejectReady = reject;
        this.socket.onopen = () => this.socket.send(JSON.stringify({ type: "stream.attach", plugin: config.owner, streamId }));
        this.socket.onmessage = event => {
          try {
            if (typeof event.data === "string") {
              const control = JSON.parse(event.data);
              if (control.type === "stream.attached" && control.streamId === streamId) { resolve(); return; }
              throw new Error("Gateway attachment failed");
            }
            const bytes = new Uint8Array(event.data);
            if (bytes.length < 6 + this.idBytes.length || decoder.decode(bytes.slice(0, 4)) !== "RPS1" || bytes[4] !== 2 || bytes[5] !== this.idBytes.length || decoder.decode(bytes.slice(6, 6 + bytes[5])) !== streamId) throw new Error("Invalid gateway stream");
            this.receive(bytes.slice(6 + bytes[5]));
          } catch (error) { this.fail(error); }
        };
        this.socket.onerror = error => this.fail(new Error(error.message || "Encrypted transport failed"));
        this.socket.onclose = () => this.fail(new Error("Encrypted gateway disconnected. Open it again from RunPilot."));
      });
      // Avoid an unhandled rejection if a worker is initialized before anyone awaits ready.
      this.ready.catch(() => {});
    }
    async send(type, id, data) {
      if (this.dead) throw new Error("Gateway disconnected");
      const message = frame(type, id, data), out = new Uint8Array(6 + this.idBytes.length + message.length);
      out.set([82, 80, 83, 49, 1, this.idBytes.length]); out.set(this.idBytes, 6); out.set(message, 6 + this.idBytes.length);
      // Credit bounds queued exchange data; this also bounds the physical WebSocket send buffer.
      const deadline = Date.now() + 30000;
      while (this.socket.bufferedAmount > 262144) {
        if (this.dead || Date.now() > deadline) throw new Error("Gateway send buffer stalled");
        await new Promise(resolve => setTimeout(resolve, 10));
      }
      await this.socket.send(out);
    }
    receive(bytes) {
      if (this.buffer.length + bytes.length > 65536) throw new Error("Gateway parser buffer exceeded");
      const buffer = new Uint8Array(this.buffer.length + bytes.length); buffer.set(this.buffer); buffer.set(bytes, this.buffer.length); this.buffer = buffer;
      while (this.buffer.length >= 10) {
        const view = new DataView(this.buffer.buffer, this.buffer.byteOffset), size = view.getUint32(6), id = view.getUint32(2), type = this.buffer[1];
        if (this.buffer[0] !== 2 || size > CHUNK || !id) throw new Error("Invalid HTTP tunnel framing");
        if (this.buffer.length < 10 + size) return;
        const data = this.buffer.slice(10, 10 + size); this.buffer = this.buffer.slice(10 + size);
        if ((id & 0x80000000) !== 0) { this.receiveSocket(type, id, data); continue; }
        const exchange = this.exchanges.get(id); if (!exchange) continue;
        if (type === 9) {
          if (data.length !== 4) throw new Error("Invalid upload credit");
          const amount = new DataView(data.buffer).getUint32(0);
          if (!amount || amount > WINDOW || exchange.uploadCredit + amount > WINDOW) throw new Error("Upload window exceeded");
          exchange.uploadCredit += amount; exchange.wakeUpload?.();
        } else if (type === 5) {
          if (exchange.started) throw new Error("Duplicate response start"); exchange.started = true;
          const meta = JSON.parse(decoder.decode(data));
          const headers = new Headers(); for (const [key, values] of Object.entries(meta.headers || {})) for (const value of values) headers.append(key, value);
          headers.delete("set-cookie"); headers.delete("service-worker-allowed");
          const noBody = exchange.method === "HEAD" || [204, 205, 304].includes(meta.status);
          let body = noBody ? null : new ReadableStream({
            start: controller => { exchange.controller = controller; },
            pull: () => {
              // Grant only enough credit to fill one bounded browser queue window.
              const amount = Math.max(0, exchange.controller.desiredSize) - exchange.downloadCredit;
              if (amount > 0) { exchange.downloadCredit += amount; return this.send(10, id, credit(amount)); }
            },
            cancel: () => this.cancel(id),
          }, { highWaterMark: WINDOW, size: bytes => bytes.length });
          // ReadableStream's size callback counts enqueues; desiredSize tracks consumption.
          const encoding = headers.get("content-encoding");
          if (body && encoding && encoding !== "identity") {
            // Synthetic Responses do not run the browser network decoder. Decode
            // incrementally here; compressed bytes still cross the encrypted tunnel.
            const encodings = encoding.split(",").map(value => value.trim()).reverse();
            for (const format of encodings) body = body.pipeThrough(new DecompressionStream(format === "x-gzip" ? "gzip" : format));
            headers.delete("content-encoding"); headers.delete("content-length");
          }
          exchange.resolve(new Response(body, { status: meta.status, headers }));
          if (noBody) { exchange.downloadCredit = WINDOW; void this.send(10, id, credit(WINDOW)); }
        } else if (type === 6) {
          if (!exchange.started || data.length > exchange.downloadCredit) throw new Error("Download window exceeded");
          exchange.downloadCredit -= data.length;
          if (exchange.controller) {
            exchange.controller.enqueue(data);
          }
        } else if (type === 7 || type === 8) {
          if (type === 8) { const error = new Error(decoder.decode(data) || "Upstream failed"); exchange.reject(error); exchange.controller?.error(error); }
          else exchange.controller?.close();
          this.finish(id);
        } else throw new Error("Unknown HTTP tunnel message");
      }
    }
    async request(request) {
      await this.ready;
      while (!this.dead && this.exchanges.size >= MAX) {
        if (this.waiters.length >= 64) throw new Error("Gateway pending request limit reached");
        await new Promise((resolve, reject) => {
          const waiter = { resolve, reject };
          const abort = () => { const index = this.waiters.indexOf(waiter); if (index >= 0) this.waiters.splice(index, 1); reject(new Error("HTTP exchange canceled")); };
          waiter.resolve = () => { request.signal.removeEventListener("abort", abort); resolve(); };
          waiter.reject = error => { request.signal.removeEventListener("abort", abort); reject(error); };
          request.signal.addEventListener("abort", abort, { once: true });
          if (request.signal.aborted) { abort(); return; }
          this.waiters.push(waiter);
        });
      }
      if (this.dead || this.nextId > 0x7fffffff) throw new Error("Gateway unavailable or exchange limit reached");
      const id = this.nextId++, url = new URL(request.url);
      if (url.origin !== location.origin || !(url.pathname === this.config.publicPrefix || url.pathname.startsWith(this.config.publicPrefix + "/"))) throw new Error("Request outside gateway publication");
      const headers = {}; request.headers.forEach((value, key) => { headers[key] = [value]; });
      if (request.referrer && request.referrer !== "about:client") headers["referer"] = [request.referrer];
      headers["accept-encoding"] = [request.headers.has("range") ? "identity" : "gzip, deflate"];
      const exchange = { method: request.method, uploadCredit: 0, downloadCredit: 0, signal: request.signal };
      const response = new Promise((resolve, reject) => { exchange.resolve = resolve; exchange.reject = reject; });
      this.exchanges.set(id, exchange);
      exchange.abort = () => this.cancel(id); request.signal.addEventListener("abort", exchange.abort, { once: true });
      if (request.signal.aborted) { this.cancel(id); return response; }
      try {
        await this.send(1, id, { method: request.method, path: url.pathname + url.search, headers });
        // Firefox versions without Request.body expose incoming upload data as a
        // browser-managed Blob. Read that Blob as a stream, never an ArrayBuffer.
        void (async () => {
          const body = request.body === undefined && !["GET", "HEAD"].includes(request.method) ? (await request.blob()).stream() : request.body;
          await this.upload(id, body);
        })().catch(error => { if (this.exchanges.has(id)) { exchange.reject(error); this.cancel(id); } });
      } catch (error) { exchange.reject(error); this.cancel(id); }
      return response;
    }
    async upload(id, body) {
      const exchange = this.exchanges.get(id); if (!exchange) return;
      let reader, byteReader = false;
      if (body) { try { reader = body.getReader({ mode: "byob" }); byteReader = true; } catch { reader = body.getReader(); } }
      exchange.reader = reader;
      try {
        if (reader) for (;;) {
          const { done, value } = await (byteReader ? reader.read(new Uint8Array(CHUNK)) : reader.read()); if (done) break;
          if (value.length > WINDOW) throw new Error("Request body chunk exceeds the bounded reader limit");
          for (let offset = 0; offset < value.length;) {
            while (exchange.uploadCredit === 0) { if (!this.exchanges.has(id)) return; await new Promise(resolve => { exchange.wakeUpload = resolve; }); }
            if (!this.exchanges.has(id)) return;
            const length = Math.min(CHUNK, exchange.uploadCredit, value.length - offset);
            exchange.uploadCredit -= length; await this.send(2, id, value.slice(offset, offset + length)); offset += length;
          }
        }
        if (this.exchanges.has(id)) await this.send(3, id);
      } finally { reader?.releaseLock(); }
    }
    finish(id) {
      const exchange = this.exchanges.get(id); if (!exchange) return;
      this.exchanges.delete(id); this.waiters.shift()?.resolve(); exchange.signal.removeEventListener("abort", exchange.abort); exchange.wakeUpload?.(); void exchange.reader?.cancel().catch(() => {});
    }
    cancel(id) {
      const exchange = this.exchanges.get(id); if (!exchange) return;
      const error = new Error("HTTP exchange canceled"); exchange.reject(error); exchange.controller?.error(error);
      void this.send(4, id).catch(() => {}); this.finish(id);
    }
    async openSocket(path, protocols, notify) {
      await this.ready;
      if (this.dead || this.sockets.size >= MAX_WS || this.nextSocketId >= 0x7fffffff) throw new Error("WebSocket gateway limit reached");
      const id = (0x80000000 | this.nextSocketId++) >>> 0;
      const socket = { notify, buffer: new Uint8Array(), opened: false };
      const opened = new Promise((resolve, reject) => { socket.resolve = resolve; socket.reject = reject; socket.timer = setTimeout(() => reject(new Error("Upstream WebSocket timed out")), 30000); });
      opened.catch(() => {}); this.sockets.set(id, socket);
      try { await this.send(11, id, { path, protocols }); await opened; return id; }
      catch (error) { this.sockets.delete(id); void this.send(15, id, { code: 1000, reason: "open failed" }).catch(() => {}); throw error; }
    }
    async sendSocket(id, data, binary) {
      const socket = this.sockets.get(id); if (!socket || this.dead) throw new Error("WebSocket is closed");
      const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
      for (let offset = 0; offset < bytes.length || (bytes.length === 0 && offset === 0);) {
        const length = Math.min(CHUNK - 2, bytes.length - offset), final = offset + length === bytes.length;
        const payload = new Uint8Array(length + 2); payload[0] = binary ? 2 : 1; payload[1] = final ? 1 : 0; payload.set(bytes.subarray(offset, offset + length), 2);
        await this.send(binary ? 14 : 13, id, payload); offset += length; if (final) break;
      }
    }
    async closeSocket(id, code, reason) {
      const socket = this.sockets.get(id); if (!socket) return;
      socket.closing = true; await this.send(15, id, { code: code || 1000, reason: reason || "" });
    }
    receiveSocket(type, id, data) {
      const socket = this.sockets.get(id); if (!socket) return;
      if (type === 12) {
        if (socket.opened) throw new Error("Duplicate WebSocket open response");
        const meta = JSON.parse(decoder.decode(data)); socket.opened = true; clearTimeout(socket.timer); socket.resolve(meta); socket.notify({ type: "open", protocol: meta.protocol || "" });
        const initial = credit(WINDOW); void this.send(18, id, initial); return;
      }
      if (type === 17) { const error = new Error(decoder.decode(data) || "Upstream WebSocket failed"); clearTimeout(socket.timer); socket.reject(error); socket.notify({ type: "error", message: error.message }); this.sockets.delete(id); return; }
      if (type === 16) { let close = {}; try { close = JSON.parse(decoder.decode(data)); } catch {} const code = close.code || 1006; socket.notify({ type: "close", code, reason: close.reason || "", wasClean: typeof close.wasClean === "boolean" ? close.wasClean : code !== 1006 }); this.sockets.delete(id); return; }
      if (type !== 13 && type !== 14) throw new Error("Unknown WebSocket tunnel message");
      if (data.length < 2 || data[0] !== (type === 13 ? 1 : 2) || data[1] > 1) throw new Error("Invalid WebSocket data frame");
      const chunk = data.slice(2), buffer = new Uint8Array(socket.buffer.length + chunk.length); buffer.set(socket.buffer); buffer.set(chunk, socket.buffer.length); socket.buffer = buffer;
      if (buffer.length > 1048576) throw new Error("WebSocket message limit exceeded");
      if (chunk.length) void this.send(18, id, credit(chunk.length));
      if (data[1] === 1) { socket.notify({ type: "message", data: type === 13 ? decoder.decode(buffer) : buffer.buffer, binary: type === 14 }); socket.buffer = new Uint8Array(); }
    }
    fail(error) {
      if (this.dead) return; this.dead = true; this.rejectReady(error);
      for (const waiter of this.waiters.splice(0)) waiter.reject(error);
      for (const [id, exchange] of this.exchanges) { exchange.reject(error); exchange.controller?.error(error); this.finish(id); }
      for (const [id, socket] of this.sockets) { clearTimeout(socket.timer); socket.reject(error); socket.notify({ type: "close", code: 1006, reason: "gateway disconnected", wasClean: false }); this.sockets.delete(id); }
      this.socket.close(1008, "gateway failed");
    }
    close() { this.fail(new Error("Gateway closed")); }
  }
  globalThis.HTTPGatewayTunnel = HTTPGatewayTunnel;
})();
