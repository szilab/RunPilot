package core

import (
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestCommandSettingsPersistForProcessesAndJobs(t *testing.T) {
	dir := t.TempDir()
	controller, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	process, err := controller.UpsertProcess(model.ProcessDefinition{
		Name: "Worker",
		Command: model.CommandSpec{
			Path:             "C:\\Tools\\worker.exe",
			WorkingDirectory: "C:\\Tools",
			Environment:      map[string]string{"PORT": "8096"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := controller.UpsertJob(model.JobDefinition{
		Name:    "Scheduled worker",
		Enabled: true,
		Type:    model.JobCommand,
		Schedule: model.ScheduleSpec{
			Type:            model.ScheduleInterval,
			IntervalSeconds: 60,
		},
		Command: &model.CommandSpec{
			Path:             "C:\\Tools\\job.exe",
			WorkingDirectory: "C:\\Tools\\jobs",
			Environment:      map[string]string{"LOG_LEVEL": "info"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	controller.Close()

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot := reopened.Snapshot()
	if got := snapshot.Processes[0].Command; got.WorkingDirectory != process.Command.WorkingDirectory || got.Environment["PORT"] != "8096" {
		t.Fatalf("process command did not round trip: %#v", got)
	}
	if got := snapshot.Jobs[0].Command; got == nil || got.WorkingDirectory != job.Command.WorkingDirectory || got.Environment["LOG_LEVEL"] != "info" {
		t.Fatalf("job command did not round trip: %#v", got)
	}
}

func TestCommandEnvironmentValidationAppliesToProcessesAndJobs(t *testing.T) {
	controller, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()

	_, err = controller.UpsertProcess(model.ProcessDefinition{
		Name:    "Invalid process",
		Command: model.CommandSpec{Path: "app.exe", Environment: map[string]string{"PATH": "a", "Path": "b"}},
	})
	if err == nil {
		t.Fatal("process with duplicate environment names was accepted")
	}
	_, err = controller.UpsertJob(model.JobDefinition{
		Name:     "Invalid job",
		Type:     model.JobCommand,
		Schedule: model.ScheduleSpec{Type: model.ScheduleInterval, IntervalSeconds: 60},
		Command:  &model.CommandSpec{Path: "job.exe", Environment: map[string]string{"NAME=BAD": "value"}},
	})
	if err == nil {
		t.Fatal("job with invalid environment name was accepted")
	}
}
