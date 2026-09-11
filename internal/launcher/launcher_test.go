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
