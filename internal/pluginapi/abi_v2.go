package pluginapi

import "encoding/json"

// ABIV2PayloadLimit matches the host's maximum input, output, and capability
// payload size. ABI v2 operations copy every byte synchronously.
const ABIV2PayloadLimit = 16 << 20

// The runtime serializes lifecycle entry into one WASM module, so this
// invocation context cannot be observed concurrently by plugin code.
var activeInvocation uint32

// ABIError describes an ABI transport or protocol failure, separate from a
// structured capability Error returned by the host.
type ABIError struct {
	Operation string
	Reason    string
}

func (e *ABIError) Error() string {
	if e == nil {
		return "ABI v2 failure"
	}
	if e.Operation == "" {
		return "ABI v2: " + e.Reason
	}
	return "ABI v2 " + e.Operation + ": " + e.Reason
}

type capabilityRequestV2 struct {
	APIVersion int    `json:"apiVersion"`
	Capability string `json:"capability"`
	Params     any    `json:"params"`
}

type capabilityResponseV2 struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// ReadInput copies the host-owned lifecycle input into a normal TinyGo-local
// byte slice before decoding it. No view into linear memory is retained.
func ReadInput(handle uint32, dst any) error {
	activeInvocation = 0
	length := v2InputLen(handle)
	if length == 0 {
		return &ABIError{Operation: "input_read", Reason: "invalid handle or empty input"}
	}
	if length > ABIV2PayloadLimit {
		return &ABIError{Operation: "input_read", Reason: "input exceeds payload limit"}
	}
	input := make([]byte, int(length))
	if got := v2InputRead(handle, input); got != length {
		return &ABIError{Operation: "input_read", Reason: "host returned an invalid byte count"}
	}
	if err := json.Unmarshal(input, dst); err != nil {
		return &ABIError{Operation: "input_decode", Reason: "invalid lifecycle JSON"}
	}
	activeInvocation = handle
	return nil
}

// WriteOutput copies JSON-encoded output into the host-owned lifecycle
// invocation before this synchronous call returns.
func WriteOutput(handle uint32, value any) error {
	defer func() { activeInvocation = 0 }()
	output, err := json.Marshal(value)
	if err != nil {
		return &ABIError{Operation: "output_encode", Reason: "could not encode lifecycle JSON"}
	}
	if len(output) > ABIV2PayloadLimit {
		return &ABIError{Operation: "output_write", Reason: "output exceeds payload limit"}
	}
	if v2OutputWrite(handle, output) != 1 {
		return &ABIError{Operation: "output_write", Reason: "host rejected lifecycle output"}
	}
	return nil
}

// CallHost performs a JSON capability call and always attempts to drop the
// invocation-owned response handle before returning.
func CallHost(method string, params any, result any) (callErr error) {
	handle := activeInvocation
	if handle == 0 {
		return &ABIError{Operation: "host_call", Reason: "no active lifecycle invocation"}
	}
	request, err := json.Marshal(capabilityRequestV2{APIVersion: CapabilityAPIVersion, Capability: method, Params: params})
	if err != nil {
		return &ABIError{Operation: "host_call", Reason: "could not encode capability request"}
	}
	if len(request) > ABIV2PayloadLimit {
		return &ABIError{Operation: "host_call", Reason: "request exceeds payload limit"}
	}
	responseHandle := v2HostCall(handle, request)
	if responseHandle == 0 {
		return &ABIError{Operation: "host_call", Reason: "host rejected capability request"}
	}
	defer func() {
		if v2ResponseDrop(handle, responseHandle) != 1 {
			cleanupErr := &ABIError{Operation: "response_drop", Reason: "host rejected response cleanup"}
			if callErr == nil {
				callErr = cleanupErr
			} else {
				callErr = &combinedError{cause: callErr, cleanup: cleanupErr}
			}
		}
	}()

	length := v2ResponseLen(handle, responseHandle)
	if length == 0 {
		return &ABIError{Operation: "response_read", Reason: "host returned an empty or invalid response"}
	}
	if length > ABIV2PayloadLimit {
		return &ABIError{Operation: "response_read", Reason: "response exceeds payload limit"}
	}
	payload := make([]byte, int(length))
	if got := v2ResponseRead(handle, responseHandle, payload); got != length {
		return &ABIError{Operation: "response_read", Reason: "host returned an invalid byte count"}
	}
	return decodeCapabilityResponse(payload, result)
}

func decodeCapabilityResponse(payload []byte, result any) error {
	var response capabilityResponseV2
	if err := json.Unmarshal(payload, &response); err != nil {
		return &ABIError{Operation: "response_decode", Reason: "invalid capability response JSON"}
	}
	if !response.OK {
		if response.Error == nil {
			return &Error{Code: "host_failure", Message: "capability response did not include an error"}
		}
		return response.Error
	}
	if result != nil && len(response.Result) != 0 {
		if err := json.Unmarshal(response.Result, result); err != nil {
			return &ABIError{Operation: "result_decode", Reason: "invalid capability result JSON"}
		}
	}
	return nil
}

type combinedError struct {
	cause   error
	cleanup *ABIError
}

func (e *combinedError) Error() string { return e.cause.Error() + "; " + e.cleanup.Error() }
func (e *combinedError) Unwrap() error { return e.cause }

// AsCapabilityError preserves the structured host failure when response-drop
// cleanup also fails and CallHost must return both errors.
func AsCapabilityError(err error) (*Error, bool) {
	if capabilityErr, ok := err.(*Error); ok {
		return capabilityErr, true
	}
	if combined, ok := err.(*combinedError); ok {
		return AsCapabilityError(combined.cause)
	}
	return nil, false
}
