(async () => {
  const config = globalThis.RUNPILOT_PUBLICATION;
  const status = document.getElementById("status");
  const params = new URLSearchParams(location.hash.slice(1));
  const ticket = params.get("ticket"), streamId = params.get("stream"), publication = params.get("publication");
  // Remove the bootstrap capability before registration, network traffic, or application rendering.
  if (ticket || streamId || publication) history.replaceState(null, "", location.pathname + location.search);
  const namePrefix = "runpilot.web.apps:";
  try {
    // Clear any remaining legacy shared credential before application code runs.
    // This deliberately does not migrate it into the Web App tab.
    RunPilotAuthStorage.clearShared(localStorage);
    let lineage;
    try { if (globalThis.name?.startsWith(namePrefix)) lineage = JSON.parse(globalThis.name.slice(namePrefix.length)); } catch { /* Unrelated application window names are not capabilities. */ }
    const initialize = !!(ticket && streamId && publication === config.publicationId);
    if (!initialize && (ticket || streamId || publication || lineage?.publication !== config.publicationId || !lineage.handle)) throw new Error("This gateway has expired. Open the Web App again from RunPilot.");
    if (!isSecureContext || !crypto.subtle || !navigator.serviceWorker) throw new Error("Web Apps require HTTPS, Service Workers and encrypted WebSocket support.");
    const scope = config.publicPrefix + "/";
    const scriptURL = new URL(scope + "__runpilot__/sw.js?publication=" + encodeURIComponent(config.publicationId), location.origin).href;
    await navigator.serviceWorker.register(scriptURL, { scope, updateViaCache: "none" });
    await navigator.serviceWorker.ready;
    if (navigator.serviceWorker.controller?.scriptURL !== scriptURL) await new Promise((resolve, reject) => {
      const changed = () => {
        if (navigator.serviceWorker.controller?.scriptURL !== scriptURL) return;
        clearTimeout(timeout); navigator.serviceWorker.removeEventListener("controllerchange", changed); resolve();
      };
      const timeout = setTimeout(() => { navigator.serviceWorker.removeEventListener("controllerchange", changed); reject(new Error("Service Worker could not activate this publication")); }, 10000);
      navigator.serviceWorker.addEventListener("controllerchange", changed); changed();
    });
    const channel = new MessageChannel();
    const handoff = await new Promise((resolve, reject) => {
      const timeout = setTimeout(() => { channel.port1.close(); reject(new Error("Encrypted gateway initialization timed out")); }, 15000);
      channel.port1.onmessage = event => { clearTimeout(timeout); channel.port1.close(); event.data.ok ? resolve(event.data) : reject(new Error(event.data.error || "Encryption could not be established")); };
      navigator.serviceWorker.controller.postMessage({ type: initialize ? "gateway.initialize" : "gateway.resume", ticket, streamId, handle: lineage?.handle, publication: config.publicationId, url: location.href }, [channel.port2]);
    });
    // This in-memory tab handle grants only the already-open target session. It
    // survives document navigation, without localStorage/sessionStorage credentials.
    globalThis.name = namePrefix + JSON.stringify({ publication: config.publicationId, handle: handoff.handle });
    location.replace(handoff.url);
  } catch (error) { status.textContent = error.message || "The encrypted gateway could not be established"; status.setAttribute("role", "alert"); }
})();
