package core

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

const terminalPluginID = "terminal"

func packageTerminalForTest(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "plugins", terminalPluginID)
	path := filepath.Join(t.TempDir(), "terminal.rpplugin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	err = filepath.Walk(root, func(name string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		entry, err := archive.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		input, err := os.Open(name)
		if err != nil {
			return err
		}
		defer input.Close()
		_, err = io.Copy(entry, input)
		return err
	})
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func terminalCall(t *testing.T, c *Controller, method string, params any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(params)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, protocolErr := c.PluginCall(ctx, terminalPluginID, method, raw)
	if protocolErr != nil {
		t.Fatalf("%s: %s", method, protocolErr.Message)
	}
	var out map[string]any
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("%s result %s: %v", method, result, err)
	}
	if failure, ok := out["error"]; ok {
		t.Fatalf("%s failed: %v", method, failure)
	}
	return out
}

func TestTerminalPluginInstallEnableRestartAndInteractiveSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("interactive shell integration uses the Linux /bin/sh fixture")
	}
	dataDir := t.TempDir()
	if _, err := plugins.InstallPackage(filepath.Join(dataDir, "plugins"), packageTerminalForTest(t), ""); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetPluginEnabled(terminalPluginID, true); err != nil {
		t.Fatal(err)
	}
	if !c.Plugins().RestartRequired() {
		t.Fatal("enabling plugin did not require restart")
	}
	c.Close()
	c, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	loaded := false
	for _, status := range c.Plugins().Statuses() {
		if status.Manifest.ID == terminalPluginID {
			loaded = status.Loaded && status.Enabled
		}
	}
	if !loaded {
		t.Fatal("terminal backend was not activated after restart")
	}
	foundEntry := false
	for _, ext := range c.Plugins().FrontendExtensions() {
		if ext.ID == terminalPluginID {
			foundEntry = true
		}
	}
	if !foundEntry {
		t.Fatal("terminal frontend was not activated after restart")
	}

	events, unsubscribe := c.SubscribePluginEvents()
	defer unsubscribe()
	opened := terminalCall(t, c, "terminal.open", map[string]any{"rows": 24, "columns": 80})
	session := opened["session"].(map[string]any)
	id := session["id"].(string)
	first := awaitTerminalOutput(t, events, id)
	if first.sequence != 1 || len(first.bytes) == 0 {
		t.Fatalf("first terminal event was not an initial prompt/output chunk: %+v", first)
	}
	settingsBefore, _ := json.Marshal(terminalCall(t, c, "terminal.settings.get", nil))
	terminalCall(t, c, "terminal.settings.test", map[string]any{"command": "/bin/sh", "args": []string{"-i"}})
	settingsAfter, _ := json.Marshal(terminalCall(t, c, "terminal.settings.get", nil))
	if string(settingsBefore) != string(settingsAfter) {
		t.Fatal("testing terminal settings changed the saved settings")
	}
	terminalCall(t, c, "terminal.resize", map[string]any{"id": id, "rows": 31, "columns": 101})
	terminalCall(t, c, "terminal.status", map[string]any{"id": id})
	input := base64.StdEncoding.EncodeToString([]byte("head -c 20000 /dev/zero | tr '\\000' X; printf '\\nRP_END_1\\n'; sleep 0.1; printf 'RP_LATE_1\\n'\n"))
	terminalCall(t, c, "terminal.write", map[string]any{"id": id, "data": input})
	var output strings.Builder
	output.Write(first.bytes)
	previousSequence := first.sequence
	chunks := 1
	seenLate := false
	deadline := time.After(8 * time.Second)
	for !seenLate {
		select {
		case event := <-events:
			if event.Plugin != terminalPluginID || event.Event != "process.session.output" {
				continue
			}
			chunk := decodeTerminalOutput(t, event.Data)
			if chunk.id == id {
				if chunk.sequence != previousSequence+1 {
					t.Fatalf("output sequence %d followed %d", chunk.sequence, previousSequence)
				}
				previousSequence = chunk.sequence
				chunks++
				output.Write(chunk.bytes)
				seenLate = strings.Contains(output.String(), "RP_LATE_1")
			}
		case <-deadline:
			t.Fatal("timed out waiting for terminal output marker")
		}
	}
	if chunks < 3 || strings.Count(output.String(), "RP_END_1") != 1 || !strings.Contains(output.String(), "RP_LATE_1") || strings.Index(output.String(), "RP_END_1") > strings.Index(output.String(), "RP_LATE_1") {
		t.Fatalf("multi-chunk output was missing, duplicated, or out of order (chunks=%d)", chunks)
	}

	input = base64.StdEncoding.EncodeToString([]byte("printf 'RP_NONZERO_7\\n'; exit 7\n"))
	terminalCall(t, c, "terminal.write", map[string]any{"id": id, "data": input})
	exit := awaitTerminalExit(t, events, id)
	if exit.Reason != "exited" || exit.ExitCode == nil || *exit.ExitCode != 7 {
		t.Fatalf("non-zero shell completion: %+v", exit)
	}
	duplicateWindow := time.NewTimer(100 * time.Millisecond)
	for {
		select {
		case event := <-events:
			if event.Plugin == terminalPluginID && event.Event == "process.session.exit" {
				var duplicate struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(event.Data, &duplicate)
				if duplicate.ID == id {
					t.Fatal("session completion was delivered more than once")
				}
			}
		case <-duplicateWindow.C:
			goto completionChecked
		}
	}

