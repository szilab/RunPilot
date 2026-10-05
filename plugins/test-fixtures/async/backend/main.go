package main

// This fixture is intentionally not a distributable plugin. Tests compile it
// with the same TinyGo ABI as first-party backends to exercise async host calls.

import (
	"encoding/json"

	"github.com/szilab/RunPilot/internal/pluginapi"
)

type envelope struct {
	APIVersion int             `json:"apiVersion"`
	Operation  string          `json:"operation"`
	Request    json.RawMessage `json:"request"`
}
type okResponse struct {
	OK bool `json:"ok"`
}
type publishedCallback struct {
	Event string `json:"event"`
}

var calls int

//export runpilot_alloc
func runpilot_alloc(n uint32) uint32 { return pluginapi.Allocate(n) }

//export runpilot_reset
func runpilot_reset() { pluginapi.ResetAllocations() }

//export runpilot_init
func runpilot_init(_, _ uint32) uint64 { return pluginapi.JSONResponse(okResponse{OK: true}) }

//export runpilot_shutdown
func runpilot_shutdown(_, _ uint32) uint64 { return pluginapi.JSONResponse(okResponse{OK: true}) }

//export runpilot_call
func runpilot_call(p, n uint32) uint64 {
	var call envelope
	if json.Unmarshal(pluginapi.Bytes(p, n), &call) != nil || call.APIVersion != pluginapi.APIVersion {
		return pluginapi.JSONResponse(map[string]any{"error": "invalid request"})
	}
	calls++
	switch call.Operation {
	case "state.get":
		return pluginapi.JSONResponse(map[string]any{"calls": calls})
	case "storage.set":
		var value any
		_ = json.Unmarshal(call.Request, &value)
		err := pluginapi.Call("storage.set", map[string]any{"key": "fixture", "value": value}, nil)
		return pluginapi.JSONResponse(map[string]any{"error": errorText(err)})
	case "storage.get":
		var value any
		err := pluginapi.Call("storage.get", map[string]any{"key": "fixture"}, &value)
		return pluginapi.JSONResponse(map[string]any{"value": value, "error": errorText(err)})
	case "schedule.register":
		err := pluginapi.RegisterSchedule(map[string]any{"id": "fixture", "callback": "tick", "schedule": map[string]any{"type": "interval", "intervalSeconds": 1}, "data": map[string]any{"source": "fixture"}}, nil)
		return pluginapi.JSONResponse(map[string]any{"error": errorText(err)})
	case "schedule.remove":
		err := pluginapi.RemoveSchedule("fixture")
		return pluginapi.JSONResponse(map[string]any{"error": errorText(err)})
	case "process.start":
		var input any
		_ = json.Unmarshal(call.Request, &input)
		var result any
		err := pluginapi.StartProcess(input, &result)
		return pluginapi.JSONResponse(map[string]any{"process": result, "error": errorText(err)})
	case "process.terminate":
		var input map[string]any
		_ = json.Unmarshal(call.Request, &input)
		var result any
		err := pluginapi.TerminateProcess(stringValue(input["id"]), &result)
		return pluginapi.JSONResponse(map[string]any{"process": result, "error": errorText(err)})
	case "publish":
		err := pluginapi.PublishEvent("fixture.published", map[string]any{"calls": calls})
		return pluginapi.JSONResponse(map[string]any{"error": errorText(err)})
	default:
		return pluginapi.JSONResponse(map[string]any{"error": "unknown method"})
	}
}

//export runpilot_event
func runpilot_event(p, n uint32) uint64 {
	var event envelope
	_ = json.Unmarshal(pluginapi.Bytes(p, n), &event)
	_ = pluginapi.PublishEvent("fixture.event", publishedCallback{Event: event.Operation})
	return pluginapi.JSONResponse(okResponse{OK: true})
}
func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}
func main() {}
