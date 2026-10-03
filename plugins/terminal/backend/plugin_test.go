package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestTerminalSettingsPersistAndSelectCommand(t *testing.T) {
	original := callHost
	t.Cleanup(func() { callHost = original })
	var stored json.RawMessage
	var created map[string]any
	callHost = func(method string, params any, result any) error {
		switch method {
		case "storage.get":
			if params.(map[string]any)["key"] != "settings" {
				t.Fatal("unexpected storage key")
			}
			*result.(*json.RawMessage) = stored
		case "storage.set":
			var err error
			stored, err = json.Marshal(params.(map[string]any)["value"])
			return err
		case "system.status":
			return json.Unmarshal([]byte(`{"os":"linux"}`), result)
		case "process.session.create":
			created = params.(map[string]any)
			return json.Unmarshal([]byte(`{"id":"test-session","state":"running"}`), result)
		default:
			t.Fatalf("unexpected capability %s", method)
		}
		return nil
	}
	plugin := newPlugin()
	if _, failure := plugin.handle("terminal.settings.set", json.RawMessage(`{"command":" /bin/bash ","args":["--noprofile","--norc"]}`)); failure != nil {
		t.Fatal(failure)
	}
	restarted := newPlugin()
	out, failure := restarted.handle("terminal.settings.get", nil)
	if failure != nil {
		t.Fatal(failure)
	}
	settings := out.(map[string]any)["settings"].(terminalSettings)
	if settings.Command != "/bin/bash" || !reflect.DeepEqual(settings.Args, []string{"--noprofile", "--norc"}) {
		t.Fatalf("settings did not survive restart: %+v", settings)
	}
	if _, failure := restarted.open(nil); failure != nil {
		t.Fatal(failure)
	}
	if created["command"] != settings.Command || !reflect.DeepEqual(created["args"], settings.Args) {
		t.Fatalf("configured command was not used: %#v", created)
	}
	if _, failure := restarted.setSettings(json.RawMessage(`{"command":"","args":[]}`)); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := restarted.open(nil); failure != nil {
		t.Fatal(failure)
	}
	if created["command"] != "/bin/sh" || !reflect.DeepEqual(created["args"], defaultShellArgs("linux")) {
		t.Fatalf("default shell behavior changed: %#v", created)
	}
}

func TestTerminalSettingsTestUsesUnsavedCommandAndClosesOnlyProbe(t *testing.T) {
	original := callHost
	t.Cleanup(func() { callHost = original })
	plugin := newPlugin()
	plugin.sessions["existing"] = session{ID: "existing", State: "running"}
	var methods []string
	callHost = func(method string, params any, result any) error {
		methods = append(methods, method)
		switch method {
		case "system.status":
			return json.Unmarshal([]byte(`{"os":"linux"}`), result)
		case "process.session.create":
			request := params.(map[string]any)
			if request["command"] != "/bin/bash" || !reflect.DeepEqual(request["args"], []string{"--noprofile", "--norc"}) {
				t.Fatalf("probe did not use candidate settings: %#v", request)
			}
			return json.Unmarshal([]byte(`{"id":"probe","state":"running"}`), result)
		case "process.session.terminate":
			request := params.(map[string]any)
			if request["id"] != "probe" || request["force"] != true {
				t.Fatalf("probe closed the wrong session: %#v", request)
			}
			return nil
		default:
			t.Fatalf("probe reached unexpected capability %s", method)
			return nil
		}
	}
	if _, failure := plugin.handle("terminal.settings.test", json.RawMessage(`{"command":" /bin/bash ","args":["--noprofile","--norc"]}`)); failure != nil {
		t.Fatal(failure)
	}
	if !reflect.DeepEqual(methods, []string{"system.status", "process.session.create", "process.session.terminate"}) || len(plugin.sessions) != 1 || plugin.sessions["existing"].State != "running" {
		t.Fatalf("probe mutated storage or existing sessions: methods=%v sessions=%v", methods, plugin.sessions)
	}
}

