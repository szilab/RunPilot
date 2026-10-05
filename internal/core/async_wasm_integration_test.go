package core

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

// TestAsyncFixtureRealWASM exercises the complete generic path with a TinyGo
// module, actual process execution, scheduler timing, runtime callbacks, and
// the shared browser-event fanout. Set TINYGO to run it outside CI images.
func TestAsyncFixtureRealWASM(t *testing.T) {
	tinygo := os.Getenv("TINYGO")
	if tinygo == "" {
		var err error
		tinygo, err = exec.LookPath("tinygo")
		if err != nil {
			t.Skip("TinyGo is not installed")
		}
	}
	root := filepath.Join("..", "..")
	wasm := filepath.Join(t.TempDir(), "plugin.wasm")
	build := exec.Command(tinygo, "build", "-target=wasm-unknown", "-tags=runpilot_wasm", "-scheduler=none", "-gc=conservative", "-no-debug", "-o", wasm, "./plugins/test-fixtures/async/backend")
	build.Dir = root
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile fixture: %v: %s", err, output)
	}
	data, err := os.ReadFile(wasm)
	if err != nil {
		t.Fatal(err)
	}
	pkg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pkg, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "backend", "plugin.wasm"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	manifest := plugins.Manifest{APIVersion: plugins.PluginAPIVersion, ID: "async.fixture", Name: "Async fixture", Version: "1.0.0", Requires: plugins.Requires{Backend: ">=1.0.0 <2.0.0", Frontend: ">=1.0.0 <2.0.0", RunPilotAPI: plugins.PluginABIVersion}, Backend: &plugins.BackendManifest{Module: "backend/plugin.wasm"}}
	runtime, err := plugins.LoadRuntime(context.Background(), pkg, manifest, controllerPluginHost{controller: c})
	if err != nil {
		t.Fatal(err)
	}
	c.runtimeMu.Lock()
	c.runtimes[manifest.ID] = runtime
	c.runtimeMu.Unlock()
	defer runtime.Close(context.Background())
	events, unsubscribe := c.SubscribePluginEvents()
	defer unsubscribe()
	call := func(method string, request any) map[string]any {
		t.Helper()
		var got map[string]any
		if err := runtime.Call(context.Background(), method, request, &got); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return got
	}
	if got := call("state.get", map[string]any{}); got["calls"] != float64(1) {
		t.Fatalf("state 1: %#v", got)
	}
	if got := call("state.get", map[string]any{}); got["calls"] != float64(2) {
		t.Fatalf("state 2: %#v", got)
	}
	call("storage.set", map[string]any{"saved": true})
	if got := call("storage.get", map[string]any{}); got["value"].(map[string]any)["saved"] != true {
		t.Fatalf("storage: %#v", got)
	}
	call("publish", map[string]any{})
	waitFixtureEvent(t, events, "fixture.published")
	call("schedule.register", map[string]any{})
	waitFixtureCallback(t, events, "scheduler.fired")
	call("schedule.remove", map[string]any{})
	process := call("process.start", map[string]any{"command": os.Args[0], "args": []string{"-test.run=TestAsyncWASMHelper", "--"}, "environment": map[string]string{"RUNPILOT_ASYNC_HELPER": "output"}})
	if process["error"] != "" {
		t.Fatalf("start: %#v", process)
	}
	waitFixtureCallbacks(t, events, "process.stdout", "process.stderr", "process.exit")
	long := call("process.start", map[string]any{"command": os.Args[0], "args": []string{"-test.run=TestAsyncWASMHelper", "--"}, "environment": map[string]string{"RUNPILOT_ASYNC_HELPER": "wait"}})
	id := long["process"].(map[string]any)["id"].(string)
	if got := call("process.terminate", map[string]any{"id": id}); got["error"] != "" {
		t.Fatalf("terminate: %#v", got)
	}
	if got := call("process.terminate", map[string]any{"id": id}); got["error"] != "" {
		t.Fatalf("repeat terminate: %#v", got)
	}
	waitFixtureCallback(t, events, "process.exit")
}
func waitFixtureEvent(t *testing.T, events <-chan plugins.Event, wanted string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Event == wanted {
				return
			}
		case <-deadline:
			t.Fatalf("did not receive %s", wanted)
		}
	}
}
func waitFixtureCallback(t *testing.T, events <-chan plugins.Event, callback string) {
	waitFixtureCallbacks(t, events, callback)
}
func waitFixtureCallbacks(t *testing.T, events <-chan plugins.Event, callbacks ...string) {
	t.Helper()
	wanted := map[string]bool{}
	for _, callback := range callbacks {
		wanted[callback] = true
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Event != "fixture.event" {
				continue
			}
			var data struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(event.Data, &data) == nil && wanted[data.Event] {
				delete(wanted, data.Event)
				if len(wanted) == 0 {
					return
				}
			}
		case <-deadline:
			t.Fatalf("did not receive callbacks %#v", wanted)
		}
	}
}
func TestAsyncWASMHelper(t *testing.T) {
	if os.Getenv("RUNPILOT_ASYNC_HELPER") == "output" {
		_, _ = os.Stdout.WriteString("fixture stdout\n")
		_, _ = os.Stderr.WriteString("fixture stderr\n")
	}
	if os.Getenv("RUNPILOT_ASYNC_HELPER") == "wait" {
		select {}
	}
}
