package web

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/plugins"
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