completionChecked:

	// Opening another shell after completion must create fresh state and receive
	// only output tagged for the new opaque session ID.
	opened = terminalCall(t, c, "terminal.open", map[string]any{"rows": 24, "columns": 80})
	secondID := opened["session"].(map[string]any)["id"].(string)
	if secondID == id {
		t.Fatal("reopened terminal reused the previous session ID")
	}
	_ = awaitTerminalOutput(t, events, secondID)
	input = base64.StdEncoding.EncodeToString([]byte("printf 'RP_SECOND_SESSION\\n'\n"))
	terminalCall(t, c, "terminal.write", map[string]any{"id": secondID, "data": input})
	var secondOutput strings.Builder
	deadline = time.After(8 * time.Second)
	for !strings.Contains(secondOutput.String(), "RP_SECOND_SESSION") {
		select {
		case event := <-events:
			if event.Plugin == terminalPluginID && event.Event == "process.session.output" {
				chunk := decodeTerminalOutput(t, event.Data)
				if chunk.id == secondID {
					secondOutput.Write(chunk.bytes)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for second terminal session output")
		}
	}
	terminalCall(t, c, "terminal.close", map[string]any{"id": secondID, "force": true})
	waitForPluginSessionExit(t, c, secondID, "terminated")
	third := terminalCall(t, c, "terminal.open", map[string]any{"rows": 24, "columns": 80})
	thirdID := third["session"].(map[string]any)["id"].(string)
	if thirdID == id || thirdID == secondID {
		t.Fatal("session ID leaked after termination")
	}
	terminalCall(t, c, "terminal.close", map[string]any{"id": thirdID, "force": true})
	waitForPluginSessionExit(t, c, thirdID, "terminated")

	// Saturate the shared application event subscriber. Output publication must
	// fail back through ABI v2 so the core session ends explicitly with io_error.
	fourth := terminalCall(t, c, "terminal.open", map[string]any{"rows": 24, "columns": 80})
	fourthID := fourth["session"].(map[string]any)["id"].(string)
	_ = awaitTerminalOutput(t, events, fourthID)
	drainTerminalEvents(events, 150*time.Millisecond)
	for i := 0; i < cap(events); i++ {
		c.publishPluginEvent("other", "test.noise", json.RawMessage(`{}`))
	}
	input = base64.StdEncoding.EncodeToString([]byte("printf 'RP_OVERFLOW_TRIGGER\\n'\n"))
	terminalCall(t, c, "terminal.write", map[string]any{"id": fourthID, "data": input})
	waitForPluginSessionExit(t, c, fourthID, "io_error")
	status := terminalCall(t, c, "terminal.status", map[string]any{"id": fourthID})
	if status["reason"] != "io_error" || status["state"] != "exited" {
		t.Fatalf("event overflow was not visible as an explicit terminal error: %#v", status)
	}
}

func TestTerminalPluginSettingsPersistAcrossRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("configured shell integration uses Linux /bin/sh")
	}
	dataDir := t.TempDir()
	if _, err := plugins.InstallPackage(filepath.Join(dataDir, "plugins"), packageTerminalForTest(t), ""); err != nil {
		t.Fatal(err)
	}
	controller, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.SetPluginEnabled(terminalPluginID, true); err != nil {
		controller.Close()
		t.Fatal(err)
	}
	controller.Close()
	controller, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	settings := terminalCall(t, controller, "terminal.settings.get", nil)
	if settings["defaultCommand"] != "/bin/sh" || settings["settings"].(map[string]any)["command"] != "" {
		controller.Close()
		t.Fatalf("unexpected defaults: %#v", settings)
	}
	terminalCall(t, controller, "terminal.settings.set", map[string]any{"command": "/bin/sh", "args": []string{"-i"}})
	controller.Close()
	controller, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	settings = terminalCall(t, controller, "terminal.settings.get", nil)
	saved := settings["settings"].(map[string]any)
	if saved["command"] != "/bin/sh" || len(saved["args"].([]any)) != 1 || saved["args"].([]any)[0] != "-i" {
		t.Fatalf("settings did not survive restart: %#v", settings)
	}
	events, unsubscribe := controller.SubscribePluginEvents()
	defer unsubscribe()
	opened := terminalCall(t, controller, "terminal.open", map[string]any{"rows": 24, "columns": 80})
	session := opened["session"].(map[string]any)
	if session["command"] != "/bin/sh" {
		t.Fatalf("configured command was not used: %#v", session)
	}
	id := session["id"].(string)
	_ = awaitTerminalOutput(t, events, id)
	terminalCall(t, controller, "terminal.close", map[string]any{"id": id, "force": true})
	waitForPluginSessionExit(t, controller, id, "terminated")
}

