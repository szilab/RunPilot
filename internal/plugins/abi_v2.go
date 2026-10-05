package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero/api"
)

// ABI v2 never retains a view into WebAssembly memory. Handles are monotonically
// allocated opaque values, scoped to a single lifecycle invocation.
const maxABIV2Payload = 16 << 20

type abiV2Invocation struct {
	input       []byte
	output      []byte
	wroteOutput bool
}

type abiV2Response struct {
	invocation uint32
	payload    []byte
}

type abiV2State struct {
	mu          sync.Mutex
	next        uint64
	invocations map[uint32]*abiV2Invocation
	responses   map[uint32]abiV2Response
}

func newABIV2State() *abiV2State {
	return &abiV2State{next: 1, invocations: make(map[uint32]*abiV2Invocation), responses: make(map[uint32]abiV2Response)}
}

func (s *abiV2State) handle() (uint32, bool) {
	if s.next == 0 || s.next > uint64(^uint32(0)) {
		return 0, false
	}
	h := uint32(s.next)
	s.next++
	return h, true
}
func (s *abiV2State) create(input []byte) (uint32, bool) {
	if len(input) > maxABIV2Payload {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.handle()
	if !ok {
		return 0, false
	}
	s.invocations[h] = &abiV2Invocation{input: append([]byte(nil), input...)}
	return h, true
}
func (s *abiV2State) input(handle uint32) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.invocations[handle]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), v.input...), true
}
func (s *abiV2State) writeOutput(handle uint32, output []byte) bool {
	if len(output) > maxABIV2Payload {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.invocations[handle]
	if !ok || v.wroteOutput {
		return false
	}
	v.output, v.wroteOutput = append([]byte(nil), output...), true
	return true
}
func (s *abiV2State) output(handle uint32) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.invocations[handle]
	if !ok || !v.wroteOutput {
		return nil, false
	}
	return append([]byte(nil), v.output...), true
}
func (s *abiV2State) remove(handle uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.invocations, handle)
	for response, value := range s.responses {
		if value.invocation == handle {
			delete(s.responses, response)
		}
	}
}
func (s *abiV2State) response(handle uint32, payload []byte) (uint32, bool) {
	if len(payload) > maxABIV2Payload {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.invocations[handle]; !ok {
		return 0, false
	}
	r, ok := s.handle()
	if !ok {
		return 0, false
	}
	s.responses[r] = abiV2Response{invocation: handle, payload: append([]byte(nil), payload...)}
	return r, true
}
func (s *abiV2State) readResponse(invocation, response uint32) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.responses[response]
	if !ok || v.invocation != invocation {
		return nil, false
	}
	return append([]byte(nil), v.payload...), true
}
func (s *abiV2State) dropResponse(invocation, response uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.responses[response]
	if !ok || v.invocation != invocation {
		return false
	}
	delete(s.responses, response)
	return true
}

// copyFromWASM and copyToWASM make a fresh copy on every operation. The uint64
// checks make malformed pointer/length pairs safe even before wazero is called.
func copyFromWASM(memory api.Memory, pointer, length uint32) ([]byte, bool) {
	if memory == nil || uint64(pointer)+uint64(length) > uint64(memory.Size()) {
		return nil, false
	}
	if length == 0 {
		return []byte{}, true
	}
	b, ok := memory.Read(pointer, length)
	if !ok {
		return nil, false
	}
	return append([]byte(nil), b...), true
}
func copyToWASM(memory api.Memory, pointer uint32, value []byte) bool {
	if memory == nil || uint64(pointer)+uint64(len(value)) > uint64(memory.Size()) {
		return false
	}
	if len(value) == 0 {
		return true
	}
	return memory.Write(pointer, value)
}
func withinABIV2Limit(length uint32) bool { return uint64(length) <= maxABIV2Payload }

