package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type fakeGuacdHost struct {
	settings      json.RawMessage
	targets       json.RawMessage
	args          []string
	ready         []string
	readyOpcode   string
	incoming      []byte
	outgoing      []byte
	instructions  []guacInstruction
	events        []json.RawMessage
	openParams    map[string]any
	openCount     int
	closeCount    int
	storageWrites int
}

func (host *fakeGuacdHost) call(method string, params any, result any) error {
	arguments, _ := params.(map[string]any)
	switch method {
	case "storage.get":
		key, _ := arguments["key"].(string)
		stored := host.settings
		if key == "targets" {
			stored = host.targets
		}
		if result != nil && stored != nil {
			return assignJSON(result, stored)
		}
		return nil
	case "storage.set":
		host.storageWrites++
		value, err := json.Marshal(arguments["value"])
		if err == nil {
			key, _ := arguments["key"].(string)
			if key == "targets" {
				host.targets = value
			} else {
				host.settings = value
			}
		}
		return err
	case "events.publish":
		data, err := json.Marshal(arguments["data"])
		if err == nil {
			host.events = append(host.events, data)
		}
		return err
	case "network.stream.open":
		host.openCount++
		host.openParams = arguments
		return assignJSON(result, []byte(`{"id":"owned-stream"}`))
	case "network.stream.close":
		host.closeCount++
		return nil
	case "network.stream.write":
		encoded, _ := arguments["data"].(string)
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return err
		}
		host.outgoing = append(host.outgoing, data...)
		for {
			instruction, used, ok := decodeTestInstruction(host.outgoing)
			if !ok {
				break
			}
			host.instructions = append(host.instructions, instruction)
			host.outgoing = host.outgoing[used:]
			switch instruction.Opcode {
			case "select":
				host.incoming = append(host.incoming, encodeTestInstruction("args", host.args...)...)
			case "connect":
				opcode := host.readyOpcode
				if opcode == "" {
					opcode = "ready"
				}
				host.incoming = append(host.incoming, encodeTestInstruction(opcode, host.ready...)...)
			}
		}
		return nil
	case "network.stream.read":
		maximum := 0
		switch value := arguments["maxBytes"].(type) {
		case int:
			maximum = value
		case float64:
			maximum = int(value)
		}
		offset := 0
		if value, ok := arguments["offset"].(int); ok {
			offset = value
		}
		peek, _ := arguments["peek"].(bool)
		available := len(host.incoming)
		if peek {
			available -= offset
		} else {
			offset = 0
		}
		if available < 0 {
			available = 0
		}
		count := maximum
		if count > available {
			count = available
		}
		chunk := append([]byte(nil), host.incoming[offset:offset+count]...)
		if !peek {
			host.incoming = host.incoming[count:]
		}
		return assignJSON(result, mustMarshalTest(map[string]any{"data": base64.StdEncoding.EncodeToString(chunk), "eof": len(host.incoming) == 0 && len(chunk) == 0}))
	default:
		return fmt.Errorf("unexpected capability %s", method)
	}
}

func installFakeHost(t *testing.T, host *fakeGuacdHost) {
	t.Helper()
	original := callHost
	callHost = host.call
	t.Cleanup(func() { callHost = original })
}

