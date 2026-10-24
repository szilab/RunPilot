package core

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
)

// The compiled Remote plugin uses network.probe for target status and
// network.neighbor.lookup before Wake-on-LAN.
func TestRemotePluginWASMReportsTargetStatus(t *testing.T) {
	dataDir := t.TempDir()
	copyRemoteVNCPlugin(t, dataDir)
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error {
		enabled := true
		cfg.Plugins = map[string]model.PluginSettings{remoteVNCPluginID: {Enabled: &enabled}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()

	controller, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	call := func(method string, params any) string {
		t.Helper()
		raw, _ := json.Marshal(params)
		result, protocolErr := controller.PluginCall(context.Background(), remoteVNCPluginID, method, raw)
		if protocolErr != nil {
			t.Fatalf("%s: %v", method, protocolErr)
		}
		return string(result)
	}
	call("vnc.targets.save", map[string]any{"target": map[string]any{"name": "Up", "host": "127.0.0.1", "port": listener.Addr().(*net.TCPAddr).Port}})
	call("vnc.targets.save", map[string]any{"target": map[string]any{"name": "Down", "host": "127.0.0.1", "port": closedPort}})
	status := call("remote.targets.status", map[string]any{})
	for _, want := range []string{`"id":"vnc-1","state":"online"`, `"id":"vnc-2","state":"service-down"`} {
		if !strings.Contains(status, want) {
			t.Fatalf("status %s misses %s", status, want)
		}
	}
	// Loopback has no neighbor entry, so no packet is sent and the user is told why.
	wake := call("remote.targets.wake", map[string]any{"kind": "vnc", "id": "vnc-2"})
	if !strings.Contains(wake, "not_found") || !strings.Contains(wake, "No MAC address is known for Down") {
		t.Fatalf("wake without MAC = %s", wake)
	}
}
