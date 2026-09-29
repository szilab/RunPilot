//go:build !runpilot_wasm

package pluginapi

func hostCall(pointer, length uint32) uint64 { return 0 }
