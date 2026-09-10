package scheduler

import (
	"strings"
	"testing"

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
