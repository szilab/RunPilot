package backup

import (
	"runtime"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestBuildMirror(t *testing.T) {
	if runtime.GOOS != "windows" {
		if _, err := Build(model.BackupSpec{Engine: "robocopy", Source: "/source", Destination: "/destination"}); err == nil {
			t.Fatal("robocopy should be unavailable off Windows")
		}
		return
	}
	cmd, err := Build(model.BackupSpec{
		Engine:      "robocopy",
		Source:      `C:\Data`,
		Destination: `F:\Backup\Data`,
		Mode:        model.BackupMirror,
		Retries:     2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "robocopy.exe" {
		t.Fatalf("path=%q", cmd.Path)
	}
	found := false
	for _, a := range cmd.Args {
		if a == "/MIR" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing /MIR")
	}
}

func TestPortableBackupEngines(t *testing.T) {
	for _, engine := range []string{"restic", "rdiff-backup"} {
		cmd, err := Build(model.BackupSpec{Engine: engine, Source: "source", Destination: "destination"})
		if err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		if cmd.Path == "" {
			t.Fatalf("%s produced no command", engine)
		}
	}
}

func TestRobocopyExitCodes(t *testing.T) {
	for _, code := range []int{0, 1, 3, 7} {
		if !ExitCodeSuccess(code) {
			t.Fatalf("%d should be success", code)
		}
	}
	if ExitCodeSuccess(8) {
		t.Fatal("8 should fail")
	}
}
