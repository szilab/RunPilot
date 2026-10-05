package main

import (
	"errors"
	"strconv"
	"strings"
)

const (
	typeContinuous = "continuous"
	typeScheduled  = "scheduled"

	restartNever     = "never"
	restartOnFailure = "on-failure"
	restartAlways    = "always"

	overlapSkip  = "skip"
	overlapAllow = "allow"

	maxTasks          = 500
	maxNameLength     = 200
	maxDescription    = 2000
	maxCommandString  = 32 << 10
	maxArguments      = 1024
	maxEnvironment    = 256
	maxTimeoutSeconds = 7 * 24 * 3600
	maxRetries        = 1000
	maxDelaySeconds   = 24 * 3600
)

type Command struct {
	Path             string            `json:"path"`
	Args             []string          `json:"args,omitempty"`
	WorkingDirectory string            `json:"workingDirectory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Interpreter      string            `json:"interpreter,omitempty"`
}

type Restart struct {
	Mode                string `json:"mode"`
	InitialDelaySeconds int    `json:"initialDelaySeconds"`
	MaxDelaySeconds     int    `json:"maxDelaySeconds"`
	MaxRetries          int    `json:"maxRetries"`
}

type Schedule struct {
	Type            string `json:"type"`
	IntervalSeconds int    `json:"intervalSeconds,omitempty"`
	TimeOfDay       string `json:"timeOfDay,omitempty"`
	Cron            string `json:"cron,omitempty"`
	TimeZone        string `json:"timeZone,omitempty"`
}

// Task is one persisted definition. Fields for the other task type are
// cleared on validation so a stored task is unambiguous.
type Task struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Type        string  `json:"type"`
	Command     Command `json:"command"`

	// Continuous tasks.
	Autostart bool     `json:"autostart,omitempty"`
	Restart   *Restart `json:"restart,omitempty"`

	// Scheduled tasks.
	Enabled        bool      `json:"enabled,omitempty"`
	Schedule       *Schedule `json:"schedule,omitempty"`
	OverlapPolicy  string    `json:"overlapPolicy,omitempty"`
	TimeoutSeconds int       `json:"timeoutSeconds,omitempty"`
}

var interpreters = map[string]bool{"": true, "auto": true, "direct": true, "powershell": true, "cmd": true, "sh": true, "bash": true, "sh-inline": true, "python": true}

func fail(message string) error { return errors.New(message) }

// normalizeTask trims and applies defaults. It mirrors the legacy
// NormalizeProcess/NormalizeJob defaults.
func normalizeTask(t *Task) {
	t.Name = strings.TrimSpace(t.Name)
	t.Description = strings.TrimSpace(t.Description)
	t.Command.Path = strings.TrimSpace(t.Command.Path)
	t.Command.Interpreter = strings.ToLower(strings.TrimSpace(t.Command.Interpreter))
	if t.Command.Interpreter == "auto" {
		t.Command.Interpreter = ""
	}
	if len(t.Command.Args) == 0 {
		t.Command.Args = nil
	}
	if len(t.Command.Environment) == 0 {
		t.Command.Environment = nil
	}
	switch t.Type {
	case typeContinuous:
		t.Enabled, t.Schedule, t.OverlapPolicy, t.TimeoutSeconds = false, nil, "", 0
		if t.Restart == nil || t.Restart.Mode == "" {
			t.Restart = &Restart{Mode: restartOnFailure, InitialDelaySeconds: 2, MaxDelaySeconds: 60}
		}
		if t.Restart.InitialDelaySeconds <= 0 {
			t.Restart.InitialDelaySeconds = 2
		}
		if t.Restart.MaxDelaySeconds <= 0 {
			t.Restart.MaxDelaySeconds = 60
		}
	case typeScheduled:
		t.Autostart, t.Restart = false, nil
		if t.OverlapPolicy == "" {
			t.OverlapPolicy = overlapSkip
		}
		if t.Schedule != nil {
			t.Schedule.Type = strings.TrimSpace(t.Schedule.Type)
			t.Schedule.TimeOfDay = strings.TrimSpace(t.Schedule.TimeOfDay)
			t.Schedule.Cron = strings.TrimSpace(t.Schedule.Cron)
			t.Schedule.TimeZone = strings.TrimSpace(t.Schedule.TimeZone)
		}
	}
}

// validateTask checks bounds and invariants that do not need the host. Schedule
// syntax is validated by the host scheduler so both paths share one compiler.
func validateTask(t *Task) error {
	if t.Type != typeContinuous && t.Type != typeScheduled {
		return fail("task type must be continuous or scheduled")
	}
	if t.Name == "" {
		return fail("task name is required")
	}
	if len(t.Name) > maxNameLength || len(t.Description) > maxDescription {
		return fail("task name or description is too long")
	}
	if t.Command.Path == "" {
		return fail("task command is required")
	}
	if len(t.Command.Path) > maxCommandString || len(t.Command.WorkingDirectory) > maxCommandString {
		return fail("task command or working directory is too long")
	}
	if len(t.Command.Args) > maxArguments {
		return fail("task has too many arguments")
	}
	for _, arg := range t.Command.Args {
		if len(arg) > maxCommandString {
			return fail("task argument is too long")
		}
	}
	if !interpreters[t.Command.Interpreter] {
		return fail("unsupported interpreter \"" + t.Command.Interpreter + "\"")
	}
	if err := validateEnvironment(t.Command.Environment); err != nil {
		return err
	}
	switch t.Type {
	case typeContinuous:
		r := t.Restart
		if r.Mode != restartNever && r.Mode != restartOnFailure && r.Mode != restartAlways {
			return fail("restart mode must be never, on-failure or always")
		}
		if r.MaxRetries < 0 || r.MaxRetries > maxRetries || r.InitialDelaySeconds > maxDelaySeconds || r.MaxDelaySeconds > maxDelaySeconds {
			return fail("restart limits are out of range")
		}
	case typeScheduled:
		if t.Schedule == nil {
			return fail("scheduled task requires a schedule")
		}
		if t.OverlapPolicy != overlapSkip && t.OverlapPolicy != overlapAllow {
			return fail("overlap policy must be skip or allow")
		}
		if t.TimeoutSeconds < 0 || t.TimeoutSeconds > maxTimeoutSeconds {
			return fail("timeoutSeconds must be between 0 and " + strconv.Itoa(maxTimeoutSeconds))
		}
	}
	return nil
}

// validateEnvironment matches RunPilot's command validation: names must be
// representable on Windows, where they are case-insensitive.
func validateEnvironment(environment map[string]string) error {
	if len(environment) > maxEnvironment {
		return fail("too many environment variables")
	}
	seen := make(map[string]string, len(environment))
	for name, value := range environment {
		if strings.TrimSpace(name) == "" {
			return fail("environment variable name is required")
		}
		if strings.Contains(name, "=") {
			return fail("environment variable name \"" + name + "\" must not contain =")
		}
		if len(name) > 1024 || len(value) > maxCommandString {
			return fail("environment variable is too long")
		}
		canonical := strings.ToUpper(name)
		if previous, exists := seen[canonical]; exists {
			return fail("environment variable names \"" + previous + "\" and \"" + name + "\" conflict on case-insensitive platforms")
		}
		seen[canonical] = name
	}
	return nil
}

// restartDelay is the legacy exponential backoff: initial, doubled per retry,
// capped at the maximum.
func restartDelay(r *Restart, retry int) int {
	base := r.InitialDelaySeconds
	if base <= 0 {
		base = 2
	}
	max := r.MaxDelaySeconds
	if max <= 0 {
		max = 60
	}
	seconds := base
	for i := 0; i < retry; i++ {
		seconds *= 2
		if seconds >= max {
			seconds = max
			break
		}
	}
	return seconds
}

func cloneTask(t Task) Task {
	c := t
	if t.Restart != nil {
		r := *t.Restart
		c.Restart = &r
	}
	if t.Schedule != nil {
		s := *t.Schedule
		c.Schedule = &s
	}
	c.Command.Args = append([]string(nil), t.Command.Args...)
	if len(c.Command.Args) == 0 {
		c.Command.Args = nil
	}
	if t.Command.Environment != nil {
		c.Command.Environment = make(map[string]string, len(t.Command.Environment))
		for k, v := range t.Command.Environment {
			c.Command.Environment[k] = v
		}
	}
	return c
}
