package backup

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestRobocopyPlanIncludesExclusions(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Robocopy is only available on Windows")
	}
	p, err := BuildPlan(model.BackupSpec{Engine: "robocopy", Robocopy: &model.RobocopyBackupSpec{Source: `C:\Data`, Destination: `F:\Data`, Mode: model.BackupCopy, Retries: 2, RetryWaitSeconds: 5, ExcludeDirs: []string{"node_modules"}, ExcludeFiles: []string{"*.tmp"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`C:\Data`, `F:\Data`, "/E", "/COPY:DAT", "/DCOPY:DAT", "/XJ", "/R:2", "/W:5", "/NP", "/XD", "node_modules", "/XF", "*.tmp"}
	if !reflect.DeepEqual(p.Steps[0].Command.Args, want) {
		t.Fatalf("args = %#v", p.Steps[0].Command.Args)
	}
}

func TestResticPlan(t *testing.T) {
	p, err := BuildPlan(model.BackupSpec{Engine: "restic", Restic: &model.ResticBackupSpec{Repository: `F:\Repo`, Sources: []string{`C:\One`, `D:\Two`}, Excludes: []string{"*.tmp"}, Tags: []string{"work"}, UseVSS: true, Retention: &model.ResticRetention{KeepDaily: 7, Prune: true}, CheckAfterBackup: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 3 || p.Steps[0].Name != "backup" || p.Steps[1].Name != "retention" || p.Steps[2].Name != "check" {
		t.Fatalf("steps = %#v", p.Steps)
	}
	if !reflect.DeepEqual(p.Steps[0].Command.Args, []string{"--repo", `F:\Repo`, "backup", "--exclude", "*.tmp", "--tag", "work", "--use-fs-snapshot", `C:\One`, `D:\Two`}) {
		t.Fatalf("backup args = %#v", p.Steps[0].Command.Args)
	}
	if !reflect.DeepEqual(p.Steps[1].Command.Args, []string{"--repo", `F:\Repo`, "forget", "--keep-daily", "7", "--prune"}) {
		t.Fatalf("retention args = %#v", p.Steps[1].Command.Args)
	}
}

func TestResticValidation(t *testing.T) {
	if err := Validate(model.BackupSpec{Engine: "restic", Restic: &model.ResticBackupSpec{Sources: []string{"C:\\Data"}}}); err == nil {
		t.Fatal("missing repository accepted")
	}
	if err := Validate(model.BackupSpec{Engine: "restic", Restic: &model.ResticBackupSpec{Repository: "repo", Sources: []string{"C:\\Data"}, Retention: &model.ResticRetention{KeepLast: -1}}}); err == nil {
		t.Fatal("negative retention accepted")
	}
}

func TestRdiffBackupPlan(t *testing.T) {
	p, err := BuildPlan(model.BackupSpec{Engine: "rdiff-backup", RdiffBackup: &model.RdiffBackupSpec{Source: `C:\Photos`, Destination: `F:\Photos`, Excludes: []string{"cache"}, Retention: &model.RdiffBackupRetention{OlderThan: "3M"}, VerifyAfterBackup: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 3 || p.Steps[1].Name != "retention" || p.Steps[2].Name != "verify" {
		t.Fatalf("steps = %#v", p.Steps)
	}
	if !reflect.DeepEqual(p.Steps[1].Command.Args, []string{"remove", "increments", "--older-than", "3M", `F:\Photos`}) {
		t.Fatalf("retention = %#v", p.Steps[1].Command.Args)
	}
}
