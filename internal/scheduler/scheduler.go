package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/jobs"
	"github.com/szilab/RunPilot/internal/model"
)

type Scheduler struct {
	mu     sync.Mutex
	cancel chan struct{}
	wg     sync.WaitGroup
	runner *jobs.Runner
}

func New(runner *jobs.Runner) *Scheduler {
	return &Scheduler{runner: runner}
}

func (s *Scheduler) Reload(defs []model.JobDefinition) error {
	compiled := make([]scheduledJob, 0, len(defs))
	for _, def := range defs {
		if !def.Enabled {
			continue
		}
		next, err := compile(def.Schedule)
		if err != nil {
			return fmt.Errorf("job %q: %w", def.Name, err)
		}
		compiled = append(compiled, scheduledJob{def: def, next: next})
	}

	s.Stop()

	cancel := make(chan struct{})
	s.mu.Lock()
	s.cancel = cancel
	for _, item := range compiled {
		s.wg.Add(1)
		go s.loop(cancel, item)
	}
	s.mu.Unlock()
	return nil
}

func (s *Scheduler) loop(cancel <-chan struct{}, item scheduledJob) {
	defer s.wg.Done()
	for {
		next := item.next(time.Now())
		delay := time.Until(next)
		if delay < 0 {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-cancel:
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
			_, _ = s.runner.Run(item.def)
		}
	}
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		close(cancel)
		s.wg.Wait()
	}
}

type scheduledJob struct {
	def  model.JobDefinition
	next func(time.Time) time.Time
}

func Expression(spec model.ScheduleSpec) (string, error) {
	switch spec.Type {
	case model.ScheduleInterval:
		if spec.IntervalSeconds <= 0 {
			return "", fmt.Errorf("intervalSeconds must be > 0")
		}
		return "@every " + strconv.Itoa(spec.IntervalSeconds) + "s", nil
	case model.ScheduleDaily:
		if _, _, err := parseTimeOfDay(spec.TimeOfDay); err != nil {
			return "", err
		}
		prefix := ""
		if spec.TimeZone != "" {
			if _, err := time.LoadLocation(spec.TimeZone); err != nil {
				return "", fmt.Errorf("invalid timeZone %q: %w", spec.TimeZone, err)
			}
			prefix = "TZ=" + spec.TimeZone + " "
		}
		return prefix + "daily " + spec.TimeOfDay, nil
	case model.ScheduleCron:
		if _, err := parseCron(spec.Cron); err != nil {
			return "", err
		}
		if spec.TimeZone != "" {
			if _, err := time.LoadLocation(spec.TimeZone); err != nil {
				return "", fmt.Errorf("invalid timeZone %q: %w", spec.TimeZone, err)
			}
			return "TZ=" + spec.TimeZone + " " + strings.TrimSpace(spec.Cron), nil
		}
		return strings.TrimSpace(spec.Cron), nil
	default:
		return "", fmt.Errorf("unsupported schedule type %q", spec.Type)
	}
}

func compile(spec model.ScheduleSpec) (func(time.Time) time.Time, error) {
	if _, err := Expression(spec); err != nil {
		return nil, err
	}
	loc := time.Local
	if spec.TimeZone != "" {
		var err error
		loc, err = time.LoadLocation(spec.TimeZone)
		if err != nil {
			return nil, err
		}
	}

	switch spec.Type {
	case model.ScheduleInterval:
		d := time.Duration(spec.IntervalSeconds) * time.Second
		return func(now time.Time) time.Time { return now.Add(d) }, nil
	case model.ScheduleDaily:
		h, m, _ := parseTimeOfDay(spec.TimeOfDay)
		return func(now time.Time) time.Time {
			local := now.In(loc)
			next := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, loc)
			if !next.After(local) {
				next = next.AddDate(0, 0, 1)
			}
			return next
		}, nil
	case model.ScheduleCron:
		expr, _ := parseCron(spec.Cron)
		return func(now time.Time) time.Time { return expr.next(now.In(loc)) }, nil
	default:
		return nil, fmt.Errorf("unsupported schedule type %q", spec.Type)
	}
}

func parseTimeOfDay(v string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("timeOfDay must use HH:MM")
	}
	h, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("invalid timeOfDay %q", v)
	}
	return h, m, nil
}

type cronExpr struct {
	second, minute, hour, dom, month, dow cronField
	domAny, dowAny                        bool
}

func parseCron(raw string) (cronExpr, error) {
	parts := strings.Fields(raw)
	if len(parts) == 5 {
		parts = append([]string{"0"}, parts...)
	}
	if len(parts) != 6 {
		return cronExpr{}, fmt.Errorf("cron must contain 5 or 6 fields")
	}
	ranges := [][2]int{{0, 59}, {0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	fields := make([]cronField, 6)
	for i := range fields {
		f, err := parseCronField(parts[i], ranges[i][0], ranges[i][1])
		if err != nil {
			return cronExpr{}, fmt.Errorf("cron field %d: %w", i+1, err)
		}
		fields[i] = f
	}
	return cronExpr{
		second: fields[0], minute: fields[1], hour: fields[2],
		dom: fields[3], month: fields[4], dow: fields[5],
		domAny: parts[3] == "*", dowAny: parts[5] == "*",
	}, nil
}

type cronField map[int]bool

func parseCronField(raw string, min, max int) (cronField, error) {
	out := make(cronField)
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, fmt.Errorf("empty cron token")
		}
		base, step := token, 1
		if strings.Contains(token, "/") {
			p := strings.Split(token, "/")
			if len(p) != 2 {
				return nil, fmt.Errorf("invalid step %q", token)
			}
			base = p[0]
			n, err := strconv.Atoi(p[1])
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid step %q", token)
			}
			step = n
		}
		start, end := min, max
		if base != "*" {
			if strings.Contains(base, "-") {
				p := strings.Split(base, "-")
				if len(p) != 2 {
					return nil, fmt.Errorf("invalid range %q", base)
				}
				var err error
				start, err = strconv.Atoi(p[0])
				if err != nil {
					return nil, fmt.Errorf("invalid number %q", p[0])
				}
				end, err = strconv.Atoi(p[1])
				if err != nil {
					return nil, fmt.Errorf("invalid number %q", p[1])
				}
			} else {
				n, err := strconv.Atoi(base)
				if err != nil {
					return nil, fmt.Errorf("invalid number %q", base)
				}
				start, end = n, n
			}
		}
		if start < min || end > max || start > end {
			return nil, fmt.Errorf("value %d-%d outside %d-%d", start, end, min, max)
		}
		for v := start; v <= end; v += step {
			out[v] = true
		}
	}
	return out, nil
}

func (c cronExpr) match(t time.Time) bool {
	if !c.second[t.Second()] || !c.minute[t.Minute()] || !c.hour[t.Hour()] || !c.month[int(t.Month())] {
		return false
	}
	domMatch := c.dom[t.Day()]
	dowMatch := c.dow[int(t.Weekday())]
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dowMatch
	case c.dowAny:
		return domMatch
	default:
		// Traditional cron treats day-of-month and day-of-week as OR when both are restricted.
		return domMatch || dowMatch
	}
}

func (c cronExpr) next(now time.Time) time.Time {
	// Search at one-second resolution. This is simple and deterministic for a local
	// scheduler; the upper bound prevents malformed calendars from looping forever.
	t := now.Truncate(time.Second).Add(time.Second)
	limit := t.AddDate(1, 0, 0)
	for !t.After(limit) {
		if c.match(t) {
			return t
		}
		t = t.Add(time.Second)
	}
	return limit
}
