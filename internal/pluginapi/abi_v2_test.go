package pluginapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeCapabilityResponsePreservesStructuredError(t *testing.T) {
	var details struct {
		Field string `json:"field"`
	}
	err := decodeCapabilityResponse([]byte(`{"ok":false,"error":{"code":"invalid_argument","message":"bad value","details":{"field":"name"}}}`), nil)
	capabilityErr, ok := err.(*Error)
	if !ok || capabilityErr.Code != "invalid_argument" || capabilityErr.Message != "bad value" {
		t.Fatalf("capability error = %#v", err)
	}
	if err := json.Unmarshal(capabilityErr.Details, &details); err != nil || details.Field != "name" {
		t.Fatalf("capability details = %s, err = %v", capabilityErr.Details, err)
	}
	if !strings.Contains(capabilityErr.Error(), "invalid_argument") || !strings.Contains(capabilityErr.Error(), "bad value") {
		t.Fatalf("capability error string = %q", capabilityErr.Error())
	}
	combined := &combinedError{cause: capabilityErr, cleanup: &ABIError{Operation: "response_drop", Reason: "cleanup failed"}}
	if got, ok := AsCapabilityError(combined); !ok || got.Code != "invalid_argument" {
		t.Fatalf("combined error lost capability details: %#v", got)
	}
}

func TestDecodeCapabilityResponseSuccessAndProtocolFailures(t *testing.T) {
	if err := decodeCapabilityResponse([]byte(`{"ok":true}`), nil); err != nil {
		t.Fatalf("empty successful result with nil destination: %v", err)
	}
	var result struct {
		Value string `json:"value"`
	}
	if err := decodeCapabilityResponse([]byte(`{"ok":true,"result":{"value":"ok"}}`), &result); err != nil || result.Value != "ok" {
		t.Fatalf("decoded result = %#v, err = %v", result, err)
	}
	for _, payload := range []string{`not-json`, `{"ok":false}`, `{"ok":true,"result":"wrong-shape"}`} {
		destination := any(nil)
		if strings.Contains(payload, "wrong-shape") {
			destination = &result
		}
		if err := decodeCapabilityResponse([]byte(payload), destination); err == nil {
			t.Fatalf("accepted invalid capability response %q", payload)
		}
	}
}

func TestNativeABIV2HelpersReportTransportFailures(t *testing.T) {
	if err := ReadInput(1, new(map[string]any)); err == nil {
		t.Fatal("native input stub unexpectedly succeeded")
	} else if _, ok := err.(*ABIError); !ok {
		t.Fatalf("ReadInput error type = %T", err)
	}
	if err := WriteOutput(1, map[string]any{"ok": true}); err == nil {
		t.Fatal("native output stub unexpectedly succeeded")
	}
	if err := CallHost("system.status", map[string]any{}, nil); err == nil {
		t.Fatal("native host-call stub unexpectedly succeeded")
	} else if _, ok := err.(*ABIError); !ok {
		t.Fatalf("CallHost error type = %T", err)
	}
}