func TestRDPHandshakeMapsReturnedArgsAndNegotiatesVersion(t *testing.T) {
	host := &fakeGuacdHost{
		args:  []string{"VERSION_9_0_0", "password", "hostname", "server-layout", "security", "port", "ignore-cert", "disable-copy", "disable-paste", "resize-method", "timezone", "unknown"},
		ready: []string{"guac-connection"},
	}
	installFakeHost(t, host)
	plugin := newPlugin()
	targetID := saveRDPTestTarget(t, plugin, json.RawMessage(`{"host":"rdp-target","port":3390,"username":"saved-user","domain":"WORK","securityMode":"nla","serverLayout":"hu-hu-qwertz","resizeMethod":"display-update","certificatePolicy":"ignore","copy":false,"paste":true,"clipboardNormalization":"unix","performanceProfile":"quality","timeoutSeconds":23,"timeZone":"Europe/Budapest"}`))
	request := json.RawMessage(fmt.Sprintf(`{"targetId":%q,"credentials":{"password":"do-not-leak"},"client":{"width":1280,"height":800,"dpi":96,"timeZone":"America/New_York","clientName":"RunPilot","imageMimetypes":["image/png"]}}`, targetID))
	result, failure := plugin.handle("rdp.session.open", request)
	if failure != nil {
		t.Fatal(failure)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "do-not-leak") {
		t.Fatalf("credential leaked from response: %s", encoded)
	}
	wantOpcodes := []string{"select", "size", "audio", "video", "image", "timezone", "name", "connect"}
	gotOpcodes := make([]string, len(host.instructions))
	for index, instruction := range host.instructions {
		gotOpcodes[index] = instruction.Opcode
	}
	if !reflect.DeepEqual(gotOpcodes, wantOpcodes) {
		t.Fatalf("instruction sequence=%v", gotOpcodes)
	}
	wantConnect := []string{"VERSION_1_5_0", "do-not-leak", "rdp-target", "hu-hu-qwertz", "nla", "3390", "true", "true", "false", "display-update", "Europe/Budapest", ""}
	if got := host.instructions[len(host.instructions)-1].Args; !reflect.DeepEqual(got, wantConnect) {
		t.Fatalf("connect args=%q want=%q", got, wantConnect)
	}
	if !reflect.DeepEqual(host.instructions[1].Args, []string{"1280", "800", "96"}) || !reflect.DeepEqual(host.instructions[5].Args, []string{"Europe/Budapest"}) || !reflect.DeepEqual(host.instructions[6].Args, []string{"RunPilot"}) {
		t.Fatalf("client handshake instructions=%#v", host.instructions)
	}
	if host.closeCount != 0 || len(plugin.sessions) != 1 {
		t.Fatalf("ready stream was not retained: close=%d sessions=%#v", host.closeCount, plugin.sessions)
	}
	sessionID := result.(map[string]any)["session"].(session).ID
	diagnosticResult, failure := plugin.handle("rdp.session.diagnostics", json.RawMessage(fmt.Sprintf(`{"id":%q}`, sessionID)))
	if failure != nil {
		t.Fatal(failure)
	}
	diagnosticLog := diagnosticResult.(map[string]any)["log"].(string)
	if !strings.Contains(diagnosticLog, "target endpoint rdp-target:3390") || !strings.Contains(diagnosticLog, "password supplied") || !strings.Contains(diagnosticLog, "sent select rdp") || !strings.Contains(diagnosticLog, "received args") || !strings.Contains(diagnosticLog, "received ready") || strings.Contains(diagnosticLog, "do-not-leak") || strings.Contains(diagnosticLog, "saved-user") || strings.Contains(diagnosticLog, "WORK") {
		t.Fatalf("unexpected session diagnostics: %q", diagnosticLog)
	}
	for _, event := range host.events {
		if strings.Contains(string(event), "do-not-leak") || strings.Contains(string(event), "saved-user") || strings.Contains(string(event), "WORK") {
			t.Fatalf("diagnostic event leaked password: %s", event)
		}
	}
	if _, failure := plugin.handle("rdp.targets.delete", json.RawMessage(fmt.Sprintf(`{"id":%q}`, targetID))); failure == nil || failure.Code != "failed_precondition" {
		t.Fatalf("active target deletion was not rejected: %+v", failure)
	}
	if err := plugin.event("network.stream.closed", json.RawMessage(`{"id":"owned-stream"}`)); err != nil || len(plugin.sessions) != 0 {
		t.Fatalf("stream close event cleanup: sessions=%#v err=%v", plugin.sessions, err)
	}
}

