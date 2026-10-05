package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// BrowserProtocolVersion is the version of the common browser/server WebSocket
// envelope. It is independent from both the package and capability APIs.
const BrowserProtocolVersion = 1

type Request struct {
	ID     string          `json:"id"`
	Plugin string          `json:"plugin"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ProtocolError  `json:"error,omitempty"`
}

type Event struct {
	Plugin string          `json:"plugin"`
	Event  string          `json:"event"`
	Data   json.RawMessage `json:"data"`
}

// ParseRequest rejects unknown/malformed fields before a message reaches a
// plugin. The transport can turn parse errors into a structured bad_request
// response when an id was recoverable.
func ParseRequest(payload []byte) (Request, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("decode request: %w", err)
	}
	if decoder.More() {
		return Request{}, fmt.Errorf("decode request: trailing data")
	}
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.Plugin) == "" || strings.TrimSpace(request.Method) == "" {
		return Request{}, fmt.Errorf("request id, plugin, and method are required")
	}
	if len(request.Params) == 0 {
		request.Params = json.RawMessage("{}")
	}
	if !json.Valid(request.Params) {
		return Request{}, fmt.Errorf("request params must be JSON")
	}
	return request, nil
}

func Success(id string, result any) Response {
	payload, err := json.Marshal(result)
	if err != nil {
		payload = json.RawMessage(`null`)
	}
	return Response{ID: id, Result: payload}
}

func Failure(id, code, message string) Response {
	return Response{ID: id, Error: &ProtocolError{Code: code, Message: message}}
}

// RPCHandler is transport-independent, so one authenticated WebSocket can
// route messages without feature-specific REST or socket implementations.
type RPCHandler interface {
	Call(context.Context, string, json.RawMessage) (json.RawMessage, *ProtocolError)
}

type Dispatcher struct {
	Timeout time.Duration
	Plugins map[string]RPCHandler
}

func (d Dispatcher) Dispatch(parent context.Context, request Request) Response {
	handler := d.Plugins[request.Plugin]
	if handler == nil {
		return Failure(request.ID, "unknown_plugin", "plugin is not loaded")
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result, rpcErr := handler.Call(ctx, request.Method, request.Params)
	if rpcErr != nil {
		return Response{ID: request.ID, Error: rpcErr}
	}
	if err := ctx.Err(); err != nil {
		return Failure(request.ID, "timeout", "plugin call timed out")
	}
	if !json.Valid(result) {
		return Failure(request.ID, "plugin_failure", "plugin returned invalid JSON")
	}
	return Response{ID: request.ID, Result: result}
}
