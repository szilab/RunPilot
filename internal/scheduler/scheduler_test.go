package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

func TestDailyExpression(t *testing.T) {
	got, err := Expression(model.ScheduleSpec{
		Type:      model.ScheduleDaily,
		TimeOfDay: "03:15",
		TimeZone:  "Europe/Budapest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "daily 03:15") {
		t.Fatalf("unexpected expression %q", got)
	}
}

func TestPluginRegistrationOwnershipAndRemoval(t *testing.T) {
	s := New(nil)
	defer s.Stop()
	fired := make(chan PluginSchedule, 1)
	value := PluginSchedule{Owner: "one", ID: "same", Callback: "tick", Schedule: model.ScheduleSpec{Type: model.ScheduleInterval, IntervalSeconds: 1}}
	if err := s.RegisterPlugin(value, func(v PluginSchedule) { fired <- v }); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterPlugin(value, func(PluginSchedule) {}); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	if err := s.RegisterPlugin(PluginSchedule{Owner: "two", ID: "same", Callback: "tick", Schedule: value.Schedule}, func(PluginSchedule) {}); err != nil {
		t.Fatalf("other owner: %v", err)
	}
	select {
	case got := <-fired:
		if got.ID != value.ID {
			t.Fatalf("fired %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("schedule did not fire")
	}
	if !s.RemovePlugin("one", "same") {
		t.Fatal("remove failed")
	}
	if len(s.ListPlugin("one")) != 0 || len(s.ListPlugin("two")) != 1 {
		t.Fatal("owner isolation lost")
	}
}

func TestIntervalExpression(t *testing.T) {
	got, err := Expression(model.ScheduleSpec{
		Type:            model.ScheduleInterval,
		IntervalSeconds: 1200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "@every 1200s" {
		t.Fatalf("unexpected expression %q", got)
	}
}

// Reloading legacy job definitions (for example after editing a job) must not
// drop schedules that plugins registered.
func TestReloadKeepsPluginSchedules(t *testing.T) {
	s := New(nil)
	defer s.Stop()
	value := PluginSchedule{Owner: "tasks", ID: "one", Callback: "tick", Schedule: model.ScheduleSpec{Type: model.ScheduleInterval, IntervalSeconds: 3600}}
	if err := s.RegisterPlugin(value, func(PluginSchedule) {}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Reload(nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.ListPlugin("tasks")) != 1 {
		t.Fatal("Reload removed a plugin schedule")
	}
	s.Stop()
	if len(s.ListPlugin("tasks")) != 0 {
		t.Fatal("Stop must remove plugin schedules")
	}
}
