package plugins

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/tetratelabs/wazero"
)

func TestABIV2InvocationAndResponseOwnership(t *testing.T) {
	s := newABIV2State()
	a, ok := s.create([]byte("input"))
	if !ok {
		t.Fatal("invocation creation failed")
	}
	b, ok := s.create(nil)
	if !ok || a == b {
		t.Fatal("invocation handles are invalid or reused")
	}
	if input, ok := s.input(a); !ok || string(input) != "input" {
		t.Fatalf("input = %q, %v", input, ok)
	}
	if !s.writeOutput(a, []byte("output")) || s.writeOutput(a, []byte("again")) {
		t.Fatal("output write semantics are incorrect")
	}
	response, ok := s.response(a, []byte("response"))
	if !ok {
		t.Fatal("response creation failed")
	}
	if _, ok := s.readResponse(b, response); ok {
		t.Fatal("response crossed invocation boundary")
	}
	if got, ok := s.readResponse(a, response); !ok || string(got) != "response" {
		t.Fatalf("response = %q, %v", got, ok)
	}
	s.remove(a)
	if _, ok := s.input(a); ok {
		t.Fatal("stale invocation was accepted")
	}
	if _, ok := s.readResponse(a, response); ok {
		t.Fatal("response survived invocation cleanup")
	}
}

func TestABIV2RegistryRejectsInvalidStaleAndExhaustedHandles(t *testing.T) {
	s := newABIV2State()
	if _, ok := s.input(0); ok || s.writeOutput(0, nil) || s.removeResultForTest(0) {
		t.Fatal("zero handle was accepted")
	}
	h, ok := s.create([]byte("input"))
	if !ok {
		t.Fatal("create failed")
	}
	s.remove(h)
	if _, ok := s.input(h); ok || s.writeOutput(h, []byte("x")) {
		t.Fatal("stale invocation handle was accepted")
	}
	s.mu.Lock()
	s.next = uint64(^uint32(0)) + 1
	s.mu.Unlock()
	if _, ok := s.create(nil); ok {
		t.Fatal("exhausted handle space was reused")
	}
}

// removeResultForTest checks removal of a nonexistent handle via observable
// registry state without adding a second removal result to the production API.
func (s *abiV2State) removeResultForTest(handle uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.invocations[handle]
	return ok
}

func TestABIV2RegistryConcurrentCreateAndCleanup(t *testing.T) {
	s := newABIV2State()
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, ok := s.create([]byte("x"))
			if ok {
				s.writeOutput(h, []byte("{}"))
				s.remove(h)
			}
		}()
	}
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.invocations) != 0 || len(s.responses) != 0 {
		t.Fatalf("registry leaked: %#v %#v", s.invocations, s.responses)
	}
}

func TestABIV2InvocationIsRemovedAfterLifecycleTrap(t *testing.T) {
	ctx := context.Background()
	wasmRuntime := wazero.NewRuntime(ctx)
	defer wasmRuntime.Close(ctx)
	module, err := wasmRuntime.Instantiate(ctx, abiV2TrapFixture())
	if err != nil {
		t.Fatal(err)
	}
	defer module.Close(ctx)
	state := newABIV2State()
	r := &Runtime{module: module, manifest: Manifest{ID: "test.trap"}, v2: state}
	if _, err := r.invokeV2(ctx, exportCall, lifecycleRequest{APIVersion: PluginABIVersion2}); err == nil {
		t.Fatal("trapping lifecycle call succeeded")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.invocations) != 0 || len(state.responses) != 0 {
		t.Fatalf("trapping lifecycle call leaked registry entries: %#v %#v", state.invocations, state.responses)
	}
}

func abiV2TrapFixture() []byte {
	wasm := []byte{'\x00', 'a', 's', 'm', '\x01', '\x00', '\x00', '\x00'}
	section := func(id byte, data []byte) {
		wasm = append(wasm, id, byte(len(data)))
		wasm = append(wasm, data...)
	}
	section(1, []byte{1, 0x60, 1, 0x7f, 0}) // (i32) -> ()
	section(3, []byte{1, 0})                // one function
	section(5, []byte{1, 0, 1})             // one-page memory
	exports := []byte{2, 6, 'm', 'e', 'm', 'o', 'r', 'y', 2, 0, 13}
	exports = append(exports, "runpilot_call"...)
	exports = append(exports, 0, 0)
	section(7, exports)
	section(10, []byte{1, 3, 0, 0, 0x0b}) // unreachable lifecycle body
	return wasm
}

