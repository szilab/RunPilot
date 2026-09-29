package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
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
	runtime, err := LoadRuntime(context.Background(), dir, manifest, systemStatusHost{})
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

func TestRuntimeDispatchesABIV2Validation(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backend", "plugin.wasm"), runtimeFixture("null", true), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := runtimeManifest()
	manifest.Requires.RunPilotAPI = PluginABIVersion2
	if err := manifest.Validate(); err != nil {
		t.Fatalf("ABI v2 manifest rejected: %v", err)
	}
	if _, err := LoadRuntime(context.Background(), dir, manifest, testHost{}); err == nil || !strings.Contains(err.Error(), exportInit) {
		t.Fatalf("ABI v2 dispatch did not enforce v2 signature: %v", err)
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
	if manifest.Requires.RunPilotAPI != PluginABIVersion2 {
		t.Fatalf("System manifest ABI = %d, want %d", manifest.Requires.RunPilotAPI, PluginABIVersion2)
	}
	assertSystemABIV2Module(t, filepath.Join(dir, manifest.Backend.Module))
	runtime, err := LoadRuntime(context.Background(), dir, manifest, systemStatusHost{})
	if err != nil {
		t.Fatalf("load compiled System WASM: %v", err)
	}
	defer runtime.Close(context.Background())
	var result map[string]any
	initialMemory := runtime.module.Memory().Size()
	for i := 1; i <= 128; i++ {
		var request any = map[string]any{}
		if i == 1 {
			request = nil
		} else if i == 2 {
			request = map[string]any{"padding": strings.Repeat("y", 20<<10)}
		} else if i == 3 {
			request = map[string]any{"padding": strings.Repeat("z", 1<<20)}
		}
		if err := runtime.Call(context.Background(), "status.get", request, &result); err != nil {
			t.Fatalf("call compiled System WASM #%d (memory=%d): %v", i, runtime.module.Memory().Size(), err)
		}
		if result["pluginCallCount"] != float64(i) || result["hostname"] == "" {
			t.Fatalf("stateful status #%d = %#v", i, result)
		}
		runtime.v2.mu.Lock()
		responseCount, invocationCount := len(runtime.v2.responses), len(runtime.v2.invocations)
		runtime.v2.mu.Unlock()
		if responseCount != 0 || invocationCount != 0 {
			t.Fatalf("ABI v2 handles leaked after call #%d: responses=%d invocations=%d", i, responseCount, invocationCount)
		}
		if i == 3 && runtime.module.Memory().Size() <= initialMemory {
			t.Fatalf("1 MiB lifecycle payload did not exercise WASM memory growth: initial=%d current=%d", initialMemory, runtime.module.Memory().Size())
		}
	}
	if err := runtime.Event(context.Background(), "scheduler.fired", map[string]string{"id": "test"}); err != nil {
		t.Fatalf("deliver compiled System event: %v", err)
	}
	if err := runtime.Call(context.Background(), "missing.method", map[string]any{}, &result); err != nil {
		t.Fatalf("unknown System method call: %v", err)
	}
	if result["error"].(map[string]any)["code"] != "unknown_method" {
		t.Fatalf("unknown method response = %#v", result)
	}
	runtime.v2.mu.Lock()
	responseCount, invocationCount := len(runtime.v2.responses), len(runtime.v2.invocations)
	runtime.v2.mu.Unlock()
	if responseCount != 0 || invocationCount != 0 {
		t.Fatalf("ABI v2 handles leaked after method error: responses=%d invocations=%d", responseCount, invocationCount)
	}
	tooLarge := map[string]any{"padding": strings.Repeat("z", maxABIV2Payload+1)}
	if err := runtime.Call(context.Background(), "status.get", tooLarge, &result); err == nil || !strings.Contains(err.Error(), "payload limit") {
		t.Fatalf("oversized lifecycle input error = %v", err)
	}

	failingRuntime, err := LoadRuntime(context.Background(), dir, manifest, failingSystemStatusHost{})
	if err != nil {
		t.Fatalf("load System with failing capability host: %v", err)
	}
	defer failingRuntime.Close(context.Background())
	if err := failingRuntime.Call(context.Background(), "status.get", nil, &result); err != nil {
		t.Fatalf("structured capability error call: %v", err)
	}
	if result["error"].(map[string]any)["code"] != "failed" {
		t.Fatalf("structured capability error result = %#v", result)
	}
	failingRuntime.v2.mu.Lock()
	responseCount, invocationCount = len(failingRuntime.v2.responses), len(failingRuntime.v2.invocations)
	failingRuntime.v2.mu.Unlock()
	if responseCount != 0 || invocationCount != 0 {
		t.Fatalf("ABI v2 handles leaked after capability error: responses=%d invocations=%d", responseCount, invocationCount)
	}
}

func TestSystemRPPluginInstallsAndLoadsThroughNormalPath(t *testing.T) {
	sourceRoot := filepath.Join("..", "..", "plugins", "system")
	assets := make(map[string]string)
	for _, name := range []string{"plugin.yaml", "backend/plugin.wasm", "web/plugin.js", "web/plugin.css"} {
		data, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		assets[name] = string(data)
	}
	packagePath := writePackage(t, assets)
	installRoot := t.TempDir()
	manifest, err := InstallPackage(installRoot, packagePath, "")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Requires.RunPilotAPI != PluginABIVersion2 {
		t.Fatalf("installed System ABI = %d", manifest.Requires.RunPilotAPI)
	}
	installedDir := filepath.Join(installRoot, manifest.ID, manifest.Version)
	runtime, err := LoadRuntime(context.Background(), installedDir, manifest, systemStatusHost{})
	if err != nil {
		t.Fatalf("load installed System package: %v", err)
	}
	defer runtime.Close(context.Background())
	var result map[string]any
	if err := runtime.Call(context.Background(), "status.get", map[string]any{}, &result); err != nil {
		t.Fatalf("call installed System package: %v", err)
	}
	if result["hostname"] != "test-host" || result["pluginCallCount"] != float64(1) {
		t.Fatalf("installed System result = %#v", result)
	}
}

func TestBuiltSystemPackageArtifact(t *testing.T) {
	packagePath := os.Getenv("RUNPILOT_SYSTEM_PACKAGE")
	if packagePath == "" {
		t.Skip("RUNPILOT_SYSTEM_PACKAGE is not set")
	}
	installRoot := t.TempDir()
	manifest, err := InstallPackage(installRoot, packagePath, "")
	if err != nil {
		t.Fatalf("install built System package: %v", err)
	}
	if manifest.ID != "system" || manifest.Requires.RunPilotAPI != PluginABIVersion2 {
		t.Fatalf("built package manifest = %#v", manifest)
	}
	runtime, err := LoadRuntime(context.Background(), filepath.Join(installRoot, manifest.ID, manifest.Version), manifest, systemStatusHost{})
	if err != nil {
		t.Fatalf("load built System package: %v", err)
	}
	defer runtime.Close(context.Background())
	var result map[string]any
	if err := runtime.Call(context.Background(), "status.get", map[string]any{}, &result); err != nil {
		t.Fatalf("call built System package: %v", err)
	}
	if result["hostname"] != "test-host" {
		t.Fatalf("built System status = %#v", result)
	}
}

func assertSystemABIV2Module(t *testing.T, wasmPath string) {
	t.Helper()
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatal(err)
	}
	wasmRuntime := wazero.NewRuntime(context.Background())
	defer wasmRuntime.Close(context.Background())
	compiled, err := wasmRuntime.CompileModule(context.Background(), wasm)
	if err != nil {
		t.Fatal(err)
	}
	defer compiled.Close(context.Background())
	imports := map[string]api.FunctionDefinition{}
	importCount := 0
	for _, function := range compiled.ImportedFunctions() {
		module, name, imported := function.Import()
		if imported && module == hostModuleName {
			importCount++
			imports[name] = function
		}
	}
	wantImports := []string{"input_len", "input_read", "output_write", "host_call", "response_len", "response_read", "response_drop"}
	if len(imports) != len(wantImports) || importCount != len(wantImports) {
		t.Fatalf("System ABI v2 imports = %v", imports)
	}
	for _, name := range wantImports {
		if imports[name] == nil {
			t.Errorf("System WASM does not import runpilot.%s", name)
		}
	}
	if _, exists := compiled.ExportedFunctions()[exportAlloc]; exists {
		t.Fatal("System ABI v2 WASM exports the ABI-v1 allocator")
	}
	if _, exists := compiled.ExportedFunctions()[exportReset]; exists {
		t.Fatal("System ABI v2 WASM exports the ABI-v1 allocator reset")
	}
	for _, name := range []string{exportInit, exportCall, exportEvent, exportShutdown} {
		function := compiled.ExportedFunctions()[name]
		if function == nil || !sameValueTypes(function.ParamTypes(), []api.ValueType{api.ValueTypeI32}) || len(function.ResultTypes()) != 0 {
			t.Errorf("System export %s has wrong ABI signature: %#v", name, function)
		}
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

type systemStatusHost struct{ testHost }

func (systemStatusHost) SystemStatus(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"hostname":"test-host","os":"linux","architecture":"amd64","cpuPercent":12.5,"memoryTotalBytes":4096,"memoryFreeBytes":1024,"disks":[]}`), nil
}

type failingSystemStatusHost struct{ testHost }

func (failingSystemStatusHost) SystemStatus(context.Context) (json.RawMessage, error) {
	return nil, os.ErrPermission
}

type recordingHost struct {
	testHost
	message string
}

func (h *recordingHost) Log(_ context.Context, message string) error { h.message = message; return nil }

func runtimeManifest() Manifest {
	return Manifest{APIVersion: PluginAPIVersion, ID: "test.runtime", Name: "Runtime", Version: "1.0.0", Requires: Requires{Backend: ">=1.0.0 <2.0.0", Frontend: ">=1.0.0 <2.0.0", RunPilotAPI: PluginABIVersion}, Backend: &BackendManifest{Module: "backend/plugin.wasm"}}
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

type historyCapabilityHost struct {
	testHost
	owner, operation string
	params           string
	err              error
	validated        bool
	validateErr      error
}

func (h *historyCapabilityHost) History(_ context.Context, owner, operation string, params json.RawMessage) (json.RawMessage, error) {
	h.owner, h.operation, h.params = owner, operation, string(params)
	if h.err != nil {
		return nil, h.err
	}
	return json.RawMessage(`{"ok":true}`), nil
}
func (h *historyCapabilityHost) ScheduleValidate(context.Context, json.RawMessage) error {
	h.validated = true
	return h.validateErr
}

func TestHistoryAndSchedulerValidateCapabilities(t *testing.T) {
	host := &historyCapabilityHost{}
	runtime := &Runtime{host: host, manifest: Manifest{ID: "owner.one"}}
	call := func(capability, params string) capabilityResponse {
		return runtime.dispatchCapability(context.Background(), []byte(`{"apiVersion":1,"capability":"`+capability+`","params":`+params+`}`))
	}
	for _, operation := range []string{"begin", "append", "finish", "list", "get", "output"} {
		response := call("history."+operation, `{"id":"x"}`)
		if !response.OK || host.operation != operation || host.owner != "owner.one" || host.params != `{"id":"x"}` {
			t.Fatalf("history.%s = %#v host=%#v", operation, response, host)
		}
	}
	// The owner always comes from the loaded plugin, never from parameters.
	call("history.get", `{"owner":"someone.else","id":"x"}`)
	if host.owner != "owner.one" {
		t.Fatalf("owner = %q", host.owner)
	}
	host.err = &HostFailure{Code: "not_found", Message: "unknown execution"}
	if response := call("history.get", `{"id":"x"}`); response.Error == nil || response.Error.Code != "not_found" || response.Error.Message != "unknown execution" {
		t.Fatalf("structured host failure lost: %#v", response)
	}
	host.err = os.ErrPermission
	if response := call("history.get", `{"id":"x"}`); response.Error == nil || response.Error.Code != "failed" || !strings.Contains(response.Error.Message, "permission") {
		t.Fatalf("plain failure = %#v", response)
	}
	if response := call("scheduler.validate", `{"schedule":{"type":"interval","intervalSeconds":5}}`); !response.OK || !host.validated {
		t.Fatalf("validate = %#v", response)
	}
	host.validateErr = &HostFailure{Code: "invalid_argument", Message: "bad cron"}
	if response := call("scheduler.validate", `{}`); response.Error == nil || response.Error.Message != "bad cron" {
		t.Fatalf("validate failure = %#v", response)
	}
	// Hosts that do not publish the capabilities report them as unavailable.
	bare := &Runtime{host: testHost{}, manifest: Manifest{ID: "owner.one"}}
	for _, capability := range []string{"history.list", "scheduler.validate"} {
		response := bare.dispatchCapability(context.Background(), []byte(`{"apiVersion":1,"capability":"`+capability+`","params":{}}`))
		if response.Error == nil || response.Error.Code != "unavailable" {
			t.Fatalf("%s on bare host = %#v", capability, response)
		}
	}
}