func TestRDPHandshakeSupportsInteractiveLoginAndVersionGates(t *testing.T) {
	host := &fakeGuacdHost{args: []string{"VERSION_1_0_0", "hostname", "username", "password", "domain"}, ready: []string{"connection"}}
	installFakeHost(t, host)
	plugin := newPlugin()
	targetID := saveRDPTestTarget(t, plugin, json.RawMessage(`{"host":"target","username":"configured","domain":"EXAMPLE"}`))
	_, failure := plugin.handle("rdp.session.open", json.RawMessage(fmt.Sprintf(`{"targetId":%q,"client":{"timeZone":"UTC","clientName":"browser"}}`, targetID)))
	if failure != nil {
		t.Fatal(failure)
	}
	connect := host.instructions[len(host.instructions)-1]
	if !reflect.DeepEqual(connect.Args, []string{"VERSION_1_0_0", "target", "configured", "", "EXAMPLE"}) {
		t.Fatalf("credentialless connect args=%q", connect.Args)
	}
	for _, instruction := range host.instructions {
		if instruction.Opcode == "timezone" || instruction.Opcode == "name" {
			t.Fatalf("unsupported version received gated instruction %#v", instruction)
		}
	}
}

func TestRDPRejectsMissingNLASecretAndInvalidCandidate(t *testing.T) {
	host := &fakeGuacdHost{}
	installFakeHost(t, host)
	plugin := newPlugin()
	targetID := saveRDPTestTarget(t, plugin, json.RawMessage(`{"host":"target","securityMode":"nla"}`))
	if _, failure := plugin.handle("rdp.session.open", json.RawMessage(fmt.Sprintf(`{"targetId":%q}`, targetID))); failure == nil || failure.Code != "failed_precondition" {
		t.Fatalf("missing NLA credentials accepted: %+v", failure)
	}
	if host.openCount != 0 {
		t.Fatalf("NLA validation opened %d streams", host.openCount)
	}
	priorWrites := host.storageWrites
	if _, failure := plugin.handle("rdp.settings.test", json.RawMessage(`{"host":"https://evil","port":4822,"connectTimeoutSeconds":5}`)); failure == nil || failure.Code != "invalid_argument" {
		t.Fatalf("invalid candidate accepted: %+v", failure)
	}
	if host.openCount != 0 || host.storageWrites != priorWrites {
		t.Fatalf("invalid candidate had side effects: opens=%d writes=%d", host.openCount, host.storageWrites)
	}
}

func TestRDPTargetCRUDValidationAndActiveSessionProtection(t *testing.T) {
	host := &fakeGuacdHost{args: []string{"VERSION_1_5_0", "hostname"}, ready: []string{"connection"}}
	installFakeHost(t, host)
	plugin := newPlugin()
	created, failure := plugin.handle("rdp.targets.save", json.RawMessage(`{"target":{"name":" Workstation ","options":{"host":"desktop.example","username":"operator","domain":"WORK","port":3390,"securityMode":"automatic","certificatePolicy":"validate","clipboardNormalization":"preserve","performanceProfile":"balanced","timeoutSeconds":12}}}`))
	if failure != nil {
		t.Fatal(failure)
	}
	target := created.(map[string]any)["target"].(rdpTarget)
	if target.ID == "" || target.Name != "Workstation" || target.Options.Port != 3390 {
		t.Fatalf("created target=%#v", target)
	}
	if bytes.Contains(host.targets, []byte("password")) {
		t.Fatalf("stored target contains password field: %s", host.targets)
	}
	plugin = newPlugin()
	listed, failure := plugin.handle("rdp.targets.list", nil)
	if failure != nil || len(listed.(map[string]any)["targets"].([]rdpTarget)) != 1 {
		t.Fatalf("list result=%v failure=%v", listed, failure)
	}
	got, failure := plugin.handle("rdp.targets.get", json.RawMessage(fmt.Sprintf(`{"id":%q}`, target.ID)))
	if failure != nil || got.(map[string]any)["target"].(rdpTarget).ID != target.ID {
		t.Fatalf("get result=%v failure=%v", got, failure)
	}
	updated, failure := plugin.handle("rdp.targets.save", json.RawMessage(fmt.Sprintf(`{"target":{"id":%q,"name":"Edited","options":{"host":"desktop.example","port":3391}}}`, target.ID)))
	if failure != nil || updated.(map[string]any)["target"].(rdpTarget).Name != "Edited" {
		t.Fatalf("update result=%v failure=%v", updated, failure)
	}
	if _, failure := plugin.handle("rdp.targets.save", json.RawMessage(`{"target":{"name":"Bad secret","options":{"host":"desktop.example","password":"never-accepted"}}}`)); failure == nil || failure.Code != "invalid_argument" {
		t.Fatalf("target password field accepted: %+v", failure)
	}
	if _, failure := plugin.handle("rdp.targets.get", json.RawMessage(`{"id":"missing"}`)); failure == nil || failure.Code != "not_found" {
		t.Fatalf("unknown target get error=%+v", failure)
	}
	if _, failure := plugin.handle("rdp.targets.delete", json.RawMessage(`{"id":"missing"}`)); failure == nil || failure.Code != "not_found" {
		t.Fatalf("unknown target delete error=%+v", failure)
	}
	if _, failure := plugin.handle("rdp.session.open", json.RawMessage(fmt.Sprintf(`{"targetId":%q}`, target.ID))); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := plugin.handle("rdp.targets.delete", json.RawMessage(fmt.Sprintf(`{"id":%q}`, target.ID))); failure == nil || failure.Code != "failed_precondition" {
		t.Fatalf("active target delete error=%+v", failure)
	}
	for id := range plugin.sessions {
		if _, failure := plugin.handle("rdp.session.close", json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))); failure != nil {
			t.Fatal(failure)
		}
	}
	if _, failure := plugin.handle("rdp.targets.delete", json.RawMessage(fmt.Sprintf(`{"id":%q}`, target.ID))); failure != nil {
		t.Fatalf("delete after session close: %v", failure)
	}
}

