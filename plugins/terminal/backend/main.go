package main

import (
	"encoding/json"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

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
	var in envelope
	if pluginapi.ReadInput(handle, &in) != nil || in.APIVersion != pluginapi.LifecycleAPIVersion {
		writeFailure(handle, "invalid_argument", "invalid initialization input")
		return
	}
	current = newPlugin()
	_ = pluginapi.WriteOutput(handle, map[string]any{"ok": true})
}

//export runpilot_call
func runpilot_call(handle uint32) {
	var in envelope
	if pluginapi.ReadInput(handle, &in) != nil || in.APIVersion != pluginapi.LifecycleAPIVersion {
		writeFailure(handle, "invalid_argument", "invalid plugin request")
		return
	}
	out, err := current.handle(in.Operation, in.Request)
	if err != nil {
		writeFailure(handle, err.Code, err.Message)
		return
	}
	if pluginapi.WriteOutput(handle, out) != nil {
		writeFailure(handle, "output_failed", "could not encode plugin response")
	}
}

//export runpilot_event
func runpilot_event(handle uint32) {
	var in envelope
	if pluginapi.ReadInput(handle, &in) != nil {
		writeFailure(handle, "invalid_argument", "invalid event input")
		return
	}
	if err := current.event(in.Operation, in.Request); err != nil {
		writeFailure(handle, err.Code, err.Message)
		return
	}
	_ = pluginapi.WriteOutput(handle, map[string]any{"ok": true})
}

//export runpilot_shutdown
func runpilot_shutdown(handle uint32) {
	var in envelope
	if pluginapi.ReadInput(handle, &in) != nil {
		writeFailure(handle, "invalid_argument", "invalid shutdown input")
		return
	}
	current.shutdown()
	_ = pluginapi.WriteOutput(handle, map[string]any{"ok": true})
}
func main() {}
