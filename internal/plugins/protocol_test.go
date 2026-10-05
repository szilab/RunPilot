package plugins

import (
	"context"
	"encoding/json"
	"testing"
)

func TestProtocolRequestAndEventContracts(t *testing.T) {
	request, err := ParseRequest([]byte(`{"id":"42","plugin":"system","method":"status.get","params":{}}`))
	if err != nil || request.ID != "42" {
		t.Fatalf("ParseRequest() = %#v, %v", request, err)
	}
	if _, err := ParseRequest([]byte(`{"id":"42","plugin":"system","method":"status.get","unexpected":true}`)); err == nil {
		t.Fatal("unknown request field accepted")
	}
	event := Event{Plugin: "system", Event: "status.changed", Data: json.RawMessage(`{}`)}
	if _, err := json.Marshal(event); err != nil {
		t.Fatalf("event does not marshal: %v", err)
	}
}

func TestDispatcherReturnsStructuredErrors(t *testing.T) {
	request, _ := ParseRequest([]byte(`{"id":"42","plugin":"missing","method":"status.get","params":{}}`))
	response := (Dispatcher{}).Dispatch(context.Background(), request)
	if response.Error == nil || response.Error.Code != "unknown_plugin" {
		t.Fatalf("response = %#v", response)
	}
}