func TestTerminalSettingsTestRejectsInvalidCandidate(t *testing.T) {
	for _, request := range []string{`{"command":"","args":["-l"]}`, `{"command":"bad\u0000command"}`, `{"command":"/bin/sh","args":["bad\u0000arg"]}`, `{`} {
		t.Run(request, func(t *testing.T) {
			original := callHost
			t.Cleanup(func() { callHost = original })
			callHost = func(method string, params any, result any) error {
				t.Fatalf("invalid candidate reached capability %s", method)
				return nil
			}
			if _, failure := newPlugin().handle("terminal.settings.test", json.RawMessage(request)); failure == nil || failure.Code != "invalid_argument" {
				t.Fatalf("invalid test candidate accepted: %+v", failure)
			}
		})
	}
}

func TestTerminalSettingsTestReportsLaunchAndCleanupFailures(t *testing.T) {
	for _, failedMethod := range []string{"process.session.create", "process.session.terminate"} {
		t.Run(failedMethod, func(t *testing.T) {
			original := callHost
			t.Cleanup(func() { callHost = original })
			callHost = func(method string, params any, result any) error {
				if method == failedMethod {
					return errors.New("probe failure")
				}
				switch method {
				case "system.status":
					return json.Unmarshal([]byte(`{"os":"linux"}`), result)
				case "process.session.create":
					return json.Unmarshal([]byte(`{"id":"probe","state":"running"}`), result)
				default:
					t.Fatalf("unexpected capability %s", method)
					return nil
				}
			}
			plugin := newPlugin()
			if _, failure := plugin.handle("terminal.settings.test", json.RawMessage(`{"command":"/bin/bash"}`)); failure == nil || failure.Message != "probe failure" {
				t.Fatalf("probe failure not reported: %+v", failure)
			}
			if failedMethod == "process.session.terminate" && plugin.sessions["probe"].State != "running" {
				t.Fatal("failed cleanup discarded the session needed for shutdown cleanup")
			}
		})
	}
}

func TestTerminalSettingsRejectInvalidAndUnreadableData(t *testing.T) {
	for _, request := range []string{`{"command":"","args":["-l"]}`, `{"command":"bad\u0000command"}`, `{"command":"/bin/sh","args":["bad\u0000arg"]}`, `{`} {
		t.Run(request, func(t *testing.T) {
			original := callHost
			t.Cleanup(func() { callHost = original })
			callHost = func(method string, params any, result any) error {
				if method != "storage.get" {
					t.Fatalf("invalid request reached %s", method)
				}
				return nil
			}
			if _, failure := newPlugin().setSettings(json.RawMessage(request)); failure == nil || failure.Code != "invalid_argument" {
				t.Fatalf("invalid settings accepted: %+v", failure)
			}
		})
	}
	for _, stored := range []string{`{`, `{"version":2}`, `{"version":1,"command":"","args":["-l"]}`} {
		t.Run(stored, func(t *testing.T) {
			original := callHost
			t.Cleanup(func() { callHost = original })
			callHost = func(method string, params any, result any) error {
				if method != "storage.get" {
					t.Fatalf("unreadable settings reached %s", method)
				}
				*result.(*json.RawMessage) = json.RawMessage(stored)
				return nil
			}
			if _, failure := newPlugin().setSettings(json.RawMessage(`{"command":"/bin/bash"}`)); failure == nil {
				t.Fatal("unreadable settings were overwritten")
			}
		})
	}
}

func TestConfiguredWindowsCommandDoesNotFallback(t *testing.T) {
	original := callHost
	t.Cleanup(func() { callHost = original })
	creates := 0
	callHost = func(method string, params any, result any) error {
		switch method {
		case "system.status":
			return json.Unmarshal([]byte(`{"os":"windows"}`), result)
		case "storage.get":
			*result.(*json.RawMessage) = json.RawMessage(`{"version":1,"command":"cmd.exe","args":["/Q"]}`)
			return nil
		case "process.session.create":
			creates++
			return errors.New("configured command failed")
		}
		t.Fatalf("unexpected capability %s", method)
		return nil
	}
	if _, failure := newPlugin().open(nil); failure == nil || creates != 1 {
		t.Fatalf("configured Windows command silently fell back: failure=%+v creates=%d", failure, creates)
	}
}
