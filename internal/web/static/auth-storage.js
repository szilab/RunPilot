// Legacy browser tabs may still carry API tokens from earlier prototypes.
// Keep only a cleanup helper for published application tabs; browser login
// credentials are now server-managed cookies.
globalThis.RunPilotAuthStorage = Object.freeze({
  clearShared(storage) {
    try { storage?.removeItem("runpilot.token"); } catch {}
  }
});
