export function activate(runpilot) {
  runpilot.remote.registerProviderUI({ id: "rdp", name: "RDP / Guacamole", capabilities: ["desktop"] });
}
