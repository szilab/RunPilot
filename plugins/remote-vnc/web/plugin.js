export function activate(runpilot) {
  runpilot.remote.registerProviderUI({ id: "vnc", name: "VNC / noVNC", capabilities: ["desktop"] });
}
