(async () => {
  const config = globalThis.RUNPILOT_PUBLICATION, launch = globalThis.RUNPILOT_BROWSER_LAUNCH;
  delete globalThis.RUNPILOT_BROWSER_LAUNCH;
  const status = document.getElementById("status"), namePrefix = "runpilot.browser:";
  try {
    RunPilotAuthStorage.clearShared(localStorage); sessionStorage.removeItem("runpilot.token");
    let lineage;
    try { if (globalThis.name?.startsWith(namePrefix) && globalThis.name.length <= 512) lineage = JSON.parse(globalThis.name.slice(namePrefix.length)); } catch { /* An application window name is not automatically a capability. */ }
    const initialize = !!launch;
    if (initialize ? launch.runtime !== config.runtimeId || launch.publication !== config.publicationId : lineage?.runtime !== config.runtimeId || lineage?.publication !== config.publicationId || !lineage?.handle) throw new Error("This gateway has expired. Open the Web App again from RunPilot.");
    if (!isSecureContext || !crypto.subtle || !navigator.serviceWorker) throw new Error("Web Apps require HTTPS, Service Workers and encrypted WebSocket support.");
    const scriptURL = new URL(config.workerURL, location.origin).href;
    // Retire this owner's legacy target-scoped worker before entering its path;
    // a more specific old registration would otherwise win over the base worker.
    for (const registration of await navigator.serviceWorker.getRegistrations()) {
      const worker = registration.active || registration.waiting || registration.installing;
      if (registration.scope === new URL(config.publicPrefix + "/", location.origin).href && worker && new URL(worker.scriptURL).pathname === config.publicPrefix + "/__runpilot__/sw.js") await registration.unregister();
    }
    await navigator.serviceWorker.register(scriptURL, { scope: config.scope, updateViaCache: "none" });
    await new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error("Service Worker could not activate this publication")), 10000);
      navigator.serviceWorker.ready.then(() => { clearTimeout(timeout); resolve(); }, error => { clearTimeout(timeout); reject(error); });
    });
    if (navigator.serviceWorker.controller?.scriptURL !== scriptURL) await new Promise((resolve, reject) => {
      const changed = () => {
        if (navigator.serviceWorker.controller?.scriptURL !== scriptURL) return;
        clearTimeout(timeout); navigator.serviceWorker.removeEventListener("controllerchange", changed); resolve();
      };
      const timeout = setTimeout(() => { navigator.serviceWorker.removeEventListener("controllerchange", changed); reject(new Error("Service Worker could not control this publication")); }, 10000);
      navigator.serviceWorker.addEventListener("controllerchange", changed); changed();
    });
    const channel = new MessageChannel();
    const handoff = await new Promise((resolve, reject) => {
      const timeout = setTimeout(() => { channel.port1.close(); reject(new Error("Encrypted gateway initialization timed out")); }, 15000);
      channel.port1.onmessage = event => { clearTimeout(timeout); channel.port1.close(); event.data.ok ? resolve(event.data) : reject(new Error(event.data.error || "Encryption could not be established")); };
      navigator.serviceWorker.controller.postMessage({ type: initialize ? "gateway.initialize" : "gateway.resume", ticket: launch?.ticket, streamId: launch?.stream, handle: lineage?.handle, runtime: config.runtimeId, publication: config.publicationId, url: initialize ? new URL(config.publicPrefix + "/", location.origin).href : location.href }, [channel.port2]);
    });
    globalThis.name = namePrefix + JSON.stringify({ runtime: config.runtimeId, publication: config.publicationId, handle: handoff.handle });
    location.replace(handoff.url);
  } catch (error) { status.textContent = error.message || "The encrypted gateway could not be established"; status.setAttribute("role", "alert"); }
})();
