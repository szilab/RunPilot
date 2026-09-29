//go:build !runpilot_wasm

package pluginapi

func Allocate(size uint32) uint32         { return 0 }
func Bytes(pointer, length uint32) []byte { return nil }
func ResetAllocations()                   {}
