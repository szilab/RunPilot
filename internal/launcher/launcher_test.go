package launcher

import (
	"runtime"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestInferInterpreterForShellScripts(t *testing.T) {
	if got := inferInterpreter("task.sh"); got != "sh" {
		t.Fatalf(".sh = %q", got)
	}
	if got := inferInterpreter("task.bash"); got != "bash" {
		t.Fatalf(".bash = %q", got)
	}
}

func TestRejectsIncompatibleInterpreter(t *testing.T) {
	interpreter := "cmd"
	if runtime.GOOS == "windows" {
		interpreter = "sh"
	}
	if _, err := Build(model.CommandSpec{Path: "script", Interpreter: interpreter}); err == nil {
		t.Fatalf("%s should be rejected on %s", interpreter, runtime.GOOS)
	}
}

func TestInlineShellCommandUsesSingleCommandArgument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("inline shell commands are Unix-only")
	}
	cmd, err := Build(model.CommandSpec{Path: "printf '%s' inline", Interpreter: "sh-inline"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cmd.Args, []string{"sh", "-c", "printf '%s' inline"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}