func TestRDPSettingsTestDoesNotPersistAndOnlyStoresGuacdConfig(t *testing.T) {
	host := &fakeGuacdHost{}
	installFakeHost(t, host)
	plugin := newPlugin()
	candidate := json.RawMessage(`{"host":"localhost","port":5001,"tls":true,"connectTimeoutSeconds":7}`)
	result, failure := plugin.handle("rdp.settings.test", candidate)
	if failure != nil || result.(map[string]any)["available"] != true {
		t.Fatalf("settings test result=%v failure=%v", result, failure)
	}
	if host.storageWrites != 0 || host.closeCount != 1 || host.openCount != 1 {
		t.Fatalf("test persisted or retained stream: writes=%d closes=%d opens=%d", host.storageWrites, host.closeCount, host.openCount)
	}
	if host.openParams["host"] != "localhost" || host.openParams["port"] != 5001 || host.openParams["connectTimeoutSeconds"] != 7 || !host.openParams["tls"].(map[string]any)["enabled"].(bool) {
		t.Fatalf("candidate was not passed to network capability: %#v", host.openParams)
	}
	if _, failure := plugin.handle("rdp.settings.set", candidate); failure != nil {
		t.Fatal(failure)
	}
	if bytes.Contains(host.settings, []byte("password")) || !bytes.Contains(host.settings, []byte("localhost")) {
		t.Fatalf("stored settings contain unexpected data: %s", host.settings)
	}
}

func TestRDPHandshakeErrorsDoNotExposePassword(t *testing.T) {
	host := &fakeGuacdHost{args: []string{"password", "hostname"}, readyOpcode: "error", ready: []string{"secret-in-server-error"}}
	installFakeHost(t, host)
	plugin := newPlugin()
	targetID := saveRDPTestTarget(t, plugin, json.RawMessage(`{"host":"target"}`))
	_, failure := plugin.handle("rdp.session.open", json.RawMessage(fmt.Sprintf(`{"targetId":%q,"diagnosticId":"failed-open","credentials":{"password":"top-secret"}}`, targetID)))
	if failure == nil || strings.Contains(failure.Message, "top-secret") || strings.Contains(failure.Message, "secret-in-server-error") {
		t.Fatalf("unsafe handshake error: %+v", failure)
	}
	if host.closeCount != 1 || len(plugin.sessions) != 0 {
		t.Fatalf("failed handshake retained session: close=%d sessions=%#v", host.closeCount, plugin.sessions)
	}
	diagnosticResult, diagnosticFailure := plugin.handle("rdp.session.diagnostics", json.RawMessage(`{"diagnosticId":"failed-open"}`))
	if diagnosticFailure != nil {
		t.Fatal(diagnosticFailure)
	}
	diagnosticLog := diagnosticResult.(map[string]any)["log"].(string)
	if !strings.Contains(diagnosticLog, "opening guacd stream") || !strings.Contains(diagnosticLog, "Guacamole negotiation failed") || strings.Contains(diagnosticLog, "top-secret") || strings.Contains(diagnosticLog, "secret-in-server-error") {
		t.Fatalf("unsafe failed-open diagnostics: %q", diagnosticLog)
	}
}