func TestABIV2MemoryCopiesAndGrowth(t *testing.T) {
	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)
	defer runtime.Close(ctx)
	module, err := runtime.Instantiate(ctx, runtimeFixture("null", true))
	if err != nil {
		t.Fatal(err)
	}
	memory := module.Memory()
	if !copyToWASM(memory, 32, []byte("hello")) {
		t.Fatal("write failed")
	}
	large := make([]byte, 8<<10)
	for i := range large {
		large[i] = byte(i)
	}
	if !copyToWASM(memory, 128, large) {
		t.Fatal("multi-KiB write failed")
	}
	largeCopy, ok := copyFromWASM(memory, 128, uint32(len(large)))
	if !ok || string(largeCopy) != string(large) {
		t.Fatal("multi-KiB copy differs")
	}
	got, ok := copyFromWASM(memory, 32, 5)
	if !ok || string(got) != "hello" {
		t.Fatalf("copy = %q, %v", got, ok)
	}
	got[0] = 'H'
	verify, _ := copyFromWASM(memory, 32, 5)
	if string(verify) != "hello" {
		t.Fatal("copy retained WASM backing memory")
	}
	if _, ok := copyFromWASM(memory, ^uint32(0), 2); ok || copyToWASM(memory, ^uint32(0), []byte("x")) {
		t.Fatal("overflow pointer accepted")
	}
	if _, ok := copyFromWASM(memory, memory.Size(), 1); ok {
		t.Fatal("out of range read accepted")
	}
	if _, ok := copyFromWASM(memory, ^uint32(0)-2, 8); ok {
		t.Fatal("overflowing pointer/length accepted")
	}
	if b, ok := copyFromWASM(memory, 0, 0); !ok || len(b) != 0 {
		t.Fatal("zero length read failed")
	}
	if _, ok := memory.Grow(1); !ok {
		t.Fatal("memory growth failed")
	}
	if !copyToWASM(memory, 65536, []byte("grown")) {
		t.Fatal("write after growth failed")
	}
	if got, ok := copyFromWASM(memory, 65536, 5); !ok || string(got) != "grown" {
		t.Fatal("read after growth failed")
	}
}

func TestABIV2ImportsCopyAndScopeResponses(t *testing.T) {
	ctx := context.Background()
	wazeroRuntime := wazero.NewRuntime(ctx)
	defer wazeroRuntime.Close(ctx)
	module, err := wazeroRuntime.Instantiate(ctx, runtimeFixture("null", true))
	if err != nil {
		t.Fatal(err)
	}
	state := newABIV2State()
	invocation, ok := state.create([]byte(`{"apiVersion":2}`))
	if !ok {
		t.Fatal("invocation create failed")
	}
	r := &Runtime{host: &recordingHost{}, v2: state}
	mem := module.Memory()
	if got := r.v2InputLen(invocation); got != uint32(len(`{"apiVersion":2}`)) {
		t.Fatalf("input_len = %d", got)
	}
	if got := r.v2InputRead(ctx, module, invocation, 32, 64); got != uint32(len(`{"apiVersion":2}`)) {
		t.Fatalf("input_read = %d", got)
	}
	input, _ := copyFromWASM(mem, 32, uint32(len(`{"apiVersion":2}`)))
	if string(input) != `{"apiVersion":2}` {
		t.Fatalf("copied input = %q", input)
	}
	if got := r.v2InputRead(ctx, module, invocation, 32, 1); got != 0 {
		t.Fatalf("undersized input_read = %d", got)
	}
	if got := r.v2OutputWrite(ctx, module, invocation, ^uint32(0), 2); got != 0 {
		t.Fatalf("invalid output pointer result = %d", got)
	}
	if !copyToWASM(mem, 48, []byte(`null`)) || r.v2OutputWrite(ctx, module, invocation, 48, 4) != 1 {
		t.Fatal("output_write rejected a valid output")
	}
	if r.v2OutputWrite(ctx, module, invocation, 48, 4) != 0 {
		t.Fatal("duplicate output_write was accepted")
	}
	if r.v2OutputWrite(ctx, module, invocation, 48, maxABIV2Payload+1) != 0 {
		t.Fatal("oversized output_write was accepted")
	}
	if !copyToWASM(mem, 64, []byte(`{"apiVersion":1,"capability":"log.write","params":{"message":"one"}}`)) {
		t.Fatal("write capability request")
	}
	requestLen := uint32(len(`{"apiVersion":1,"capability":"log.write","params":{"message":"one"}}`))
	r1 := r.v2HostCall(ctx, module, invocation, 64, requestLen)
	if r1 == 0 || r.v2ResponseLen(invocation, r1) == 0 {
		t.Fatal("host_call did not create response")
	}
	if got := r.v2ResponseRead(ctx, module, invocation, r1, ^uint32(0), 64); got != 0 {
		t.Fatalf("invalid response destination result = %d", got)
	}
	if got := r.v2ResponseRead(ctx, module, invocation, r1, 256, 2048); got == 0 {
		t.Fatal("response_read failed")
	}
	responseBytes, _ := copyFromWASM(mem, 256, r.v2ResponseLen(invocation, r1))
	var response capabilityResponse
	if err := json.Unmarshal(responseBytes, &response); err != nil || !response.OK {
		t.Fatalf("host-call response = %s, err=%v", responseBytes, err)
	}
	if !copyToWASM(mem, 64, []byte(`{"apiVersion":1,"capability":"system.status","params":{}}`)) {
		t.Fatal("write second capability request")
	}
	requestLen = uint32(len(`{"apiVersion":1,"capability":"system.status","params":{}}`))
	r2 := r.v2HostCall(ctx, module, invocation, 64, requestLen)
	if r2 == 0 || r2 == r1 {
		t.Fatal("second host_call returned invalid handle")
	}
	if !r.dropInvocationAndResponsesForTest(invocation) {
		t.Fatal("invocation cleanup failed")
	}
	if r.v2ResponseLen(invocation, r1) != 0 || r.v2ResponseLen(invocation, r2) != 0 {
		t.Fatal("response handle survived invocation cleanup")
	}
}

func (r *Runtime) dropInvocationAndResponsesForTest(handle uint32) bool {
	_, existed := r.v2.input(handle)
	r.v2.remove(handle)
	return existed
}
