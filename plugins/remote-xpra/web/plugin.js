export function activate(runpilot) {
  runpilot.remote.registerProviderUI({ id: "xpra", name: "Xpra", capabilities: ["application", "desktop"] });
}
