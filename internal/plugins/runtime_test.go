package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeLifecycleAndABIValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backend", "plugin.wasm"), runtimeFixture("null", true), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := runtimeManifest()
	runtime, err := LoadRuntime(context.Background(), dir, manifest, testHost{})
	if err != nil {
		t.Fatalf("LoadRuntime() error = %v", err)
	}
	if err := runtime.Call(context.Background(), "status.get", map[string]any{}, nil); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "backend", "plugin.wasm"), runtimeFixture("null", false), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntime(context.Background(), dir, manifest, testHost{}); err == nil || !strings.Contains(err.Error(), exportShutdown) {
		t.Fatalf("missing lifecycle export error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backend", "plugin.wasm"), runtimeFixture("oops", true), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntime(context.Background(), dir, manifest, testHost{}); err == nil || !strings.Contains(err.Error(), "malformed JSON") {
		t.Fatalf("malformed response error = %v", err)
	}
}

func TestCapabilityBridgeReturnsStructuredResults(t *testing.T) {
	host := &recordingHost{}
	runtime := &Runtime{host: host}
	response := runtime.dispatchCapability(context.Background(), []byte(`{"apiVersion":1,"capability":"log.write","params":{"message":"hello"}}`))
	if !response.OK || host.message != "hello" {
		t.Fatalf("response = %#v, host = %#v", response, host)
	}
	response = runtime.dispatchCapability(context.Background(), []byte(`{"apiVersion":1,"capability":"nope","params":{}}`))
	if response.Error == nil || response.Error.Code != "unknown_capability" {
		t.Fatalf("response = %#v", response)
	}
	response = runtime.dispatchCapability(context.Background(), []byte(`{"apiVersion":1,"capability":"config.set","params":{"key":"x"}}`))
	if response.Error == nil || response.Error.Code != "invalid_argument" {
		t.Fatalf("response = %#v", response)
	}
}

// TestSystemSourceWASM proves the checked-in System module is produced by a
// real compiler and uses the normal ABI/capability bridge, not a core bypass.
func TestSystemSourceWASM(t *testing.T) {
	dir := filepath.Join("..", "..", "plugins", "system")
	manifest, err := loadManifest(filepath.Join(dir, "plugin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := LoadRuntime(context.Background(), dir, manifest, testHost{})
	if err != nil {
		t.Fatalf("load compiled System WASM: %v", err)
	}
	defer runtime.Close(context.Background())
	var result map[string]any
	if err := runtime.Call(context.Background(), "status.get", map[string]any{}, &result); err != nil {
		t.Fatalf("call compiled System WASM: %v", err)
	}
	if result["pluginCallCount"] != float64(1) {
		t.Fatalf("first stateful call = %#v", result)
	}
	if err := runtime.Event(context.Background(), "scheduler.fired", map[string]string{"id": "test"}); err != nil {
		t.Fatalf("deliver compiled System event: %v", err)
	}
	if err := runtime.Call(context.Background(), "status.get", map[string]any{}, &result); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if result["pluginCallCount"] != float64(2) {
		t.Fatalf("state was not retained: %#v", result)
	}
}

type testHost struct{}

func (testHost) Log(context.Context, string) error { return nil }
func (testHost) ConfigGet(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`null`), nil
}
func (testHost) ConfigSet(context.Context, string, json.RawMessage) error { return nil }
func (testHost) SystemStatus(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

type recordingHost struct {
	testHost
	message string
}

func (h *recordingHost) Log(_ context.Context, message string) error { h.message = message; return nil }

func runtimeManifest() Manifest {
	return Manifest{APIVersion: PluginAPIVersion, ID: "test.runtime", Name: "Runtime", Version: "1", Requires: Requires{RunPilotAPI: PluginABIVersion}, Backend: &BackendManifest{Module: "backend/plugin.wasm"}}
}

// runtimeFixture is a tiny standards-compliant WASM module assembled here so
// runtime tests do not depend on a compiler being installed on the test host.
func runtimeFixture(response string, shutdown bool) []byte {
	wasm := []byte{'\x00', 'a', 's', 'm', '\x01', '\x00', '\x00', '\x00'}
	section := func(id byte, data []byte) { wasm = append(wasm, id, byte(len(data))); wasm = append(wasm, data...) }
	section(1, []byte{2, 0x60, 1, 0x7f, 1, 0x7f, 0x60, 2, 0x7f, 0x7f, 1, 0x7e})
	section(3, []byte{4, 0, 1, 1, 1})
	section(5, []byte{1, 0, 1})
	exports := []byte{4}
	export := func(name string, kind, index byte) {
		exports = append(exports, byte(len(name)))
		exports = append(exports, name...)
		exports = append(exports, kind, index)
	}
	export("memory", 2, 0)
	export(exportAlloc, 0, 0)
	export(exportInit, 0, 1)
	export(exportCall, 0, 2)
	if shutdown {
		exports[0]++
		export(exportShutdown, 0, 3)
	}
	section(7, exports)
	allocator := []byte{0, 0x41, 0x80, 0x08, 0x0b}          // always allocates at 1024
	lifecycle := []byte{0, 0x42, byte(len(response)), 0x0b} // pointer zero, length response
	code := []byte{4, byte(len(allocator))}
	code = append(code, allocator...)
	for range 3 {
		code = append(code, byte(len(lifecycle)))
		code = append(code, lifecycle...)
	}
	section(10, code)
	data := append([]byte{1, 0, 0x41, 0, 0x0b, byte(len(response))}, response...)
	section(11, data)
	return wasm
}
