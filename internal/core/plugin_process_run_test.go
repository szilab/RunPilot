package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/szilab/RunPilot/internal/plugins"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type runResult struct {
	ExitCode        int    `json:"exitCode"`
	Success         bool   `json:"success"`
	TimedOut        bool   `json:"timedOut"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdoutTruncated"`
	StderrTruncated bool   `json:"stderrTruncated"`
}

func TestProcessRunHelper(t *testing.T) {
	if os.Getenv("RUNPILOT_RUN_HELPER") != "1" {
		return
	}
	mode := os.Getenv("RUNPILOT_RUN_MODE")
	switch mode {
	case "sleep":
		time.Sleep(10 * time.Second)
	case "output":
		fmt.Print(strings.Repeat("x", 1000))
		fmt.Fprint(os.Stderr, strings.Repeat("y", 1000))
	case "exit":
		fmt.Fprint(os.Stderr, "failure")
		os.Exit(23)
	default:
		fmt.Print(strings.Join(os.Args, "|"))
		cwd, _ := os.Getwd()
		fmt.Fprint(os.Stderr, cwd)
	}
	os.Exit(0)
}
func processRunCall(t *testing.T, c *Controller, in pluginRunInput) (json.RawMessage, error) {
	t.Helper()
	if in.Environment == nil {
		in.Environment = map[string]string{}
	}
	in.Environment["RUNPILOT_RUN_HELPER"] = "1"
	raw, _ := json.Marshal(in)
	return c.pluginProcessRun(context.Background(), "owner", raw)
}
func TestPluginProcessRun(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	base := pluginRunInput{Command: os.Args[0], Args: []string{"-test.run=^TestProcessRunHelper$", "hello world", "$literal"}, TimeoutSeconds: 3}
	raw, err := processRunCall(t, c, base)
	if err != nil {
		t.Fatal(err)
	}
	var out runResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Success || !strings.Contains(out.Stdout, "hello world|$literal") {
		t.Fatalf("arguments changed: %+v", out)
	}
	base.Environment = map[string]string{"RUNPILOT_RUN_MODE": "exit"}
	raw, err = processRunCall(t, c, base)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &out)
	if out.Success || out.ExitCode != 23 || !strings.Contains(out.Stderr, "failure") {
		t.Fatalf("exit: %+v", out)
	}
	base.Environment = map[string]string{"RUNPILOT_RUN_MODE": "output"}
	base.MaxOutputBytes = 20
	raw, err = processRunCall(t, c, base)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Stdout) != 20 || len(out.Stderr) != 20 || !out.StdoutTruncated || !out.StderrTruncated {
		t.Fatalf("capture: %+v", out)
	}
	base.TailOutput = true
	raw, err = processRunCall(t, c, base)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &out)
	if out.Stdout != strings.Repeat("x", 20) || out.Stderr != strings.Repeat("y", 20) {
		t.Fatalf("tail capture: %+v", out)
	}
	base.Command = "missing-runpilot-executable-987654"
	if _, err = processRunCall(t, c, base); err == nil {
		t.Fatal("missing executable accepted")
	}
	var host *plugins.HostFailure
	if !errors.As(err, &host) || host.Code != "not_found" {
		t.Fatalf("missing executable error: %v", err)
	}
	base.Command = os.Args[0]
	base.Environment = map[string]string{"RUNPILOT_RUN_MODE": "sleep"}
	base.TimeoutSeconds = 1
	start := time.Now()
	raw, err = processRunCall(t, c, base)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &out)
	if !out.TimedOut || time.Since(start) > 4*time.Second {
		t.Fatalf("timeout: %+v elapsed %v", out, time.Since(start))
	}
}
func TestPluginProcessRunWorkspace(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	_, err := wsCall(t, c, "owner", "mkdir", "projects/jellyfin", nil)
	if err != nil {
		t.Fatal(err)
	}
	in := pluginRunInput{Command: os.Args[0], Args: []string{"-test.run=^TestProcessRunHelper$"}, Environment: map[string]string{}, WorkspaceDirectory: "projects/jellyfin", TimeoutSeconds: 3}
	raw, err := processRunCall(t, c, in)
	if err != nil {
		t.Fatal(err)
	}
	var out runResult
	_ = json.Unmarshal(raw, &out)
	if !strings.Contains(out.Stderr, filepath.Join("projects", "jellyfin")) {
		t.Fatalf("cwd: %+v", out)
	}
	in.WorkspaceDirectory = "../escape"
	if _, err = processRunCall(t, c, in); err == nil {
		t.Fatal("accepted traversal")
	}
}
