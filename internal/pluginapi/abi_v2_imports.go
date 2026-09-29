//go:build runpilot_wasm

package pluginapi

import "unsafe"

// ABI v2 imports and all pointer conversion live here. Each wrapper passes a
// temporary slice to a synchronous host import; it never stores that slice.
//
//go:wasmimport runpilot input_len
func abiV2InputLen(handle uint32) uint32

//go:wasmimport runpilot input_read
func abiV2InputRead(handle, dstPtr, dstLen uint32) uint32

//go:wasmimport runpilot output_write
func abiV2OutputWrite(handle, srcPtr, srcLen uint32) uint32

//go:wasmimport runpilot host_call
func abiV2HostCall(handle, reqPtr, reqLen uint32) uint32

//go:wasmimport runpilot response_len
func abiV2ResponseLen(handle, response uint32) uint32

//go:wasmimport runpilot response_read
func abiV2ResponseRead(handle, response, dstPtr, dstLen uint32) uint32

//go:wasmimport runpilot response_drop
func abiV2ResponseDrop(handle, response uint32) uint32

func v2InputLen(handle uint32) uint32 { return abiV2InputLen(handle) }
func v2InputRead(handle uint32, dst []byte) uint32 {
	return abiV2InputRead(handle, slicePointer(dst), uint32(len(dst)))
}
func v2OutputWrite(handle uint32, src []byte) uint32 {
	return abiV2OutputWrite(handle, slicePointer(src), uint32(len(src)))
}
func v2HostCall(handle uint32, req []byte) uint32 {
	return abiV2HostCall(handle, slicePointer(req), uint32(len(req)))
}
func v2ResponseLen(handle, response uint32) uint32 {
	return abiV2ResponseLen(handle, response)
}
func v2ResponseRead(handle, response uint32, dst []byte) uint32 {
	return abiV2ResponseRead(handle, response, slicePointer(dst), uint32(len(dst)))
}
func v2ResponseDrop(handle, response uint32) uint32 {
	return abiV2ResponseDrop(handle, response)
}

func slicePointer(value []byte) uint32 {
	if len(value) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&value[0])))
}
