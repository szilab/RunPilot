(() => {
  const MAGIC = new Uint8Array([82,80,83,49]);
  const TAG = new Uint8Array([82,80,87,69]);
  const VERSION = 1;
  const textEncoder = new TextEncoder();
  const textDecoder = new TextDecoder();
  const equal = (a,b) => a.length === b.length && a.every((v,i) => v === b[i]);
  const concat = (...parts) => { const out = new Uint8Array(parts.reduce((n,p) => n + p.length, 0)); let offset=0; for (const part of parts) { out.set(part, offset); offset += part.length; } return out; };
  const u64 = value => { const out=new Uint8Array(8), view=new DataView(out.buffer); view.setBigUint64(0,value); return out; };
  const nonce = (base, sequence) => { const out=base.slice(), view=new DataView(out.buffer); view.setBigUint64(4, view.getBigUint64(4) ^ sequence); return out; };
  const randomBytes = length => crypto.getRandomValues(new Uint8Array(length));

  class SecureWebSocket {
    constructor(url, mode="disabled", protocols) {
      this.url=url; this.mode=mode; this.protocols=protocols; this.socket=null; this._binaryType="arraybuffer"; this.sendSequence=0n; this.receiveSequence=0n; this.sendChain=Promise.resolve(); this.receiveChain=Promise.resolve(); this._failed=false; this._ready=false; this._stateOverride=null;
      this.onopen=null; this.onmessage=null; this.onerror=null; this.onclose=null;
      this._start();
    }
    get protocol() { return this.socket?.protocol || ""; }
    get extensions() { return this.socket?.extensions || ""; }
    get bufferedAmount() { return this.socket?.bufferedAmount || 0; }
    get binaryType() { return this.socket?.binaryType || this._binaryType; }
    set binaryType(value) { this._binaryType=value; if (this.socket) this.socket.binaryType=value; }
    get readyState() { if (this._stateOverride !== null) return this._stateOverride; if (!this.socket || this.socket.readyState !== WebSocket.OPEN) return this.socket?.readyState ?? WebSocket.CONNECTING; return this._ready || this.mode === "disabled" ? WebSocket.OPEN : WebSocket.CONNECTING; }
    async _start() {
      try {
        if (this.mode === "disabled") {
          const socket=new WebSocket(this.url,this.protocols); this.socket=socket; socket.binaryType=this._binaryType;
          socket.onopen=()=>this.onopen?.(); socket.onmessage=event=>this.onmessage?.(event); socket.onerror=event=>this.onerror?.(event); socket.onclose=event=>{ this._stateOverride=WebSocket.CLOSED; this.onclose?.(event); }; return;
        }
        if (!globalThis.isSecureContext || !globalThis.crypto?.subtle) throw new Error("Secure WebSocket requires HTTPS and Web Crypto");
        const keyPair=await crypto.subtle.generateKey({name:"ECDH",namedCurve:"P-256"},false,["deriveBits"]);
        const publicKey=new Uint8Array(await crypto.subtle.exportKey("raw",keyPair.publicKey));
        const clientRandom=randomBytes(32), hello=concat(MAGIC,new Uint8Array([VERSION,1]),publicKey,clientRandom);
        const socket=new WebSocket(this.url,this.protocols); this.socket=socket; socket.binaryType=this._binaryType;
        socket.onopen=async()=>{ try { socket.send(hello); const serverHello=await this._waitForServerHello(socket); await this._finish(keyPair.privateKey,clientRandom,serverHello); if (this._failed) return; this._ready=true; this.onopen?.(); } catch (error) { this._fail(error); } };
        socket.onmessage=event=>{ if(this._ready)this._receive(event); };
        socket.onerror=event=>this.onerror?.(event);
        socket.onclose=event=>{ this._stateOverride=WebSocket.CLOSED; this.onclose?.(event); };
      } catch (error) { this._fail(error); }
    }
    _waitForServerHello(socket) {
      return new Promise((resolve,reject)=>{
        let settled=false;
        const timer=setTimeout(()=>finish(reject,new Error("secure WebSocket handshake timed out")),10000);
        const cleanup=()=>{ clearTimeout(timer); socket.removeEventListener("message",onMessage); socket.removeEventListener("error",onError); socket.removeEventListener("close",onClose); };
        const finish=(callback,value)=>{ if(settled)return; settled=true; cleanup(); callback(value); };
        const onMessage=async event=>{ try { const data=event.data instanceof ArrayBuffer ? event.data : await event.data.arrayBuffer(); finish(resolve,new Uint8Array(data)); } catch (error) { finish(reject,error); } };
        const onError=()=>finish(reject,new Error("secure WebSocket handshake failed"));
        const onClose=()=>finish(reject,new Error("secure WebSocket closed during handshake"));
        socket.addEventListener("message",onMessage); socket.addEventListener("error",onError); socket.addEventListener("close",onClose);
      });
    }
    async _finish(privateKey, clientRandom, data) {
      if(data.length!==103 || !equal(data.slice(0,4),MAGIC) || data[4]!==VERSION || data[5]!==2) throw new Error("invalid secure WebSocket server hello");
      const serverKey=await crypto.subtle.importKey("raw",data.slice(6,71),{name:"ECDH",namedCurve:"P-256"},false,[]);
      const shared=await crypto.subtle.deriveBits({name:"ECDH",public:serverKey},privateKey,256);
      const hkdf=await crypto.subtle.importKey("raw",shared,"HKDF",false,["deriveBits"]);
      const info=textEncoder.encode("RunPilot WebSocket secure channel v1"), salt=concat(clientRandom,data.slice(71));
      const material=new Uint8Array(await crypto.subtle.deriveBits({name:"HKDF",hash:"SHA-256",salt,info},hkdf,88*8));
      const c2s=await crypto.subtle.importKey("raw",material.slice(0,32),{name:"AES-GCM"},false,["encrypt"]), s2c=await crypto.subtle.importKey("raw",material.slice(32,64),{name:"AES-GCM"},false,["decrypt"]);
      this.writeKey=c2s; this.readKey=s2c; this.writeNonce=material.slice(64,76); this.readNonce=material.slice(76,88);
    }
    send(data) {
      if(this.readyState!==WebSocket.OPEN) throw new Error("secure WebSocket is not open");
      if (this.mode === "disabled") { this.socket.send(data); return; }
      const bytes=typeof data === "string" ? textEncoder.encode(data) : new Uint8Array(data), kind=typeof data === "string" ? 1 : 2;
      this.sendChain=this.sendChain.then(async()=>{
        if(this.readyState!==WebSocket.OPEN) throw new Error("secure WebSocket is not open");
        const sequence=this.sendSequence, header=concat(TAG,new Uint8Array([VERSION,kind]),u64(sequence));
        const cipher=await crypto.subtle.encrypt({name:"AES-GCM",iv:nonce(this.writeNonce,sequence),additionalData:header},this.writeKey,bytes);
        if(this.readyState!==WebSocket.OPEN) throw new Error("secure WebSocket is not open");
        this.socket.send(concat(header,new Uint8Array(cipher)));
        this.sendSequence++;
      }).catch(error=>this._reportError(error));
    }
    close(code, reason) { this.socket?.close(code,reason); }
    _receive(event) {
      this.receiveChain=this.receiveChain.then(()=>this._decrypt(event)).catch(error=>this._fail(error));
    }
    async _decrypt(event) {
      const raw=event.data instanceof ArrayBuffer ? event.data : await event.data.arrayBuffer();
      const data=new Uint8Array(raw); if(data.length<30 || !equal(data.slice(0,4),TAG) || data[4]!==VERSION) throw new Error("invalid encrypted WebSocket envelope");
      const view=new DataView(data.buffer,data.byteOffset,data.byteLength), sequence=view.getBigUint64(6); if(sequence!==this.receiveSequence) throw new Error("invalid encrypted WebSocket sequence");
      const plain=await crypto.subtle.decrypt({name:"AES-GCM",iv:nonce(this.readNonce,sequence),additionalData:data.slice(0,14)},this.readKey,data.slice(14));
      this.receiveSequence++; const kind=data[5]===1?"text":"binary"; this.onmessage?.({data:kind==="text"?textDecoder.decode(plain):plain});
    }
    _reportError(error) { this.onerror?.(error); }
    _fail(error) { if(this._failed)return; this._failed=true; this._stateOverride=WebSocket.CLOSED; this._reportError(error); this.socket?.close(1008,"secure WebSocket negotiation failed"); }
  }
  window.RunPilotSecureWebSocket=SecureWebSocket;
})();
