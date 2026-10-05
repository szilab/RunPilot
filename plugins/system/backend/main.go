package main

import (
	"github.com/szilab/RunPilot/internal/pluginapi"
)

type request struct {
	APIVersion int    `json:"apiVersion"`
	Operation  string `json:"operation,omitempty"`
}

type failure struct {
	Error *pluginapi.Error `json:"error"`
}

var calls uint64

//export runpilot_init
func runpilot_init(handle uint32) {
	var input request
	if err := pluginapi.ReadInput(handle, &input); err != nil {
		writeFailure(handle, "invalid_argument", "invalid initialization input")
		return
	}
	if input.APIVersion != pluginapi.LifecycleAPIVersion {
		writeFailure(handle, "unsupported_api", "unsupported plugin API")
		return
	}
	_ = pluginapi.WriteOutput(handle, map[string]any{"ok": true})
}

//export runpilot_call
func runpilot_call(handle uint32) {
	var call request
	if err := pluginapi.ReadInput(handle, &call); err != nil {
		writeFailure(handle, "invalid_argument", "invalid plugin request")
		return
	}
	if call.APIVersion != pluginapi.LifecycleAPIVersion {
		writeFailure(handle, "unsupported_api", "unsupported plugin API")
		return
	}
	switch call.Operation {
	case "status.get":
		var status map[string]any
		if err := pluginapi.CallHost("system.status", map[string]any{}, &status); err != nil {
			if capabilityErr, ok := pluginapi.AsCapabilityError(err); ok {
				_ = pluginapi.WriteOutput(handle, failure{Error: capabilityErr})
			} else {
				writeFailure(handle, "host_failure", err.Error())
			}
			return
		}
		calls++
		status["pluginCallCount"] = calls // proves per-instance runtime state.
		_ = pluginapi.WriteOutput(handle, status)
	default:
		writeFailure(handle, "unknown_method", "unknown system method")
	}
}

//export runpilot_shutdown
func runpilot_shutdown(handle uint32) {
	var input request
	if err := pluginapi.ReadInput(handle, &input); err != nil {
		writeFailure(handle, "invalid_argument", "invalid shutdown input")
		return
	}
	_ = pluginapi.WriteOutput(handle, map[string]any{"ok": true})
}

//export runpilot_event
func runpilot_event(handle uint32) {
	var input request
	if err := pluginapi.ReadInput(handle, &input); err != nil {
		writeFailure(handle, "invalid_argument", "invalid event input")
		return
	}
	// System has no asynchronous feature behavior yet. Decode and acknowledge
	// the ABI-v2 event envelope so this module exercises host-to-plugin input.
	_ = pluginapi.WriteOutput(handle, map[string]any{"ok": true})
}

func writeFailure(handle uint32, code, message string) {
	_ = pluginapi.WriteOutput(handle, failure{Error: &pluginapi.Error{Code: code, Message: message}})
}

func main() {}
