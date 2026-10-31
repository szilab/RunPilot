package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

func TestPluginNetworkStreamOpensWorkspaceSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workspace socket test uses Unix domain sockets")
	}
	// A deep data directory exceeds the sun_path limit, covering the
	// /proc/self/fd fallback used for long workspace paths on Linux.
	dataDir := filepath.Join(t.TempDir(), strings.Repeat("d", 60), strings.Repeat("e", 40))
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &Controller{dataDir: dataDir}
	if _, err := wsCall(t, c, "owner", "mkdir", "sessions/one", nil); err != nil {
		t.Fatal(err)
	}
	socketPath, err := c.workspaceHostPath("owner", "sessions/one/server.sock")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" && len(socketPath) >= maxUnixSocketPath {
		t.Skip("long Unix socket paths are only supported on Linux")
	}
	listener, err := listenUnixLong(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := listener.Accept(); err == nil {
			accepted <- conn
		}
	}()
	manager := newPluginNetworkStreamManager(nil)
	manager.workspaceSocket = c.workspaceSocketHostPath
	defer manager.close()
	opened, err := manager.open(context.Background(), "owner", mustJSON(t, map[string]any{"workspaceSocket": "sessions/one/server.sock", "connectTimeoutSeconds": 2}))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(opened, &result); err != nil || result.ID == "" {
		t.Fatalf("open result %s, %v", opened, err)
	}
	peer := <-accepted
	defer peer.Close()
	if _, err := peer.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	read, err := manager.read(context.Background(), "owner", mustJSON(t, map[string]any{"id": result.ID, "maxBytes": 16, "timeoutMilliseconds": 1000}))
	if err != nil || !strings.Contains(string(read), "aGVsbG8") {
		t.Fatalf("read %s, %v", read, err)
	}

	// Another owner resolves the same relative path inside its own workspace.
	if _, err := manager.open(context.Background(), "other", mustJSON(t, map[string]any{"workspaceSocket": "sessions/one/server.sock", "connectTimeoutSeconds": 2})); err == nil {
		t.Fatal("another owner reached the workspace socket")
	}
}

func TestPluginNetworkStreamWorkspaceSocketValidation(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	if _, err := wsCall(t, c, "owner", "write", "plain", map[string]any{"data": "eA=="}); err != nil {
		t.Fatal(err)
	}
	manager := newPluginNetworkStreamManager(nil)
	manager.workspaceSocket = c.workspaceSocketHostPath
	defer manager.close()
	for name, params := range map[string]map[string]any{
		"combined with host": {"workspaceSocket": "plain", "host": "127.0.0.1", "port": 22, "connectTimeoutSeconds": 1},
		"combined with TLS":  {"workspaceSocket": "plain", "tls": map[string]any{"enabled": true}, "connectTimeoutSeconds": 1},
		"traversal":          {"workspaceSocket": "../escape.sock", "connectTimeoutSeconds": 1},
		"absolute":           {"workspaceSocket": "/tmp/escape.sock", "connectTimeoutSeconds": 1},
		"not a socket":       {"workspaceSocket": "plain", "connectTimeoutSeconds": 1},
	} {
		if _, err := manager.open(context.Background(), "owner", mustJSON(t, params)); err == nil {
			t.Errorf("%s: open succeeded", name)
		}
	}
	_, err := manager.open(context.Background(), "owner", mustJSON(t, map[string]any{"workspaceSocket": "missing.sock", "connectTimeoutSeconds": 1}))
	var failure *plugins.HostFailure
	if !errors.As(err, &failure) || failure.Code != "not_found" {
		t.Fatalf("missing socket error = %v", err)
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside.sock")
		listener, err := net.Listen("unix", outside)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		link, _ := c.workspaceHostPath("owner", "link")
		if err := os.Symlink(filepath.Dir(outside), link); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.open(context.Background(), "owner", mustJSON(t, map[string]any{"workspaceSocket": "link/outside.sock", "connectTimeoutSeconds": 1})); err == nil {
			t.Fatal("symlinked socket directory was followed")
		}
	}
	unsupported := newPluginNetworkStreamManager(nil)
	defer unsupported.close()
	if _, err := unsupported.open(context.Background(), "owner", mustJSON(t, map[string]any{"workspaceSocket": "plain", "connectTimeoutSeconds": 1})); err == nil {
		t.Fatal("manager without workspace support opened a socket")
	}
}

func TestPluginProcessStartUsesWorkspaceDirectory(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	if _, err := wsCall(t, c, "owner", "mkdir", "sessions/one", nil); err != nil {
		t.Fatal(err)
	}
	events := make(chan map[string]any, 16)
	c.pluginProcesses = newPluginProcessManager(func(_ string, name string, data any) {
		if name == "process.stderr" || name == "process.exit" {
			value := data.(map[string]any)
			value["event"] = name
			events <- value
		}
	})
	host := controllerPluginHost{controller: c}
	start := func(params map[string]any) error {
		_, err := host.ProcessStart(context.Background(), "owner", mustJSON(t, params))
		return err
	}
	params := map[string]any{"command": os.Args[0], "args": []string{"-test.run=^TestProcessRunHelper$"}, "environment": map[string]string{"RUNPILOT_RUN_HELPER": "1"}, "workspaceDirectory": "sessions/one"}
	if err := start(params); err != nil {
		t.Fatal(err)
	}
	stderr := ""
	deadline := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case event := <-events:
			if event["event"] == "process.stderr" {
				stderr += event["text"].(string)
			} else {
				done = true
			}
		case <-deadline:
			t.Fatal("process did not exit")
		}
	}
	if !strings.Contains(stderr, filepath.Join("sessions", "one")) {
		t.Fatalf("working directory = %q", stderr)
	}
	params["workingDirectory"] = t.TempDir()
	if err := start(params); err == nil {
		t.Fatal("accepted both working directory forms")
	}
	delete(params, "workingDirectory")
	params["workspaceDirectory"] = "../escape"
	if err := start(params); err == nil {
		t.Fatal("accepted workspace traversal")
	}
}

// listenUnixLong creates a Unix listener even when path exceeds sun_path, by
// binding through the parent directory's /proc/self/fd entry on Linux.
func listenUnixLong(path string) (net.Listener, error) {
	if len(path) < maxUnixSocketPath {
		return net.Listen("unix", path)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return net.Listen("unix", "/proc/self/fd/"+strconv.Itoa(int(dir.Fd()))+"/"+filepath.Base(path))
}
