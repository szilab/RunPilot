package main

import (
	"encoding/json"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

type request struct {
	APIVersion int             `json:"apiVersion"`
	Operation  string          `json:"operation"`
	Request    json.RawMessage `json:"request"`
}
type failure struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

var calls uint64

//export runpilot_alloc
func runpilot_alloc(size uint32) uint32 { return pluginapi.Allocate(size) }

//export runpilot_reset
func runpilot_reset() { pluginapi.ResetAllocations() }

//export runpilot_init
func runpilot_init(pointer, length uint32) uint64 {
	return pluginapi.JSONResponse(map[string]any{"ok": true})
}

//export runpilot_call
func runpilot_call(pointer, length uint32) uint64 {
	var call request
	if err := json.Unmarshal(pluginapi.Bytes(pointer, length), &call); err != nil {
		return pluginapi.JSONResponse(errorResponse("invalid_argument", "invalid plugin request"))
	}
	if call.APIVersion != pluginapi.APIVersion {
		return pluginapi.JSONResponse(errorResponse("unsupported_api", "unsupported plugin API"))
	}
	switch call.Operation {
	case "status.get":
		var status map[string]any
		if err := pluginapi.Call("system.status", map[string]any{}, &status); err != nil {
			return pluginapi.JSONResponse(errorResponse("host_failure", err.Error()))
		}
		calls++
		status["pluginCallCount"] = calls // proves per-instance runtime state.
		return pluginapi.JSONResponse(status)
	default:
		return pluginapi.JSONResponse(errorResponse("unknown_method", "unknown system method"))
	}
}

//export runpilot_shutdown
func runpilot_shutdown(pointer, length uint32) uint64 {
	return pluginapi.JSONResponse(map[string]any{"ok": true})
}

//export runpilot_event
func runpilot_event(pointer, length uint32) uint64 {
	// System has no asynchronous feature behavior yet, but exporting the event
	// entry point makes it a real reference for the host-to-plugin contract.
	return pluginapi.JSONResponse(map[string]any{"ok": true})
}

func errorResponse(code, message string) failure {
	var value failure
	value.Error.Code = code
	value.Error.Message = message
	return value
}

func main() {}
