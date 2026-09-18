import RFB from "./vendor/novnc/core/rfb.js";

// Keep the upstream noVNC module isolated from the non-module RunPilot UI.
window.RunPilotNoVNC = { RFB };
