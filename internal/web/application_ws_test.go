package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/plugins"
	"github.com/szilab/RunPilot/internal/websocketsecure"
)

func TestApplicationWebSocketRoutesSystemPlugin(t *testing.T) {
	dataDir := t.TempDir()
	if err := plugins.EnsureReferenceSystem(filepath.Join(dataDir, "plugins")); err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error {
		enabled := true
		cfg.Plugins = map[string]model.PluginSettings{}
		cfg.Plugins["system"] = model.PluginSettings{Enabled: &enabled}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctrl, err := core.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ticketRequest, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ws/ticket", nil)
	ticketRequest.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
	ticketResponse, err := http.DefaultClient.Do(ticketRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer ticketResponse.Body.Close()
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(ticketResponse.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/ws?ticket=" + ticket.Ticket
	connection, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	for i := 1; i <= 64; i++ {
		params := map[string]any{}
		if i == 2 {
			params["padding"] = strings.Repeat("x", 20<<10)
		}
		request, err := json.Marshal(map[string]any{"id": strconv.Itoa(42 + i), "plugin": "system", "method": "status.get", "params": params})
		if err != nil {
			t.Fatal(err)
		}
		if err := connection.Write(context.Background(), websocket.MessageText, request); err != nil {
			t.Fatal(err)
		}
		_, payload, err := connection.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var response plugins.Response
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatal(err)
		}
		if response.ID != strconv.Itoa(42+i) || response.Error != nil || !bytes.Contains(response.Result, []byte(`"hostname"`)) || result["pluginCallCount"] != float64(i) {
			t.Fatalf("response #%d = %s", i, payload)
		}
		_, payload, err = connection.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var event plugins.Event
		if err := json.Unmarshal(payload, &event); err != nil || event.Plugin != "system" || event.Event != "status.changed" {
			t.Fatalf("event #%d = %s, %v", i, payload, err)
		}
	}
	request := []byte(`{"id":"43","plugin":"system","method":"missing.method","params":{}}`)
	if err := connection.Write(context.Background(), websocket.MessageText, request); err != nil {
		t.Fatal(err)
	}
	_, payload, err := connection.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var response plugins.Response
	if err := json.Unmarshal(payload, &response); err != nil || response.Error != nil || !bytes.Contains(response.Result, []byte(`"unknown_method"`)) {
		t.Fatalf("plugin error response = %s, %v", payload, err)
	}
	if err := connection.Write(context.Background(), websocket.MessageText, []byte(`{"id":"43","plugin":"missing","method":"status.get","params":{}}`)); err != nil {
		t.Fatal(err)
	}
	_, payload, err = connection.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != "unknown_plugin" {
		t.Fatalf("unknown plugin response = %s", payload)
	}
}

func TestApplicationWebSocketMultiplexesOwnedNetworkStreamAndRPC(t *testing.T) {
	dataDir := t.TempDir()
	if err := plugins.EnsureReferenceSystem(filepath.Join(dataDir, "plugins")); err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error {
		enabled := true
		cfg.Plugins = map[string]model.PluginSettings{"system": {Enabled: &enabled}}
		cfg.Server.WebSocketPayloadMode = websocketsecure.ModeRequired
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctrl, err := core.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewTLSServer(server.Handler())
	defer httpServer.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peerReady := make(chan net.Conn, 1)
	peerDone := make(chan error, 1)
	go func() {
		peer, acceptErr := listener.Accept()
		if acceptErr != nil {
			peerDone <- acceptErr
			return
		}
		peerReady <- peer
		input := make([]byte, len("browser-to-tcp"))
		if _, readErr := io.ReadFull(peer, input); readErr != nil {
			peerDone <- readErr
			return
		}
		if string(input) != "browser-to-tcp" {
			peerDone <- fmt.Errorf("unexpected TCP input %q", input)
			return
		}
		if _, writeErr := peer.Write([]byte("tcp-to-browser")); writeErr != nil {
			peerDone <- writeErr
			return
		}
		one := make([]byte, 1)
		_, readErr := peer.Read(one)
		if !errors.Is(readErr, io.EOF) {
			peerDone <- fmt.Errorf("TCP stream was not released: %v", readErr)
			return
		}
		peerDone <- nil
	}()
	address := listener.Addr().(*net.TCPAddr)
	opened, err := ctrl.OpenPluginNetworkStream(context.Background(), "system", json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"connectTimeoutSeconds":2}`, address.Port)))
	if err != nil {
		t.Fatal(err)
	}
	var stream struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(opened, &stream); err != nil || stream.ID == "" {
		t.Fatalf("open stream response=%s err=%v", opened, err)
	}
	<-peerReady
	ticket := requestApplicationTicket(t, httpServer.Client(), httpServer.URL, ctrl.Snapshot().Server.Token)
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/ws?ticket=" + ticket
	rawConnection, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPClient: httpServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	private, hello, random, err := websocketsecure.NewClientHello()
	if err != nil {
		t.Fatal(err)
	}
	if err := rawConnection.Write(context.Background(), websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	_, serverHello, err := rawConnection.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocketsecure.ClientSession(rawConnection, private, random, serverHello)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawConnection.Close(websocket.StatusNormalClosure, "test cleanup") })
	if err := connection.Write(context.Background(), websocket.MessageBinary, []byte("malformed")); err != nil {
		t.Fatal(err)
	}
	var control map[string]any
	if err := readApplicationJSON(t, connection, &control); err != nil || control["type"] != "stream.error" || control["code"] != "bad_request" {
		t.Fatalf("malformed stream frame response=%v err=%v", control, err)
	}
	if err := connection.Write(context.Background(), websocket.MessageBinary, applicationTestStreamFrame("unknown", []byte("x"))); err != nil {
		t.Fatal(err)
	}
	if err := readApplicationJSON(t, connection, &control); err != nil || control["type"] != "stream.error" || control["code"] != "not_found" {
		t.Fatalf("unknown stream response=%v err=%v", control, err)
	}
	if err := connection.Write(context.Background(), websocket.MessageText, applicationTestJSON(t, map[string]string{"type": "stream.attach", "plugin": "test.streams", "streamId": stream.ID})); err != nil {
		t.Fatal(err)
	}
	if err := readApplicationJSON(t, connection, &control); err != nil {
		t.Fatal(err)
	}
	if control["type"] != "stream.error" || control["code"] != "not_found" {
		t.Fatalf("foreign stream attach response=%v", control)
	}
	if err := connection.Write(context.Background(), websocket.MessageText, applicationTestJSON(t, map[string]string{"type": "stream.attach", "plugin": "system", "streamId": stream.ID})); err != nil {
		t.Fatal(err)
	}
	if err := readApplicationJSON(t, connection, &control); err != nil {
		t.Fatal(err)
	}
	if control["type"] != "stream.attached" {
		t.Fatalf("stream attach response=%v", control)
	}
	if err := connection.Write(context.Background(), websocket.MessageText, []byte(`{"id":"rpc-while-streaming","plugin":"system","method":"status.get","params":{}}`)); err != nil {
		t.Fatal(err)
	}
	var response plugins.Response
	for {
		_, payload, readErr := connection.Read(context.Background())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if json.Unmarshal(payload, &response) == nil && response.ID == "rpc-while-streaming" {
			break
		}
	}
	if response.Error != nil || len(response.Result) == 0 {
		t.Fatalf("ordinary RPC failed while stream active: %#v", response)
	}
	if err := connection.Write(context.Background(), websocket.MessageBinary, applicationTestStreamFrame(stream.ID, []byte("browser-to-tcp"))); err != nil {
		t.Fatal(err)
	}
	var kind websocket.MessageType
	var frame []byte
	for kind != websocket.MessageBinary {
		kind, frame, err = connection.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	gotID, payload, err := decodeApplicationStreamFrame(frame, 2)
	if err != nil || gotID != stream.ID || string(payload) != "tcp-to-browser" {
		t.Fatalf("stream response id=%q payload=%q err=%v", gotID, payload, err)
	}
	if err := connection.Write(context.Background(), websocket.MessageText, applicationTestJSON(t, map[string]string{"type": "stream.close", "streamId": stream.ID})); err != nil {
		t.Fatal(err)
	}
	for {
		if err := readApplicationJSON(t, connection, &control); err != nil {
			t.Fatal(err)
		}
		if control["type"] == "stream.closed" && control["streamId"] == stream.ID {
			break
		}
	}
	select {
	case err := <-peerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream.close did not close its TCP connection")
	}
	disconnectListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer disconnectListener.Close()
	disconnectAddress := disconnectListener.Addr().(*net.TCPAddr)
	disconnectStream, err := ctrl.OpenPluginNetworkStream(context.Background(), "system", json.RawMessage(fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"connectTimeoutSeconds":2}`, disconnectAddress.Port)))
	if err != nil {
		t.Fatal(err)
	}
	var disconnectStreamID struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(disconnectStream, &disconnectStreamID); err != nil {
		t.Fatal(err)
	}
	disconnectPeer, err := disconnectListener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer disconnectPeer.Close()
	if err := connection.Write(context.Background(), websocket.MessageText, applicationTestJSON(t, map[string]string{"type": "stream.attach", "plugin": "system", "streamId": disconnectStreamID.ID})); err != nil {
		t.Fatal(err)
	}
	for {
		if err := readApplicationJSON(t, connection, &control); err != nil {
			t.Fatal(err)
		}
		if control["type"] == "stream.attached" && control["streamId"] == disconnectStreamID.ID {
			break
		}
	}
	if err := rawConnection.Close(websocket.StatusNormalClosure, "test complete"); err != nil {
		t.Fatal(err)
	}
	if err := disconnectPeer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := disconnectPeer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("application WebSocket disconnect did not close its TCP stream: %v", err)
	}
}

func requestApplicationTicket(t *testing.T, client *http.Client, baseURL, token string) string {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/ws/ticket", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(response.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	return ticket.Ticket
}

func readApplicationJSON(t *testing.T, connection interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
}, target any) error {
	t.Helper()
	kind, payload, err := connection.Read(context.Background())
	if err != nil {
		return err
	}
	if kind != websocket.MessageText {
		return fmt.Errorf("expected JSON text, got WebSocket message type %v", kind)
	}
	return json.Unmarshal(payload, target)
}

func applicationTestStreamFrame(id string, payload []byte) []byte {
	frame := make([]byte, 6+len(id)+len(payload))
	copy(frame[:4], applicationStreamMagic[:])
	frame[4] = 1
	frame[5] = byte(len(id))
	copy(frame[6:], id)
	copy(frame[6+len(id):], payload)
	return frame
}

func applicationTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
