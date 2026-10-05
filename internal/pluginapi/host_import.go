//go:build runpilot_wasm

package pluginapi

//go:wasmimport runpilot host_call
func hostCall(pointer, length uint32) uint64
