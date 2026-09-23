package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	DefaultCallTimeout = 5 * time.Second
	exportInit         = "runpilot_init"
	exportCall         = "runpilot_call"
	exportShutdown     = "runpilot_shutdown"
)

// Host is the narrow data-oriented ABI surface exposed to a backend module.
// Implementations must enforce permissions before performing privileged work.
type Host interface {
	Log(context.Context, string) error
	ConfigGet(context.Context, string) (json.RawMessage, error)
	ConfigSet(context.Context, string, json.RawMessage) error
}

type Runtime struct {
	context  context.Context
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	module   api.Module
	manifest Manifest
	host     Host
}

func LoadRuntime(ctx context.Context, packageDir string, manifest Manifest, host Host) (*Runtime, error) {
	if manifest.Backend == nil {
		return nil, fmt.Errorf("plugin %q has no backend", manifest.ID)
	}
	modulePath := packageDir + "/" + manifest.Backend.Module
	wasm, err := os.ReadFile(modulePath)
	if err != nil {
		return nil, err
	}
	runtime := wazero.NewRuntime(ctx)
	compiled, err := runtime.CompileModule(ctx, wasm)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("compile plugin %q: %w", manifest.ID, err)
	}
	module, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(manifest.ID))
	if err != nil {
		_ = compiled.Close(ctx)
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("initialize plugin %q: %w", manifest.ID, err)
	}
	loaded := &Runtime{context: ctx, runtime: runtime, compiled: compiled, module: module, manifest: manifest, host: host}
	if _, err := loaded.call(ctx, exportInit, nil); err != nil {
		_ = loaded.Close(ctx)
		return nil, err
	}
	return loaded, nil
}

func (r *Runtime) Call(ctx context.Context, operation string, request any, response any) error {
	if strings.TrimSpace(operation) == "" {
		return fmt.Errorf("plugin operation is required")
	}
	payload, err := json.Marshal(map[string]any{"operation": operation, "request": request})
	if err != nil {
		return err
	}
	result, err := r.call(ctx, exportCall, payload)
	if err != nil {
		return err
	}
	if response != nil && len(result) != 0 {
		if err := json.Unmarshal(result, response); err != nil {
			return fmt.Errorf("plugin returned invalid JSON: %w", err)
		}
	}
	return nil
}

func (r *Runtime) call(parent context.Context, name string, payload []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, DefaultCallTimeout)
	defer cancel()
	function := r.module.ExportedFunction(name)
	if function == nil {
		return nil, fmt.Errorf("plugin %q does not export %s", r.manifest.ID, name)
	}
	// ABI v1 reserves the function boundary for scalar values. A module that
	// does not implement the expected signature is rejected rather than guessed.
	if len(function.Definition().ParamTypes()) != 0 || len(function.Definition().ResultTypes()) > 1 {
		return nil, fmt.Errorf("plugin %q has invalid %s ABI", r.manifest.ID, name)
	}
	if _, err := function.Call(ctx); err != nil {
		return nil, fmt.Errorf("plugin %q %s failed: %w", r.manifest.ID, name, err)
	}
	return nil, nil
}

func (r *Runtime) Close(ctx context.Context) error {
	var shutdownErr error
	if r.module != nil {
		if function := r.module.ExportedFunction(exportShutdown); function != nil {
			_, shutdownErr = function.Call(ctx)
		}
		_ = r.module.Close(ctx)
	}
	if r.compiled != nil {
		_ = r.compiled.Close(ctx)
	}
	if r.runtime != nil {
		_ = r.runtime.Close(ctx)
	}
	return shutdownErr
}
