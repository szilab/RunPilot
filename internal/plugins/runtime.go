package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	DefaultCallTimeout   = 5 * time.Second
	exportInit           = "runpilot_init"
	exportCall           = "runpilot_call"
	exportShutdown       = "runpilot_shutdown"
	exportEvent          = "runpilot_event"
	exportAlloc          = "runpilot_alloc"
	exportReset          = "runpilot_reset"
	hostModuleName       = "runpilot"
	CapabilityAPIVersion = 1
)

// Host is the small data-only capability surface published in Phase 1. It is
// not a permission interface: every loaded plugin can use every published
// capability. Implementations still validate inputs and preserve invariants.
type Host interface {
	Log(context.Context, string) error
	ConfigGet(context.Context, string) (json.RawMessage, error)
	ConfigSet(context.Context, string, json.RawMessage) error
	SystemStatus(context.Context) (json.RawMessage, error)
}

// StorageHost is optional so embedders that only publish a subset of
// capabilities remain source compatible. Values are JSON and are isolated by
// the runtime-supplied plugin ID; plugins never receive a host filesystem path.
type StorageHost interface {
	StorageGet(context.Context, string, string) (json.RawMessage, error)
	StorageSet(context.Context, string, string, json.RawMessage) error
}
type SchedulerHost interface {
	ScheduleRegister(context.Context, string, json.RawMessage) (json.RawMessage, error)
	ScheduleRemove(context.Context, string, string) error
	ScheduleList(context.Context, string) (json.RawMessage, error)
}
type ProcessHost interface {
	ProcessStart(context.Context, string, json.RawMessage) (json.RawMessage, error)
	ProcessStatus(context.Context, string, string) (json.RawMessage, error)
	ProcessTerminate(context.Context, string, string) (json.RawMessage, error)
}
type ProcessSessionHost interface {
	ProcessSessionCreate(context.Context, string, json.RawMessage) (json.RawMessage, error)
	ProcessSessionWrite(context.Context, string, json.RawMessage) (json.RawMessage, error)
	ProcessSessionResize(context.Context, string, json.RawMessage) (json.RawMessage, error)
	ProcessSessionStatus(context.Context, string, json.RawMessage) (json.RawMessage, error)
	ProcessSessionTerminate(context.Context, string, json.RawMessage) (json.RawMessage, error)
}
type BrowserHost interface {
	BrowserCapability(context.Context, string, string, json.RawMessage) (json.RawMessage, error)
}

type NetworkStreamHost interface {
	NetworkStreamOpen(context.Context, string, json.RawMessage) (json.RawMessage, error)
	NetworkStreamRead(context.Context, string, json.RawMessage) (json.RawMessage, error)
	NetworkStreamWrite(context.Context, string, json.RawMessage) (json.RawMessage, error)
	NetworkStreamClose(context.Context, string, json.RawMessage) (json.RawMessage, error)
}

// SchedulerValidator optionally checks a schedule definition without
// registering it.
type SchedulerValidator interface {
	ScheduleValidate(context.Context, json.RawMessage) error
}
type EventHost interface {
	PublishEvent(context.Context, string, string, json.RawMessage) error
}

// HistoryHost exposes owner-scoped execution records and captured output.
// The runtime supplies the owner; params are the capability's JSON params and
// the host validates and bounds them.
type HistoryHost interface {
	History(ctx context.Context, owner, operation string, params json.RawMessage) (json.RawMessage, error)
}

// HostFailure lets a host implementation return a stable capability error code
// and a message that is safe to show to the plugin.
type HostFailure struct{ Code, Message string }

func (e *HostFailure) Error() string { return e.Message }

// detailedFailure reports validation-style host errors (bad interpreter,
// invalid schedule, unknown execution) so plugins can surface actionable
// messages instead of an opaque "failed".
func detailedFailure(operation string, err error) capabilityResponse {
	var host *HostFailure
	if errors.As(err, &host) {
		return capabilityFailure(host.Code, host.Message)
	}
	return capabilityFailure("failed", operation+" failed: "+err.Error())
}

// LifecycleHost releases owner-scoped asynchronous work when a runtime stops.
type LifecycleHost interface{ PluginStopped(string) }