func (r *Runtime) v2State() *abiV2State {
	if r.v2 == nil {
		r.v2 = newABIV2State()
	}
	return r.v2
}
func (r *Runtime) v2InputLen(handle uint32) uint32 {
	b, ok := r.v2State().input(handle)
	if !ok {
		return 0
	}
	return uint32(len(b))
}
func (r *Runtime) v2InputRead(_ context.Context, module api.Module, handle, pointer, length uint32) uint32 {
	b, ok := r.v2State().input(handle)
	if !ok || uint64(len(b)) > uint64(length) || !copyToWASM(module.Memory(), pointer, b) {
		return 0
	}
	return uint32(len(b))
}
func (r *Runtime) v2OutputWrite(_ context.Context, module api.Module, handle, pointer, length uint32) uint32 {
	if !withinABIV2Limit(length) {
		return 0
	}
	b, ok := copyFromWASM(module.Memory(), pointer, length)
	if !ok || !r.v2State().writeOutput(handle, b) {
		return 0
	}
	return 1
}
func (r *Runtime) v2ResponseLen(invocation, response uint32) uint32 {
	b, ok := r.v2State().readResponse(invocation, response)
	if !ok {
		return 0
	}
	return uint32(len(b))
}
func (r *Runtime) v2ResponseRead(_ context.Context, module api.Module, invocation, response, pointer, length uint32) uint32 {
	b, ok := r.v2State().readResponse(invocation, response)
	if !ok || uint64(len(b)) > uint64(length) || !copyToWASM(module.Memory(), pointer, b) {
		return 0
	}
	return uint32(len(b))
}
func (r *Runtime) v2ResponseDrop(invocation, response uint32) uint32 {
	if r.v2State().dropResponse(invocation, response) {
		return 1
	}
	return 0
}
func (r *Runtime) v2HostCall(ctx context.Context, module api.Module, invocation, pointer, length uint32) uint32 {
	if !withinABIV2Limit(length) {
		return 0
	}
	request, ok := copyFromWASM(module.Memory(), pointer, length)
	if !ok {
		return 0
	}
	payload, err := json.Marshal(r.dispatchCapability(ctx, request))
	if err != nil || len(payload) > maxABIV2Payload {
		return 0
	}
	h, ok := r.v2State().response(invocation, payload)
	if !ok {
		return 0
	}
	return h
}

func (r *Runtime) validateABIV2() error {
	if r.module.Memory() == nil {
		return fmt.Errorf("plugin %q does not export linear memory", r.manifest.ID)
	}
	for _, name := range []string{exportInit, exportCall, exportShutdown} {
		if err := r.requireFunction(name, []api.ValueType{api.ValueTypeI32}, nil); err != nil {
			return err
		}
	}
	if event := r.module.ExportedFunction(exportEvent); event != nil {
		if err := r.requireFunction(exportEvent, []api.ValueType{api.ValueTypeI32}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) invokeV2(parent context.Context, name string, value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxABIV2Payload {
		return nil, fmt.Errorf("plugin %q %s input exceeds ABI v2 payload limit", r.manifest.ID, name)
	}
	ctx, cancel := context.WithTimeout(parent, DefaultCallTimeout)
	defer cancel()
	handle, ok := r.v2State().create(payload)
	if !ok {
		return nil, fmt.Errorf("plugin %q %s could not allocate an ABI v2 invocation handle", r.manifest.ID, name)
	}
	defer r.v2State().remove(handle)
	function := r.module.ExportedFunction(name)
	if function == nil {
		return nil, fmt.Errorf("plugin %q does not export %s", r.manifest.ID, name)
	}
	if result, err := function.Call(ctx, uint64(handle)); err != nil {
		return nil, fmt.Errorf("plugin %q %s failed: %w", r.manifest.ID, name, err)
	} else if len(result) != 0 {
		return nil, fmt.Errorf("plugin %q has invalid %s ABI", r.manifest.ID, name)
	}
	output, ok := r.v2State().output(handle)
	if !ok {
		return nil, fmt.Errorf("plugin %q %s did not write an ABI v2 output", r.manifest.ID, name)
	}
	if !json.Valid(output) {
		return nil, fmt.Errorf("plugin %q %s returned malformed JSON", r.manifest.ID, name)
	}
	return output, nil
}