func drainTerminalEvents(events <-chan plugins.Event, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case <-events:
		case <-timer.C:
			return
		}
	}
}

type terminalOutput struct {
	id       string
	sequence int
	bytes    []byte
}

func decodeTerminalOutput(t *testing.T, raw json.RawMessage) terminalOutput {
	t.Helper()
	var event struct {
		ID       string `json:"id"`
		Sequence int    `json:"sequence"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(event.Data)
	if err != nil {
		t.Fatal(err)
	}
	return terminalOutput{id: event.ID, sequence: event.Sequence, bytes: decoded}
}
func awaitTerminalOutput(t *testing.T, events <-chan plugins.Event, id string) terminalOutput {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Plugin == terminalPluginID && event.Event == "process.session.output" {
				chunk := decodeTerminalOutput(t, event.Data)
				if chunk.id == id {
					return chunk
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for initial output for %s", id)
		}
	}
}
func awaitTerminalExit(t *testing.T, events <-chan plugins.Event, id string) struct {
	Reason   string `json:"reason"`
	ExitCode *int   `json:"exitCode"`
} {
	t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Plugin == terminalPluginID && event.Event == "process.session.exit" {
				var got struct {
					ID       string `json:"id"`
					Reason   string `json:"reason"`
					ExitCode *int   `json:"exitCode"`
				}
				if err := json.Unmarshal(event.Data, &got); err != nil {
					t.Fatal(err)
				}
				if got.ID == id {
					return struct {
						Reason   string `json:"reason"`
						ExitCode *int   `json:"exitCode"`
					}{got.Reason, got.ExitCode}
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for terminal exit for %s", id)
		}
	}
}

func waitForPluginSessionExit(t *testing.T, c *Controller, id, reason string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := c.pluginSessions.status(terminalPluginID, json.RawMessage(`{"id":"`+id+`"}`))
		if err == nil {
			var status pluginSessionStatus
			if json.Unmarshal(raw, &status) == nil && status.State == "exited" && status.Reason == reason {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s did not exit with reason %s", id, reason)
}
