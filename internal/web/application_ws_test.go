package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/plugins"
)

func TestApplicationWebSocketRoutesSystemPlugin(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
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
	request := []byte(`{"id":"42","plugin":"system","method":"status.get","params":{}}`)
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
	if response.ID != "42" || response.Error != nil || !bytes.Contains(response.Result, []byte(`"hostname"`)) {
		t.Fatalf("response = %s", payload)
	}
	_, payload, err = connection.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var event plugins.Event
	if err := json.Unmarshal(payload, &event); err != nil || event.Plugin != "system" || event.Event != "status.changed" {
		t.Fatalf("event = %s, %v", payload, err)
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