func saveRDPTestTarget(t *testing.T, plugin *plugin, options json.RawMessage) string {
	t.Helper()
	request, err := json.Marshal(struct {
		Target rdpTarget `json:"target"`
	}{Target: rdpTarget{Name: "Test target", Options: mustDecodeOptions(t, options)}})
	if err != nil {
		t.Fatal(err)
	}
	result, failure := plugin.handle("rdp.targets.save", request)
	if failure != nil {
		t.Fatal(failure)
	}
	return result.(map[string]any)["target"].(rdpTarget).ID
}

func mustDecodeOptions(t *testing.T, raw json.RawMessage) rdpOptions {
	t.Helper()
	var options rdpOptions
	if err := json.Unmarshal(raw, &options); err != nil {
		t.Fatal(err)
	}
	return options
}

func TestRDPCodecRejectsMalformedAndOversizedInstructions(t *testing.T) {
	for _, raw := range [][]byte{[]byte("x."), []byte("9."), []byte("1048577."), []byte("1.a?")} {
		stream := &guacStream{id: "test"}
		original := callHost
		callHost = func(method string, params any, result any) error {
			if method != "network.stream.read" {
				return fmt.Errorf("unexpected %s", method)
			}
			args := params.(map[string]any)
			count := args["maxBytes"].(int)
			if count > len(raw) {
				count = len(raw)
			}
			chunk := raw[:count]
			raw = raw[count:]
			return assignJSON(result, mustMarshalTest(map[string]any{"data": base64.StdEncoding.EncodeToString(chunk)}))
		}
		_, failure := stream.readInstruction()
		callHost = original
		if failure == nil {
			t.Fatalf("malformed instruction accepted: %q", raw)
		}
	}
}

func encodeTestInstruction(opcode string, args ...string) []byte {
	var output strings.Builder
	for index, value := range append([]string{opcode}, args...) {
		if index > 0 {
			output.WriteByte(',')
		}
		output.WriteString(strconv.Itoa(len(value)))
		output.WriteByte('.')
		output.WriteString(value)
	}
	output.WriteByte(';')
	return []byte(output.String())
}

func decodeTestInstruction(data []byte) (guacInstruction, int, bool) {
	var values []string
	for offset := 0; offset < len(data); {
		start := offset
		for offset < len(data) && data[offset] >= '0' && data[offset] <= '9' {
			offset++
		}
		if offset == len(data) || data[offset] != '.' || offset == start {
			return guacInstruction{}, 0, false
		}
		length, err := strconv.Atoi(string(data[start:offset]))
		if err != nil {
			return guacInstruction{}, 0, false
		}
		offset++
		if length > len(data)-offset {
			return guacInstruction{}, 0, false
		}
		values = append(values, string(data[offset:offset+length]))
		offset += length
		if offset == len(data) {
			return guacInstruction{}, 0, false
		}
		separator := data[offset]
		offset++
		if separator == ';' {
			return guacInstruction{Opcode: values[0], Args: values[1:]}, offset, len(values) > 0
		}
		if separator != ',' {
			return guacInstruction{}, 0, false
		}
	}
	return guacInstruction{}, 0, false
}

func assignJSON(target any, data []byte) error { return json.Unmarshal(data, target) }
func mustMarshalTest(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
