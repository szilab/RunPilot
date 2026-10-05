package core

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
)

const remoteRDPPluginID = "remote.rdp"

type testGuacInstruction struct {
	Opcode string
	Args   []string
}

func readTestGuacInstruction(reader *bufio.Reader) (testGuacInstruction, error) {
	values := []string{}
	for {
		number, err := reader.ReadString('.')
		if err != nil {
			return testGuacInstruction{}, err
		}
		length, err := strconv.Atoi(strings.TrimSuffix(number, "."))
		if err != nil || length < 0 || length > 1<<20 {
			return testGuacInstruction{}, os.ErrInvalid
		}
		value := make([]byte, length)
		if _, err := io.ReadFull(reader, value); err != nil {
			return testGuacInstruction{}, err
		}
		separator, err := reader.ReadByte()
		if err != nil {
			return testGuacInstruction{}, err
		}
		values = append(values, string(value))
		if separator == ';' {
			break
		}
		if separator != ',' {
			return testGuacInstruction{}, os.ErrInvalid
		}
	}
	if len(values) == 0 {
		return testGuacInstruction{}, os.ErrInvalid
	}
	return testGuacInstruction{Opcode: values[0], Args: values[1:]}, nil
}

func writeTestGuacInstruction(writer io.Writer, opcode string, args ...string) error {
	values := append([]string{opcode}, args...)
	var encoded strings.Builder
	for index, value := range values {
		encoded.WriteString(strconv.Itoa(len(value)))
		encoded.WriteByte('.')
		encoded.WriteString(value)
		if index == len(values)-1 {
			encoded.WriteByte(';')
		} else {
			encoded.WriteByte(',')
		}
	}
	_, err := io.WriteString(writer, encoded.String())
	return err
}

func TestRemoteRDPPluginWASMNegotiatesThroughController(t *testing.T) {
	dataDir := t.TempDir()
	copyRemoteRDPPlugin(t, dataDir)
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error {
		enabled := true
		cfg.Plugins = map[string]model.PluginSettings{remoteRDPPluginID: {Enabled: &enabled}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peerResult := make(chan []testGuacInstruction, 1)
	peerFailure := make(chan error, 1)
	peerClosed := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			peerFailure <- acceptErr
			return
		}
		peerClosed <- conn
		reader := bufio.NewReader(conn)
		instructions := make([]testGuacInstruction, 0, 8)
		args := []string{"VERSION_1_5_0", "password", "hostname", "username"}
		for range 64 {
			args = append(args, "unknown-parameter")
		}
		for len(instructions) < 8 {
			instruction, readErr := readTestGuacInstruction(reader)
			if readErr != nil {
				peerFailure <- readErr
				return
			}
			instructions = append(instructions, instruction)
			if instruction.Opcode == "select" {
				if writeErr := writeTestGuacInstruction(conn, "args", args...); writeErr != nil {
					peerFailure <- writeErr
					return
				}
			}
			if instruction.Opcode == "connect" {
				if writeErr := writeTestGuacInstruction(conn, "ready", "ready-connection"); writeErr != nil {
					peerFailure <- writeErr
					return
				}
				peerResult <- instructions
				return
			}
		}
		peerFailure <- os.ErrInvalid
	}()
	address := listener.Addr().(*net.TCPAddr)
	controller, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	settingsRequest, err := json.Marshal(map[string]any{"host": "127.0.0.1", "port": address.Port, "connectTimeoutSeconds": 5})
	if err != nil {
		t.Fatal(err)
	}
	settings, protocolErr := controller.PluginCall(context.Background(), remoteRDPPluginID, "rdp.settings.set", settingsRequest)
	if protocolErr != nil {
		t.Fatal(protocolErr)
	}
	_ = settings
	targetResult, protocolErr := controller.PluginCall(context.Background(), remoteRDPPluginID, "rdp.targets.save", json.RawMessage(`{"target":{"name":"Test desktop","options":{"host":"windows-host","username":"operator","securityMode":"nla"}}}`))
	if protocolErr != nil {
		t.Fatal(protocolErr)
	}
	var saved struct {
		Target struct {
			ID string `json:"id"`
		} `json:"target"`
	}
	if err := json.Unmarshal(targetResult, &saved); err != nil || saved.Target.ID == "" {
		t.Fatalf("saved target response=%s err=%v", targetResult, err)
	}
	openRequest, err := json.Marshal(map[string]any{"targetId": saved.Target.ID, "credentials": map[string]string{"password": "controller-secret"}, "client": map[string]any{"width": 1024, "height": 768, "dpi": 96, "timeZone": "UTC", "clientName": "RunPilot"}})
	if err != nil {
		t.Fatal(err)
	}
	result, protocolErr := controller.PluginCall(context.Background(), remoteRDPPluginID, "rdp.session.open", openRequest)
	if protocolErr != nil {
		t.Fatal(protocolErr)
	}
	if strings.Contains(string(result), "controller-secret") {
		t.Fatalf("WASM response leaked credentials: %s", result)
	}
	var opened struct {
		Session struct {
			ID           string `json:"id"`
			StreamID     string `json:"streamId"`
			ConnectionID string `json:"connectionId"`
			State        string `json:"state"`
		} `json:"session"`
		StreamID string `json:"streamId"`
	}
	if err := json.Unmarshal(result, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.StreamID == "" || opened.Session.ID != opened.StreamID || opened.Session.State != "ready" || opened.Session.ConnectionID != "ready-connection" {
		t.Fatalf("unsafe or incomplete session response: %s", result)
	}
	var instructions []testGuacInstruction
	select {
	case instructions = <-peerResult:
	case err := <-peerFailure:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("fake guacd did not complete the handshake")
	}
	connect := instructions[len(instructions)-1]
	wantConnect := []string{"VERSION_1_5_0", "controller-secret", "windows-host", "operator"}
	for range 64 {
		wantConnect = append(wantConnect, "")
	}
	if connect.Opcode != "connect" || !reflect.DeepEqual(connect.Args, wantConnect) {
		t.Fatalf("WASM connect instruction=%#v", connect)
	}
	conn, release, err := controller.AttachPluginNetworkStream(remoteRDPPluginID, opened.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	peer := <-peerClosed
	if _, err := peer.Write([]byte("post-ready")); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("post-ready"))
	if _, err := conn.Read(got); err != nil || string(got) != "post-ready" {
		t.Fatalf("post-handshake stream read=%q err=%v", got, err)
	}
	if _, err := conn.Write([]byte("browser-ready")); err != nil {
		t.Fatal(err)
	}
	fromBrowser := make([]byte, len("browser-ready"))
	if _, err := peer.Read(fromBrowser); err != nil || string(fromBrowser) != "browser-ready" {
		t.Fatalf("post-handshake stream write=%q err=%v", fromBrowser, err)
	}
	stored, err := os.ReadFile(filepath.Join(dataDir, "plugins", remoteRDPPluginID, "data", "storage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "controller-secret") || strings.Contains(string(stored), "password") {
		t.Fatalf("WASM credential persisted in plugin settings: %s", stored)
	}
}

func copyRemoteRDPPlugin(t *testing.T, dataDir string) {
	t.Helper()
	source := filepath.Join("..", "..", "plugins", "remote-rdp")
	destination := filepath.Join(dataDir, "plugins", remoteRDPPluginID, "releases", sourcePluginVersion(t, source))
	for _, name := range []string{"plugin.yaml", "backend/plugin.wasm", "web/plugin.js", "web/plugin.css", "web/icon.svg"} {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
