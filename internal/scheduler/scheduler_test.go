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
		TimeZone:  "Europe/Berlin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "daily 03:15") {
		t.Fatalf("unexpected expression %q", got)
	}
}

func TestPluginRegistrationOwnershipAndRemoval(t *testing.T) {
	s := New()
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

func TestStopRemovesPluginSchedules(t *testing.T) {
	s := New()
	value := PluginSchedule{Owner: "tasks", ID: "one", Callback: "tick", Schedule: model.ScheduleSpec{Type: model.ScheduleInterval, IntervalSeconds: 3600}}
	if err := s.RegisterPlugin(value, func(PluginSchedule) {}); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	if len(s.ListPlugin("tasks")) != 0 {
		t.Fatal("Stop must remove plugin schedules")
	}
}

func TestPluginScheduleReportsNextFireTime(t *testing.T) {
	s := New()
	defer s.Stop()
	fired := make(chan struct{}, 4)
	before := time.Now()
	first, err := s.RegisterPluginNext(PluginSchedule{Owner: "one", ID: "tick", Callback: "tick", Schedule: model.ScheduleSpec{Type: model.ScheduleInterval, IntervalSeconds: 1}}, func(PluginSchedule) { fired <- struct{}{} })
	if err != nil || first.Before(before.Add(time.Second)) || first.After(time.Now().Add(time.Second)) {
		t.Fatalf("first = %v, %v", first, err)
	}
	if list := s.ListPlugin("one"); len(list) != 1 || !list[0].NextRunAt.Equal(first) {
		t.Fatalf("list = %#v", list)
	}
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("schedule did not fire")
	}
	waitFor := time.Now().Add(time.Second)
	for time.Now().Before(waitFor) && !s.ListPlugin("one")[0].NextRunAt.After(first) {
		time.Sleep(10 * time.Millisecond)
	}
	if next := s.ListPlugin("one")[0].NextRunAt; !next.After(first) {
		t.Fatalf("next = %v, first = %v", next, first)
	}
}
