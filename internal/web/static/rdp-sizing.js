(() => {
  const fallbackSize = Object.freeze({width:640,height:480});

  function normalizedSize(value) {
    const width=Math.round(Number(value?.width)||0), height=Math.round(Number(value?.height)||0);
    return width > 0 && height > 0 ? {width,height} : null;
  }

  function resolveInitialSize(value) {
    const size=normalizedSize(value);
    return size ? {...size,fallback:false} : {...fallbackSize,fallback:true};
  }

  async function measureAfterLayout(nextFrame, measure) {
    await nextFrame();
    await nextFrame();
    return measure();
  }

  function transportParams(ticket, options) {
    const params=new URLSearchParams;
    params.set("ticket",ticket);
    params.set("width",String(options.width));
    params.set("height",String(options.height));
    params.set("dpi",String(options.dpi));
    params.set("timezone",options.timezone||"");
    return params.toString();
  }

  function fitScale(available, remote) {
    const area=normalizedSize(available), framebuffer=normalizedSize(remote);
    if (!area || !framebuffer) return null;
    return Math.min(area.width/framebuffer.width,area.height/framebuffer.height);
  }

  function sameSize(first, second) {
    return Boolean(first && second && first.width===second.width && first.height===second.height);
  }

  function createController({resizeMethod,initialSize,getSurfaceSize,sendRemoteSize,fitLocal,delay=150}) {
    let connected=false, lastSent={width:initialSize.width,height:initialSize.height}, pending=null, reconciled=false, lastDisplaySize=null;

    function currentSurfaceSize() { return normalizedSize(getSurfaceSize()); }
    function sendSettledSize() {
      pending=null;
      const size=currentSurfaceSize();
      if (!connected || resizeMethod!=="display-update" || !size || sameSize(size,lastSent)) return;
      lastSent={...size};
      sendRemoteSize(size);
    }
    function scheduleResize() {
      if (!connected || resizeMethod!=="display-update") return;
      if (pending) clearTimeout(pending);
      pending=setTimeout(sendSettledSize,delay);
    }
    function fit() { fitLocal?.(currentSurfaceSize(),lastDisplaySize); }
    function reconcileInitialSize() {
      if (reconciled || !connected || resizeMethod!=="display-update" || !normalizedSize(lastDisplaySize)) return;
      reconciled=true;
      const size=currentSurfaceSize();
      if (size && !sameSize(size,initialSize)) scheduleResize();
    }

    return {
      setConnected(value) { connected=Boolean(value); if (connected) reconcileInitialSize(); },
      surfaceChanged() { fit(); scheduleResize(); },
      displayResized(size) { lastDisplaySize=normalizedSize(size); fit(); reconcileInitialSize(); },
      flushPendingResize() { if (pending) { clearTimeout(pending); sendSettledSize(); } },
      dispose() { if (pending) clearTimeout(pending); pending=null; },
    };
  }

  globalThis.RunPilotRDPSize={fallbackSize,resolveInitialSize,measureAfterLayout,transportParams,fitScale,createController};
})();