// CapabilityError is stable data returned across the WASM boundary. It never
// exposes Go errors, pointers, handles, or implementation types.
type CapabilityError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type capabilityRequest struct {
	APIVersion int             `json:"apiVersion"`
	Capability string          `json:"capability"`
	Params     json.RawMessage `json:"params"`
}
type capabilityResponse struct {
	OK     bool             `json:"ok"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *CapabilityError `json:"error,omitempty"`
}
type lifecycleRequest struct {
	APIVersion int             `json:"apiVersion"`
	Operation  string          `json:"operation,omitempty"`
	Request    json.RawMessage `json:"request,omitempty"`
}

type Runtime struct {
	mu       sync.Mutex // wazero instances are entered serially by design.
	context  context.Context
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	module   api.Module
	manifest Manifest
	host     Host
	v2       *abiV2State
}

// LoadRuntime loads one backend and invokes runpilot_init. The module must
// export linear memory, runpilot_alloc(i32)->i32 and each lifecycle function as
// (i32 pointer, i32 length)->i64. The i64 packs output pointer in its high 32
// bits and output length in its low 32 bits.
func LoadRuntime(ctx context.Context, packageDir string, manifest Manifest, host Host) (*Runtime, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if err := manifest.CompatibilityError(runtime.GOOS); err != nil {
		return nil, err
	}
	if manifest.Backend == nil {
		return nil, fmt.Errorf("plugin %q has no backend", manifest.ID)
	}
	wasm, err := os.ReadFile(filepath.Join(packageDir, filepath.FromSlash(manifest.Backend.Module)))
	if err != nil {
		return nil, fmt.Errorf("read plugin %q backend: %w", manifest.ID, err)
	}
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
	loaded := &Runtime{context: ctx, runtime: runtime, manifest: manifest, host: host, v2: newABIV2State()}
	if err := loaded.instantiateHost(ctx); err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}
	compiled, err := runtime.CompileModule(ctx, wasm)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("compile plugin %q: %w", manifest.ID, err)
	}
	loaded.compiled = compiled
	module, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(manifest.ID))
	if err != nil {
		_ = loaded.Close(ctx)
		return nil, fmt.Errorf("initialize plugin %q: %w", manifest.ID, err)
	}
	loaded.module = module
	if err := loaded.validateABI(); err != nil {
		_ = loaded.Close(ctx)
		return nil, err
	}
	if _, err := loaded.invoke(ctx, exportInit, lifecycleRequest{APIVersion: loaded.manifest.Requires.RunPilotAPI}); err != nil {
		_ = loaded.Close(ctx)
		return nil, err
	}
	return loaded, nil
}

func (r *Runtime) Call(ctx context.Context, operation string, request any, response any) error {
	if strings.TrimSpace(operation) == "" {
		return fmt.Errorf("plugin operation is required")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal plugin request: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result, err := r.invoke(ctx, exportCall, lifecycleRequest{APIVersion: r.manifest.Requires.RunPilotAPI, Operation: operation, Request: payload})
	if err != nil {
		return err
	}
	if response != nil {
		if err := json.Unmarshal(result, response); err != nil {
			return fmt.Errorf("plugin %q returned invalid JSON: %w", r.manifest.ID, err)
		}
	}
	return nil
}

// Event delivers a generic host-originated event to a loaded plugin. Calls and
// events are serialized per runtime, so plugin authors need not make module
// state concurrently safe. The host never holds this lock while performing
// process I/O or scheduler work.
func (r *Runtime) Event(ctx context.Context, event string, data any) error {
	if strings.TrimSpace(event) == "" {
		return fmt.Errorf("plugin event is required")
	}
	if r.module.ExportedFunction(exportEvent) == nil {
		return fmt.Errorf("plugin %q does not support host events", r.manifest.ID)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal plugin event: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []byte
	result, err = r.invoke(ctx, exportEvent, lifecycleRequest{APIVersion: r.manifest.Requires.RunPilotAPI, Operation: event, Request: payload})
	if err != nil {
		return err
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(result, &output); err != nil || output == nil {
		return nil // ABI-v1 event handlers may return any valid JSON value.
	}
	errorValue, ok := output["error"]
	if !ok || string(errorValue) == "null" {
		return nil
	}
	var eventError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(errorValue, &eventError); err != nil {
		return fmt.Errorf("plugin %q returned an invalid event error: %w", r.manifest.ID, err)
	}
	return fmt.Errorf("plugin %q event %s failed: %s: %s", r.manifest.ID, event, eventError.Code, eventError.Message)
}

func (r *Runtime) invoke(parent context.Context, name string, value any) ([]byte, error) {
	if r.manifest.Requires.RunPilotAPI == PluginABIVersion2 {
		return r.invokeV2(parent, name, value)
	}
	return r.invokeV1(parent, name, value)
}

func (r *Runtime) invokeV1(parent context.Context, name string, value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, DefaultCallTimeout)
	defer cancel()
	if reset := r.module.ExportedFunction(exportReset); reset != nil {
		if _, err := reset.Call(ctx); err != nil {
			return nil, fmt.Errorf("plugin %q reset allocator: %w", r.manifest.ID, err)
		}
	}
	pointer, err := r.allocate(ctx, uint32(len(payload)))
	if err != nil {
		return nil, fmt.Errorf("plugin %q %s input: %w", r.manifest.ID, name, err)
	}
	if len(payload) != 0 && !r.module.Memory().Write(pointer, payload) {
		return nil, fmt.Errorf("plugin %q %s input exceeds linear memory", r.manifest.ID, name)
	}
	function := r.module.ExportedFunction(name)
	if function == nil {
		return nil, fmt.Errorf("plugin %q does not export %s", r.manifest.ID, name)
	}
	result, err := function.Call(ctx, uint64(pointer), uint64(len(payload)))
	if err != nil {
		return nil, fmt.Errorf("plugin %q %s failed: %w", r.manifest.ID, name, err)
	}
	if len(result) != 1 {
		return nil, fmt.Errorf("plugin %q has invalid %s ABI", r.manifest.ID, name)
	}
	outputPointer, outputLength := unpackBuffer(result[0])
	output, ok := r.module.Memory().Read(outputPointer, outputLength)
	if !ok {
		return nil, fmt.Errorf("plugin %q %s returned an invalid linear-memory buffer", r.manifest.ID, name)
	}
	output = append([]byte(nil), output...) // plugin owns mutable WASM memory after return.
	if !json.Valid(output) {
		return nil, fmt.Errorf("plugin %q %s returned malformed JSON", r.manifest.ID, name)
	}
	return output, nil
}

func (r *Runtime) validateABI() error {
	if r.manifest.Requires.RunPilotAPI == PluginABIVersion2 {
		return r.validateABIV2()
	}
	if r.module.Memory() == nil {
		return fmt.Errorf("plugin %q does not export linear memory", r.manifest.ID)
	}
	if err := r.requireFunction(exportAlloc, []api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}); err != nil {
		return err
	}
	for _, name := range []string{exportInit, exportCall, exportShutdown} {
		if err := r.requireFunction(name, []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}); err != nil {
			return err
		}
	}
	if event := r.module.ExportedFunction(exportEvent); event != nil {
		if err := r.requireFunction(exportEvent, []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runtime) requireFunction(name string, params, results []api.ValueType) error {
	function := r.module.ExportedFunction(name)
	if function == nil {
		return fmt.Errorf("plugin %q does not export %s", r.manifest.ID, name)
	}
	definition := function.Definition()
	if !sameValueTypes(definition.ParamTypes(), params) || !sameValueTypes(definition.ResultTypes(), results) {
		return fmt.Errorf("plugin %q has invalid %s ABI", r.manifest.ID, name)
	}
	return nil
}
func sameValueTypes(got, want []api.ValueType) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
func (r *Runtime) allocate(ctx context.Context, length uint32) (uint32, error) {
	result, err := r.module.ExportedFunction(exportAlloc).Call(ctx, uint64(length))
	if err != nil || len(result) != 1 {
		if err == nil {
			err = fmt.Errorf("invalid allocator result")
		}
		return 0, err
	}
	return uint32(result[0]), nil
}
func packBuffer(pointer, length uint32) uint64   { return uint64(pointer)<<32 | uint64(length) }
func unpackBuffer(value uint64) (uint32, uint32) { return uint32(value >> 32), uint32(value) }

func (r *Runtime) instantiateHost(ctx context.Context) error {
	builder := r.runtime.NewHostModuleBuilder(hostModuleName)
	if r.manifest.Requires.RunPilotAPI == PluginABIVersion {
		builder.NewFunctionBuilder().WithFunc(r.hostCall).Export("host_call")
	} else {
		builder.NewFunctionBuilder().WithFunc(r.v2InputLen).Export("input_len")
		builder.NewFunctionBuilder().WithFunc(r.v2InputRead).Export("input_read")
		builder.NewFunctionBuilder().WithFunc(r.v2OutputWrite).Export("output_write")
		builder.NewFunctionBuilder().WithFunc(r.v2ResponseLen).Export("response_len")
		builder.NewFunctionBuilder().WithFunc(r.v2ResponseRead).Export("response_read")
		builder.NewFunctionBuilder().WithFunc(r.v2ResponseDrop).Export("response_drop")
		builder.NewFunctionBuilder().WithFunc(r.v2HostCall).Export("host_call")
	}
	_, err := builder.Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("initialize RunPilot capability host: %w", err)
	}
	return nil
}

// hostCall is imported as runpilot.host_call(i32, i32)->i64. It exchanges JSON
// through the plugin's linear memory using the same allocator/result convention.
func (r *Runtime) hostCall(ctx context.Context, module api.Module, pointer, length uint32) uint64 {
	request, ok := module.Memory().Read(pointer, length)
	if !ok {
		return r.writeHostResponse(ctx, module, capabilityFailure("invalid_argument", "capability request exceeds linear memory"))
	}
	return r.writeHostResponse(ctx, module, r.dispatchCapability(ctx, request))
}
func (r *Runtime) writeHostResponse(ctx context.Context, module api.Module, response capabilityResponse) uint64 {
	payload, err := json.Marshal(response)
	if err != nil {
		return 0
	}
	allocator := module.ExportedFunction(exportAlloc)
	if allocator == nil {
		return 0
	}
	result, err := allocator.Call(ctx, uint64(len(payload)))
	if err != nil || len(result) != 1 {
		return 0
	}
	pointer := uint32(result[0])
	if len(payload) != 0 && !module.Memory().Write(pointer, payload) {
		return 0
	}
	return packBuffer(pointer, uint32(len(payload)))
}
func (r *Runtime) dispatchCapability(ctx context.Context, payload []byte) capabilityResponse {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request capabilityRequest
	if err := decoder.Decode(&request); err != nil || decoder.More() {
		return capabilityFailure("invalid_argument", "capability request must be a valid envelope")
	}
	if request.APIVersion != CapabilityAPIVersion {
		return capabilityFailure("unsupported_api", fmt.Sprintf("capability API version %d is not supported", request.APIVersion))
	}
	if r.host == nil {
		return capabilityFailure("unavailable", "RunPilot capability host is unavailable")
	}
	switch request.Capability {
	case "log.write":
		var args struct {
			Message string `json:"message"`
		}
		if err := decodeParams(request.Params, &args); err != nil || strings.TrimSpace(args.Message) == "" {
			return capabilityFailure("invalid_argument", "log.write requires a non-empty message")
		}
		if err := r.host.Log(ctx, args.Message); err != nil {
			return capabilityFailure("failed", "log.write failed")
		}
		return capabilitySuccess(nil)
	case "config.get":
		var args struct {
			Key string `json:"key"`
		}
		if err := decodeParams(request.Params, &args); err != nil || strings.TrimSpace(args.Key) == "" {
			return capabilityFailure("invalid_argument", "config.get requires a key")
		}
		value, err := r.host.ConfigGet(ctx, args.Key)
		if err != nil {
			return capabilityFailure("failed", "config.get failed")
		}
		return capabilitySuccess(value)
	case "config.set":
		var args struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		if err := decodeParams(request.Params, &args); err != nil || strings.TrimSpace(args.Key) == "" || !json.Valid(args.Value) {
			return capabilityFailure("invalid_argument", "config.set requires a key and JSON value")
		}
		if err := r.host.ConfigSet(ctx, args.Key, args.Value); err != nil {
			return capabilityFailure("failed", "config.set failed")
		}
		return capabilitySuccess(nil)
	case "system.status":
		if len(request.Params) != 0 && string(request.Params) != "{}" && string(request.Params) != "null" {
			return capabilityFailure("invalid_argument", "system.status does not accept parameters")
		}
		value, err := r.host.SystemStatus(ctx)
		if err != nil {
			return capabilityFailure("failed", "system.status failed")
		}
		if !json.Valid(value) {
			return capabilityFailure("failed", "system.status returned invalid JSON")
		}
		return capabilitySuccess(value)
	case "storage.get":
		var args struct {
			Key string `json:"key"`
		}
		if err := decodeParams(request.Params, &args); err != nil || !validStorageKey(args.Key) {
			return capabilityFailure("invalid_argument", "storage.get requires a valid key")
		}
		storage, ok := r.host.(StorageHost)
		if !ok {
			return capabilityFailure("unavailable", "plugin storage is unavailable")
		}
		value, err := storage.StorageGet(ctx, r.manifest.ID, args.Key)
		if err != nil {
			return capabilityFailure("failed", "storage.get failed")
		}
		return capabilitySuccess(value)
	case "storage.set":
		var args struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		if err := decodeParams(request.Params, &args); err != nil || !validStorageKey(args.Key) || !json.Valid(args.Value) {
			return capabilityFailure("invalid_argument", "storage.set requires a valid key and JSON value")
		}
		storage, ok := r.host.(StorageHost)
		if !ok {
			return capabilityFailure("unavailable", "plugin storage is unavailable")
		}
		if err := storage.StorageSet(ctx, r.manifest.ID, args.Key, args.Value); err != nil {
			return capabilityFailure("failed", "storage.set failed")
		}
		return capabilitySuccess(nil)
	case "scheduler.register":
		scheduler, ok := r.host.(SchedulerHost)
		if !ok {
			return capabilityFailure("unavailable", "scheduler is unavailable")
		}
		value, err := scheduler.ScheduleRegister(ctx, r.manifest.ID, request.Params)
		if err != nil {
			return detailedFailure("scheduler.register", err)
		}
		return capabilitySuccess(value)
	case "scheduler.validate":
		validator, ok := r.host.(SchedulerValidator)
		if !ok {
			return capabilityFailure("unavailable", "scheduler is unavailable")
		}
		if err := validator.ScheduleValidate(ctx, request.Params); err != nil {
			return detailedFailure("scheduler.validate", err)
		}
		return capabilitySuccess(nil)
	case "scheduler.remove":
		var args struct {
			ID string `json:"id"`
		}
		if err := decodeParams(request.Params, &args); err != nil || strings.TrimSpace(args.ID) == "" {
			return capabilityFailure("invalid_argument", "scheduler.remove requires id")
		}
		scheduler, ok := r.host.(SchedulerHost)
		if !ok {
			return capabilityFailure("unavailable", "scheduler is unavailable")
		}
		if err := scheduler.ScheduleRemove(ctx, r.manifest.ID, args.ID); err != nil {
			return detailedFailure("scheduler.remove", err)
		}
		return capabilitySuccess(nil)
	case "scheduler.list":
		scheduler, ok := r.host.(SchedulerHost)
		if !ok {
			return capabilityFailure("unavailable", "scheduler is unavailable")
		}
		value, err := scheduler.ScheduleList(ctx, r.manifest.ID)
		if err != nil {
			return capabilityFailure("failed", "scheduler.list failed")
		}
		return capabilitySuccess(value)
	case "process.start":
		process, ok := r.host.(ProcessHost)
		if !ok {
			return capabilityFailure("unavailable", "process manager is unavailable")
		}
		value, err := process.ProcessStart(ctx, r.manifest.ID, request.Params)
		if err != nil {
			return detailedFailure("process.start", err)
		}
		return capabilitySuccess(value)
	case "process.session.create", "process.session.write", "process.session.resize", "process.session.status", "process.session.terminate":
		sessions, ok := r.host.(ProcessSessionHost)
		if !ok {
			return capabilityFailure("unavailable", "interactive process sessions are unavailable")
		}
		var value json.RawMessage
		var err error
		switch request.Capability {
		case "process.session.create":
			value, err = sessions.ProcessSessionCreate(ctx, r.manifest.ID, request.Params)
		case "process.session.write":
			value, err = sessions.ProcessSessionWrite(ctx, r.manifest.ID, request.Params)
		case "process.session.resize":
			value, err = sessions.ProcessSessionResize(ctx, r.manifest.ID, request.Params)
		case "process.session.status":
			value, err = sessions.ProcessSessionStatus(ctx, r.manifest.ID, request.Params)
		case "process.session.terminate":
			value, err = sessions.ProcessSessionTerminate(ctx, r.manifest.ID, request.Params)
		}
		if err != nil {
			return detailedFailure(request.Capability, err)
		}
		return capabilitySuccess(value)
	case "browser.publication.register", "browser.publication.remove", "browser.stream.ticket", "http.gateway.open", "http.gateway.close":
		browser, ok := r.host.(BrowserHost)
		if !ok {
			return capabilityFailure("unavailable", "browser capabilities unavailable")
		}
		value, err := browser.BrowserCapability(ctx, r.manifest.ID, request.Capability, request.Params)
		if err != nil {
			return detailedFailure(request.Capability, err)
		}
		return capabilitySuccess(value)
	case "network.stream.open", "network.stream.read", "network.stream.write", "network.stream.close":
		streams, ok := r.host.(NetworkStreamHost)
		if !ok {
			return capabilityFailure("unavailable", "network streams are unavailable")
		}
		var value json.RawMessage
		var err error
		switch request.Capability {
		case "network.stream.open":
			value, err = streams.NetworkStreamOpen(ctx, r.manifest.ID, request.Params)
		case "network.stream.read":
			value, err = streams.NetworkStreamRead(ctx, r.manifest.ID, request.Params)
		case "network.stream.write":
			value, err = streams.NetworkStreamWrite(ctx, r.manifest.ID, request.Params)
		case "network.stream.close":
			value, err = streams.NetworkStreamClose(ctx, r.manifest.ID, request.Params)
		}
		if err != nil {
			return detailedFailure(request.Capability, err)
		}
		return capabilitySuccess(value)
	case "history.begin", "history.append", "history.finish", "history.list", "history.get", "history.output":
		history, ok := r.host.(HistoryHost)
		if !ok {
			return capabilityFailure("unavailable", "execution history is unavailable")
		}
		value, err := history.History(ctx, r.manifest.ID, strings.TrimPrefix(request.Capability, "history."), request.Params)
		if err != nil {
			return detailedFailure(request.Capability, err)
		}
		return capabilitySuccess(value)
	case "process.status", "process.terminate":
		var args struct {
			ID string `json:"id"`
		}
		if err := decodeParams(request.Params, &args); err != nil || strings.TrimSpace(args.ID) == "" {
			return capabilityFailure("invalid_argument", request.Capability+" requires id")
		}
		process, ok := r.host.(ProcessHost)
		if !ok {
			return capabilityFailure("unavailable", "process manager is unavailable")
		}
		var value json.RawMessage
		var err error
		if request.Capability == "process.status" {
			value, err = process.ProcessStatus(ctx, r.manifest.ID, args.ID)
		} else {
			value, err = process.ProcessTerminate(ctx, r.manifest.ID, args.ID)
		}
		if err != nil {
			return detailedFailure(request.Capability, err)
		}
		return capabilitySuccess(value)
	case "events.publish":
		var args struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if err := decodeParams(request.Params, &args); err != nil || strings.TrimSpace(args.Event) == "" || !json.Valid(args.Data) {
			return capabilityFailure("invalid_argument", "events.publish requires event and JSON data")
		}
		events, ok := r.host.(EventHost)
		if !ok {
			return capabilityFailure("unavailable", "event publication is unavailable")
		}
		if err := events.PublishEvent(ctx, r.manifest.ID, args.Event, args.Data); err != nil {
			return detailedFailure("events.publish", err)
		}
		return capabilitySuccess(nil)
	default:
		return capabilityFailure("unknown_capability", "capability is not published by this RunPilot version")
	}
}

func validStorageKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func decodeParams(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}
func capabilitySuccess(value json.RawMessage) capabilityResponse {
	if value == nil {
		value = json.RawMessage("null")
	}
	return capabilityResponse{OK: true, Result: value}
}
func capabilityFailure(code, message string) capabilityResponse {
	return capabilityResponse{OK: false, Error: &CapabilityError{Code: code, Message: message}}
}

func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var shutdownErr error
	if r.module != nil {
		_, shutdownErr = r.invoke(ctx, exportShutdown, lifecycleRequest{APIVersion: r.manifest.Requires.RunPilotAPI})
		_ = r.module.Close(ctx)
	}
	if r.compiled != nil {
		_ = r.compiled.Close(ctx)
	}
	if r.runtime != nil {
		_ = r.runtime.Close(ctx)
	}
	if host, ok := r.host.(LifecycleHost); ok {
		host.PluginStopped(r.manifest.ID)
	}
	return shutdownErr
}
