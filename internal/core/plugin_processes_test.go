package core

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestPluginProcessStreamsExitAndOwnership(t *testing.T) {
	events := make(chan struct {
		name string
		data map[string]any
	}, 16)
	m := newPluginProcessManager(func(_ string, name string, data any) {
		events <- struct {
			name string
			data map[string]any
		}{name, data.(map[string]any)}
	})
	result, err := m.start("one", pluginProcessStart{Command: os.Args[0], Args: []string{"-test.run=TestPluginProcessHelper", "--"}, Environment: map[string]string{"RUNPILOT_PLUGIN_HELPER": "output"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.status("two", result.ID); err == nil {
		t.Fatal("another plugin queried owned process")
	}
	seenOutput, seenExit := false, false
	deadline := time.After(3 * time.Second)
	for !seenExit {
		select {
		case event := <-events:
			if event.name == "process.stdout" && strings.Contains(event.data["text"].(string), "hello") {
				seenOutput = true
			}
			if event.name == "process.exit" {
				seenExit = true
			}
		case <-deadline:
			t.Fatal("process events timed out")
		}
	}
	if !seenOutput {
		t.Fatal("stdout was not delivered before exit")
	}
}

func TestPluginProcessTerminate(t *testing.T) {
	events := make(chan struct {
		name string
		data map[string]any
	}, 16)
	m := newPluginProcessManager(func(_ string, name string, data any) {
		events <- struct {
			name string
			data map[string]any
		}{name, data.(map[string]any)}
	})
	result, err := m.start("one", pluginProcessStart{Command: os.Args[0], Args: []string{"-test.run=TestPluginProcessHelper", "--"}, Environment: map[string]string{"RUNPILOT_PLUGIN_HELPER": "wait"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.terminate("one", result.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			if event.name == "process.exit" {
				if terminated, _ := event.data["terminated"].(bool); !terminated {
					t.Fatal("exit was not marked terminated")
				}
				return
			}
		case <-deadline:
			t.Fatal("termination event timed out")
		}
	}
}

func TestPluginProcessHelper(t *testing.T) {
	if os.Getenv("RUNPILOT_PLUGIN_HELPER") == "" {
		return
	}
	if os.Getenv("RUNPILOT_PLUGIN_HELPER") == "output" {
		_, _ = os.Stdout.WriteString("hello\n")
		return
	}
	select {}
}
