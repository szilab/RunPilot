//go:build linux

package launcher

import (
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestLinuxCommandsUseSeparateProcessGroup(t *testing.T) {
	cmd, err := Build(model.CommandSpec{Path: "/bin/true", Interpreter: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("managed Linux command does not have its own process group")
	}
}
