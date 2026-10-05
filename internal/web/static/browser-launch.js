// Generic restricted publication entry, before management authentication/initialization.
(async () => {
  const marker = "runpilot-publication", namePrefix = "runpilot.browser:";
  const id = value => typeof value === "string" && /^[A-Za-z0-9_-]{32}$/.test(value);
  const load = source => new Promise((resolve, reject) => {
    const script = document.createElement("script"); script.src = source;
    script.onload = resolve; script.onerror = () => reject(new Error("Browser publication could not load. Open it again from RunPilot."));
    document.head.append(script);
  });
  const params = new URLSearchParams(location.hash.slice(1));
  let launch, restricted = params.has(marker) || globalThis.name?.startsWith(namePrefix);
  if (!restricted) { await load(new URL("app.js", document.baseURI).href); return; }
  // This tab must never migrate or acquire management credentials, even on errors.
  RunPilotAuthStorage.clearShared(localStorage); sessionStorage.removeItem("runpilot.token");
  const title = document.createElement("h1"), status = document.createElement("p");
  title.textContent = "Opening application"; status.id = "status"; status.setAttribute("role", "status"); status.textContent = "Establishing the encrypted tunnel…";
  document.body.replaceChildren(title, status);
  try {
    if (params.has(marker)) {
      const fragment = location.hash;
      history.replaceState(null, "", location.pathname + location.search);
      if (fragment.length > 1024 || [...params].length !== 1) throw new Error("Invalid browser publication launch.");
      try { launch = JSON.parse(params.get(marker)); } catch { throw new Error("Invalid browser publication launch."); }
      if (!launch || Object.keys(launch).sort().join(",") !== "publication,runtime,stream,ticket" || !Object.values(launch).every(id)) throw new Error("Invalid browser publication launch.");
      globalThis.RUNPILOT_BROWSER_LAUNCH = launch;
    } else {
      if (globalThis.name.length > 512) throw new Error("Invalid browser publication lineage.");
      try { launch = JSON.parse(globalThis.name.slice(namePrefix.length)); } catch { throw new Error("Invalid browser publication lineage."); }
      if (!id(launch.runtime) || !id(launch.publication) || !id(launch.handle)) throw new Error("This gateway has expired. Open it again from RunPilot.");
    }
    const base = new URL("./", document.baseURI);
    await load(new URL("__runpilot__/browser/" + launch.runtime + "/bootstrap.js?publication=" + launch.publication, base).href);
  } catch (error) { delete globalThis.RUNPILOT_BROWSER_LAUNCH; status.textContent = error.message; status.setAttribute("role", "alert"); }
})();
