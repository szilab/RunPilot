// Package pluginapi is the small convenience layer used by first-party WASM
// plugins.  It deliberately exposes JSON data rather than Go host objects so
// the wire ABI remains usable from non-Go toolchains.
package pluginapi

import (
	"encoding/json"
	"fmt"
)

const APIVersion = 1

// Keep allocations reachable for the lifetime of the module. The v1 ABI has
// no free operation: the host copies returned bytes before the next call.
func PackedBytes(value uint64) []byte    { return Bytes(uint32(value>>32), uint32(value)) }
func Pack(pointer, length uint32) uint64 { return uint64(pointer)<<32 | uint64(length) }

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type hostResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// Call asks the generic RunPilot capability host to perform an operation.
func Call(capability string, params any, result any) error {
	request, err := json.Marshal(struct {
		APIVersion int    `json:"apiVersion"`
		Capability string `json:"capability"`
		Params     any    `json:"params"`
	}{APIVersion, capability, params})
	if err != nil {
		return err
	}
	pointer := Allocate(uint32(len(request)))
	copy(Bytes(pointer, uint32(len(request))), request)
	reply := PackedBytes(hostCall(pointer, uint32(len(request))))
	var response hostResponse
	if err := json.Unmarshal(reply, &response); err != nil {
		return fmt.Errorf("decode host response: %w", err)
	}
	if !response.OK {
		if response.Error == nil {
			return fmt.Errorf("host capability %s failed", capability)
		}
		return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	if result != nil && len(response.Result) != 0 {
		return json.Unmarshal(response.Result, result)
	}
	return nil
}

func JSONResponse(value any) uint64 {
	b, err := json.Marshal(value)
	if err != nil {
		b = []byte(`{"error":{"code":"internal","message":"marshal response"}}`)
	}
	p := Allocate(uint32(len(b)))
	copy(Bytes(p, uint32(len(b))), b)
	return Pack(p, uint32(len(b)))
}

// RegisterSchedule and friends are deliberately JSON-shaped convenience
// wrappers: schedule and process semantics remain owned by the plugin.
func RegisterSchedule(value any, result any) error { return Call("scheduler.register", value, result) }
func RemoveSchedule(id string) error {
	return Call("scheduler.remove", map[string]string{"id": id}, nil)
}
func ListSchedules(result any) error           { return Call("scheduler.list", map[string]any{}, result) }
func StartProcess(value any, result any) error { return Call("process.start", value, result) }
func ProcessStatus(id string, result any) error {
	return Call("process.status", map[string]string{"id": id}, result)
}
func TerminateProcess(id string, result any) error {
	return Call("process.terminate", map[string]string{"id": id}, result)
}
func PublishEvent(event string, data any) error {
	return Call("events.publish", struct {
		Event string `json:"event"`
		Data  any    `json:"data"`
	}{event, data}, nil)
}
