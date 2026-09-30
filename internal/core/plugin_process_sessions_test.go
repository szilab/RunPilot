package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

type capturedSessionEvent struct {
	event string
	data  map[string]any
}

func newTestSessionManager() (*pluginSessionManager, <-chan capturedSessionEvent) {
	events := make(chan capturedSessionEvent, 256)
	manager := newPluginSessionManager(func(owner, event string, data any) bool {
		encoded, _ := json.Marshal(data)
		object := map[string]any{}
		_ = json.Unmarshal(encoded, &object)
		select {
		case events <- capturedSessionEvent{event: event, data: object}:
			return true
		default:
			return false
		}
	})
	return manager, events
}
func helperCommand(mode string) pluginSessionStart {
	var in pluginSessionStart
	in.Command = os.Args[0]
	in.Args = []string{"-test.run=^TestInteractiveSessionHelper$"}
	in.Environment = map[string]string{"RUNPILOT_SESSION_HELPER": mode}
	in.Size.Rows, in.Size.Columns = 24, 80
	return in
}
func TestInteractiveSessionHelper(t *testing.T) {
	mode := os.Getenv("RUNPILOT_SESSION_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "echo":
		var input string
		_, _ = fmt.Scanln(&input)
		fmt.Printf("RP_SESSION:%s\n", input)
	case "flood":
		for i := 0; i < 500; i++ {
			fmt.Printf("RP_CHUNK_%04d_abcdefghijklmnopqrstuvwxyz0123456789\n", i)
		}
	case "wait":
		select {}
	}
}
func awaitSessionEvent(t *testing.T, events <-chan capturedSessionEvent, name, id string) capturedSessionEvent {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case event := <-events:
			if event.event == name && event.data["id"] == id {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s for %s", name, id)
		}
	}
}
func createTestSession(t *testing.T, m *pluginSessionManager, mode string) string {
	t.Helper()
	raw, _ := json.Marshal(helperCommand(mode))
	response, err := m.create("owner", raw)
	if err != nil {
		t.Fatal(err)
	}
	var status pluginSessionStatus
	if err = json.Unmarshal(response, &status); err != nil {
		t.Fatal(err)
	}
	if status.ID == "" || status.State != "running" {
		t.Fatalf("create response %#v", status)
	}
	return status.ID
}
func TestPluginProcessSessionCreateInputResizeStatusAndOutput(t *testing.T) {
	m, events := newTestSessionManager()
	defer m.close()
	id := createTestSession(t, m, "echo")
	if _, err := m.resize("owner", json.RawMessage(fmt.Sprintf(`{"id":%q,"rows":31,"columns":101}`, id))); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"id": id, "data": base64.StdEncoding.EncodeToString([]byte("hello-session\n"))})
	if _, err := m.write("owner", input); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	var previous float64
	for {
		event := awaitSessionEvent(t, events, "process.session.output", id)
		chunk, err := base64.StdEncoding.DecodeString(event.data["data"].(string))
		if err != nil {
			t.Fatal(err)
		}
		seq := event.data["sequence"].(float64)
		if seq != previous+1 {
			t.Fatalf("output sequence %v did not follow %v", seq, previous)
		}
		previous = seq
		output.Write(chunk)
		if strings.Contains(output.String(), "RP_SESSION:hello-session") {
			break
		}
	}
	if _, err := m.status("owner", json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))); err != nil {
		t.Fatal(err)
	}
	exit := awaitSessionEvent(t, events, "process.session.exit", id)
	if exit.data["reason"] != "exited" {
		t.Fatalf("exit event %#v", exit.data)
	}
}
func TestPluginProcessSessionOwnershipAndBounds(t *testing.T) {
	m, _ := newTestSessionManager()
	defer m.close()
	id := createTestSession(t, m, "wait")
	if _, err := m.status("other", json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))); err == nil {
		t.Fatal("other plugin queried session")
	}
	if _, err := m.write("owner", json.RawMessage(`{"id":"missing","data":"YQ=="}`)); err == nil {
		t.Fatal("unknown session accepted")
	}
	if _, err := m.write("owner", json.RawMessage(fmt.Sprintf(`{"id":%q,"data":%q}`, id, base64.StdEncoding.EncodeToString(make([]byte, maxPluginSessionInput+1))))); err == nil {
		t.Fatal("oversized input accepted")
	}
	in := helperCommand("wait")
	in.Size.Columns = maxPluginSessionColumns + 1
	if err := validatePluginSessionStart(in); err == nil {
		t.Fatal("oversized dimensions accepted")
	}
	if _, err := m.resize("owner", json.RawMessage(fmt.Sprintf(`{"id":%q,"rows":0,"columns":80}`, id))); err == nil {
		t.Fatal("zero rows accepted")
	}
}
func TestPluginProcessSessionOutputOverflowFailsExplicitly(t *testing.T) {
	var mu sync.Mutex
	var events []capturedSessionEvent
	m := newPluginSessionManager(func(owner, event string, data any) bool {
		encoded, _ := json.Marshal(data)
		object := map[string]any{}
		_ = json.Unmarshal(encoded, &object)
		mu.Lock()
		events = append(events, capturedSessionEvent{event: event, data: object})
		mu.Unlock()
		return event != "process.session.output"
	})
	defer m.close()
	id := createTestSession(t, m, "flood")
	deadline := time.After(8 * time.Second)
	for {
		mu.Lock()
		hasExit, hasError := false, false
		for _, event := range events {
			if event.event == "process.session.exit" && event.data["id"] == id {
				hasExit = true
			}
			if event.event == "process.session.error" && event.data["id"] == id {
				hasError = true
			}
		}
		mu.Unlock()
		if hasExit && hasError {
			break
		}
		select {
		case <-deadline:
			t.Fatal("overflow did not produce error and exit events")
		case <-time.After(10 * time.Millisecond):
		}
	}
	status, err := m.status("owner", json.RawMessage(fmt.Sprintf(`{"id":%q}`, id)))
	if err != nil {
		t.Fatal(err)
	}
	var state pluginSessionStatus
	_ = json.Unmarshal(status, &state)
	if state.Reason != "io_error" {
		t.Fatalf("status %#v", state)
	}
}
func TestPluginProcessSessionTerminateAndShutdownCleanup(t *testing.T) {
	m, events := newTestSessionManager()
	id := createTestSession(t, m, "wait")
	if _, err := m.terminate("owner", json.RawMessage(fmt.Sprintf(`{"id":%q,"force":true}`, id))); err != nil {
		t.Fatal(err)
	}
	exit := awaitSessionEvent(t, events, "process.session.exit", id)
	if exit.data["reason"] != "terminated" {
		t.Fatalf("exit %#v", exit.data)
	}
	other := createTestSession(t, m, "wait")
	otherSession, _ := m.lookup("owner", other)
	m.close()
	select {
	case <-otherSession.process.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown left the session process running")
	}
	exit = awaitSessionEvent(t, events, "process.session.exit", other)
	if exit.data["reason"] != "terminated" {
		t.Fatalf("shutdown exit %#v", exit.data)
	}
}

func TestPluginProcessSessionPerOwnerLimit(t *testing.T) {
	m, _ := newTestSessionManager()
	defer m.close()
	for i := 0; i < maxPluginSessionsPerOwner; i++ {
		createTestSession(t, m, "wait")
	}
	raw, _ := json.Marshal(helperCommand("wait"))
	if _, err := m.create("owner", raw); err == nil {
		t.Fatal("per-plugin session limit was not enforced")
	}
}

func TestProcessSessionBrowserEventQueueOverflowIsReported(t *testing.T) {
	controller := &Controller{eventSubscribers: map[uint64]chan plugins.Event{1: make(chan plugins.Event, 1)}}
	if err := (controllerPluginHost{controller}).PublishEvent(nil, "owner", "process.session.output", json.RawMessage(`{"id":"s","sequence":1,"data":"YQ=="}`)); err != nil {
		t.Fatalf("first event: %v", err)
	}
	if err := (controllerPluginHost{controller}).PublishEvent(nil, "owner", "process.session.output", json.RawMessage(`{"id":"s","sequence":2,"data":"Yg=="}`)); err == nil {
		t.Fatal("full browser queue silently accepted session output")
	}
}
