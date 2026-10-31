package main

import (
	"encoding/json"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

// The current plugin instance. RunPilot serializes lifecycle entry per module,
// so no locking is needed.
var current = newPlugin()

type envelope struct {
	APIVersion int             `json:"apiVersion"`
	Operation  string          `json:"operation,omitempty"`
	Request    json.RawMessage `json:"request,omitempty"`
}

type failure struct {
	Error *pluginapi.Error `json:"error"`
}

func writeFailure(handle uint32, code, message string) {
	_ = pluginapi.WriteOutput(handle, failure{Error: &pluginapi.Error{Code: code, Message: message}})
}

//export runpilot_init
func runpilot_init(handle uint32) {
	var input envelope
	if err := pluginapi.ReadInput(handle, &input); err != nil {
		writeFailure(handle, "invalid_argument", "invalid initialization input")
		return
	}
	if input.APIVersion != pluginapi.LifecycleAPIVersion {
		writeFailure(handle, "unsupported_api", "unsupported plugin API")
		return
	}
	current = newPlugin()
	current.init()
	_ = pluginapi.WriteOutput(handle, map[string]any{"ok": true})
}

//export runpilot_call
func runpilot_call(handle uint32) {
	var call envelope
	if err := pluginapi.ReadInput(handle, &call); err != nil {
		writeFailure(handle, "invalid_argument", "invalid plugin request")
		return
	}
	if call.APIVersion != pluginapi.LifecycleAPIVersion {
		writeFailure(handle, "unsupported_api", "unsupported plugin API")
		return
	}
	result, err := current.handle(call.Operation, call.Request)
	if err != nil {
		writeFailure(handle, err.Code, err.Message)
		return
	}
	if writeErr := pluginapi.WriteOutput(handle, result); writeErr != nil {
		writeFailure(handle, "output_failed", writeErr.Error())
	}
}

//export runpilot_shutdown
func runpilot_shutdown(handle uint32) {
	var input envelope
	if err := pluginapi.ReadInput(handle, &input); err != nil {
		writeFailure(handle, "invalid_argument", "invalid shutdown input")
		return
	}
	current.shutdown()
	_ = pluginapi.WriteOutput(handle, map[string]any{"ok": true})
}

//export runpilot_event
func runpilot_event(handle uint32) {
	var input envelope
	if err := pluginapi.ReadInput(handle, &input); err != nil {
		writeFailure(handle, "invalid_argument", "invalid event input")
		return
	}
	current.event(input.Operation, input.Request)
	_ = pluginapi.WriteOutput(handle, map[string]any{"ok": true})
}

func main() {}
