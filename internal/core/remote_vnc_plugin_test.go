package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
)

const remoteVNCPluginID = "remote.vnc"

func TestRemoteVNCPluginWASMOwnsTargetsAndTCPSessionLifecycle(t *testing.T) {
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
	peers := make(chan net.Conn, 2)
	go func() {
		for range 2 {
			conn, acceptErr := listener.Accept()
			if acceptErr == nil {
				peers <- conn
			}
		}
	}()

	controller, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	targetPayload, _ := json.Marshal(map[string]any{"target": map[string]any{"name": "Test VNC", "host": "127.0.0.1", "port": listener.Addr().(*net.TCPAddr).Port, "username": "operator", "connectTimeoutSeconds": 4}})
	created, protocolErr := controller.PluginCall(context.Background(), remoteVNCPluginID, "vnc.targets.save", targetPayload)
	if protocolErr != nil {
		controller.Close()
		t.Fatal(protocolErr)
	}
	var saved struct {
		Target struct {
			ID string `json:"id"`
		} `json:"target"`
	}
	if err := json.Unmarshal(created, &saved); err != nil || saved.Target.ID == "" {
		controller.Close()
		t.Fatalf("target response=%s err=%v", created, err)
	}
	var response json.RawMessage
	for _, invalid := range []string{`{"target":{"name":"bad","host":"127.0.0.1/path"}}`, `{"target":{"name":"bad","host":"localhost","port":70000}}`} {
		response, err := controller.PluginCall(context.Background(), remoteVNCPluginID, "vnc.targets.save", json.RawMessage(invalid))
		if err == nil && !pluginResponseHasError(response) {
			controller.Close()
			t.Fatalf("invalid VNC target accepted: %s", invalid)
		}
	}
	response, protocolErr = controller.PluginCall(context.Background(), remoteVNCPluginID, "vnc.targets.save", json.RawMessage(`{"target":{"name":"bad","host":"localhost","password":"must-not-persist"}}`))
	if protocolErr == nil && !pluginResponseHasError(response) {
		controller.Close()
		t.Fatal("VNC target accepted a password field")
	}
	open := func() (string, net.Conn) {
		t.Helper()
		request, _ := json.Marshal(map[string]string{"targetId": saved.Target.ID})
		result, callErr := controller.PluginCall(context.Background(), remoteVNCPluginID, "vnc.session.open", request)
		if callErr != nil {
			controller.Close()
			t.Fatal(callErr)
		}
		var response struct {
			StreamID string `json:"streamId"`
			Session  struct {
				ID         string `json:"id"`
				TargetName string `json:"targetName"`
			} `json:"session"`
		}
		if err := json.Unmarshal(result, &response); err != nil || response.StreamID == "" || response.StreamID != response.Session.ID || response.Session.TargetName != "Test VNC" {
			controller.Close()
			t.Fatalf("unsafe or incomplete session response: %s err=%v", result, err)
		}
		if strings.Contains(string(result), "must-not-persist") || strings.Contains(strings.ToLower(string(result)), "password") {
			controller.Close()
			t.Fatalf("credential leaked in session response: %s", result)
		}
		peer := <-peers
		return response.StreamID, peer
	}
	streamID, peer := open()
	response, protocolErr = controller.PluginCall(context.Background(), remoteVNCPluginID, "vnc.targets.delete", json.RawMessage(`{"id":"vnc-1"}`))
	if protocolErr == nil && !pluginResponseHasError(response) {
		controller.Close()
		t.Fatal("deleting target with active session succeeded")
	}
	if _, _, err := controller.AttachPluginNetworkStream("another.plugin", streamID); err == nil {
		controller.Close()
		t.Fatal("a different plugin attached to the VNC stream")
	}
	client, release, err := controller.AttachPluginNetworkStream(remoteVNCPluginID, streamID)
	if err != nil {
		peer.Close()
		controller.Close()
		t.Fatal(err)
	}
	defer release()
	serverBytes := []byte{0, 82, 70, 66, 255, 10}
	if _, err := peer.Write(serverBytes); err != nil {
		peer.Close()
		controller.Close()
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, len(serverBytes))
	if _, err := io.ReadFull(client, got); err != nil || string(got) != string(serverBytes) {
		peer.Close()
		controller.Close()
		t.Fatalf("server stream bytes=%v err=%v", got, err)
	}
	clientBytes := []byte{255, 0, 1, 128}
	if _, err := client.Write(clientBytes); err != nil {
		peer.Close()
		controller.Close()
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	got = make([]byte, len(clientBytes))
	if _, err := io.ReadFull(peer, got); err != nil || string(got) != string(clientBytes) {
		peer.Close()
		controller.Close()
		t.Fatalf("client stream bytes=%v err=%v", got, err)
	}
	if _, err := controller.PluginCall(context.Background(), remoteVNCPluginID, "vnc.session.close", json.RawMessage(`{"id":"`+streamID+`"}`)); err != nil {
		peer.Close()
		controller.Close()
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		peer.Close()
		controller.Close()
		t.Fatalf("VNC session close left TCP stream open: %v", err)
	}
	peer.Close()

	_, shutdownPeer := open()
	controller.Close() // ABI shutdown closes any still-owned network stream.
	_ = shutdownPeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := shutdownPeer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		shutdownPeer.Close()
		t.Fatalf("VNC plugin shutdown left its active TCP stream open: %v", err)
	}
	shutdownPeer.Close()

	stored, err := os.ReadFile(filepath.Join(dataDir, "plugins", remoteVNCPluginID, "data", "storage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(stored)), "password") || strings.Contains(string(stored), "must-not-persist") {
		t.Fatalf("VNC credentials persisted: %s", stored)
	}
}

func pluginResponseHasError(raw json.RawMessage) bool {
	var response struct {
		Error json.RawMessage `json:"error"`
	}
	return json.Unmarshal(raw, &response) == nil && len(response.Error) != 0 && string(response.Error) != "null"
}

func copyRemoteVNCPlugin(t *testing.T, dataDir string) {
	t.Helper()
	source := filepath.Join("..", "..", "plugins", "remote-vnc")
	destination := filepath.Join(dataDir, "plugins", remoteVNCPluginID, "releases", sourcePluginVersion(t, source))
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative != "plugin.yaml" && !strings.HasPrefix(relative, "backend"+string(filepath.Separator)) && !strings.HasPrefix(relative, "web"+string(filepath.Separator)) {
			return nil
		}
		if strings.HasPrefix(relative, "backend"+string(filepath.Separator)) && relative != filepath.Join("backend", "plugin.wasm") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		output := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			return err
		}
		return os.WriteFile(output, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
