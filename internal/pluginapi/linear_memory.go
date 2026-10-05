//go:build runpilot_wasm

package pluginapi

import "unsafe"

// allocations is retained by the current legacy ABI invocation. ABI v2 will
// replace this pointer-lifetime model with synchronous host-owned copies.
var allocations [][]byte

// ResetAllocations releases buffers retained for a completed host entry. The
// host copies a lifecycle result before the next entry, so retaining every
// historical ABI buffer is both unnecessary and an unbounded WASM leak.
func ResetAllocations() { allocations = nil }

func Allocate(size uint32) uint32 {
	if size == 0 {
		size = 1
	}
	b := make([]byte, size)
	allocations = append(allocations, b)
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

// Bytes maps a caller-owned WASM linear-memory buffer. It is deliberately
// confined to the WASM build so native checks never treat an offset as a host
// pointer.
func Bytes(pointer, length uint32) []byte {
	if length == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(pointer))), length)
}
