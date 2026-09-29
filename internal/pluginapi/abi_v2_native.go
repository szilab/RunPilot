//go:build !runpilot_wasm

package pluginapi

// Native builds provide inert ABI-v2 imports so SDK logic remains
// compile-checkable without treating integer offsets as native pointers.
func v2InputLen(uint32) uint32                     { return 0 }
func v2InputRead(uint32, []byte) uint32            { return 0 }
func v2OutputWrite(uint32, []byte) uint32          { return 0 }
func v2HostCall(uint32, []byte) uint32             { return 0 }
func v2ResponseLen(uint32, uint32) uint32          { return 0 }
func v2ResponseRead(uint32, uint32, []byte) uint32 { return 0 }
func v2ResponseDrop(uint32, uint32) uint32         { return 0 }
