package main

import (
	"errors"
	"strconv"
	"strings"
)

const (
	engineRestic   = "restic"
	engineRsync    = "rsync"
	engineRobocopy = "robocopy"

	maxJobs           = 200
	maxNameLength     = 200
	maxString         = 4096
	maxListItems      = 256
	maxTimeoutSeconds = 7 * 24 * 3600
	maxRetention      = 100000
)

type Schedule struct {
	Type            string `json:"type"`
	IntervalSeconds int    `json:"intervalSeconds,omitempty"`
	TimeOfDay       string `json:"timeOfDay,omitempty"`
	Cron            string `json:"cron,omitempty"`
	TimeZone        string `json:"timeZone,omitempty"`
}

type Retention struct {
	KeepLast    int    `json:"keepLast,omitempty"`
	KeepDaily   int    `json:"keepDaily,omitempty"`
	KeepWeekly  int    `json:"keepWeekly,omitempty"`
	KeepMonthly int    `json:"keepMonthly,omitempty"`
	KeepYearly  int    `json:"keepYearly,omitempty"`
	KeepWithin  string `json:"keepWithin,omitempty"`
	Prune       bool   `json:"prune,omitempty"`
}

// Restic backs up folders into a versioned, deduplicated repository. The
// repository password stays outside RunPilot: a password file, or an
// environment file that restic reads its settings from.
type Restic struct {
	Executable       string     `json:"executable,omitempty"`
	Repository       string     `json:"repository"`
	PasswordFile     string     `json:"passwordFile,omitempty"`
	EnvironmentFile  string     `json:"environmentFile,omitempty"`
	Sources          []string   `json:"sources"`
	Excludes         []string   `json:"excludes,omitempty"`
	Tags             []string   `json:"tags,omitempty"`
	UseFsSnapshot    bool       `json:"useFsSnapshot,omitempty"`
	Retention        *Retention `json:"retention,omitempty"`
	CheckAfterBackup bool       `json:"checkAfterBackup,omitempty"`
}

// Mirror copies a folder to a destination with rsync (Linux) or robocopy
// (Windows). With Delete the destination becomes an exact mirror.
type Mirror struct {
	Executable       string   `json:"executable,omitempty"`
	Source           string   `json:"source"`
	Destination      string   `json:"destination"`
	Delete           bool     `json:"delete,omitempty"`
	Excludes         []string `json:"excludes,omitempty"`
	ExcludeFiles     []string `json:"excludeFiles,omitempty"` // robocopy only
	Retries          int      `json:"retries,omitempty"`      // robocopy only
	RetryWaitSeconds int      `json:"retryWaitSeconds,omitempty"`
	AdditionalArgs   []string `json:"additionalArgs,omitempty"`
}

// Job is one persisted backup definition. Without a schedule it only runs
// on demand.
type Job struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	Engine         string    `json:"engine"`
	Schedule       *Schedule `json:"schedule,omitempty"`
	TimeoutSeconds int       `json:"timeoutSeconds,omitempty"`
	Restic         *Restic   `json:"restic,omitempty"`
	Mirror         *Mirror   `json:"mirror,omitempty"`
}

func fail(message string) error { return errors.New(message) }

func trimList(in []string) []string {
	out := []string{}
	for _, value := range in {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeJob trims values and drops the settings of other engines so a
// stored job is unambiguous.
func normalizeJob(j *Job) {
	j.Name = strings.TrimSpace(j.Name)
	j.Engine = strings.ToLower(strings.TrimSpace(j.Engine))
	if s := j.Schedule; s != nil {
		s.Type = strings.TrimSpace(s.Type)
		s.TimeOfDay, s.Cron, s.TimeZone = strings.TrimSpace(s.TimeOfDay), strings.TrimSpace(s.Cron), strings.TrimSpace(s.TimeZone)
		if s.Type == "" || s.Type == "manual" {
			j.Schedule = nil
		}
	}
	switch j.Engine {
	case engineRestic:
		j.Mirror = nil
		if r := j.Restic; r != nil {
			r.Executable, r.Repository = strings.TrimSpace(r.Executable), strings.TrimSpace(r.Repository)
			r.PasswordFile, r.EnvironmentFile = strings.TrimSpace(r.PasswordFile), strings.TrimSpace(r.EnvironmentFile)
			r.Sources, r.Excludes, r.Tags = trimList(r.Sources), trimList(r.Excludes), trimList(r.Tags)
			if k := r.Retention; k != nil {
				k.KeepWithin = strings.TrimSpace(k.KeepWithin)
				if k.KeepLast == 0 && k.KeepDaily == 0 && k.KeepWeekly == 0 && k.KeepMonthly == 0 && k.KeepYearly == 0 && k.KeepWithin == "" {
					r.Retention = nil
				}
			}
		}
	case engineRsync, engineRobocopy:
		j.Restic = nil
		if m := j.Mirror; m != nil {
			m.Executable, m.Source, m.Destination = strings.TrimSpace(m.Executable), strings.TrimSpace(m.Source), strings.TrimSpace(m.Destination)
			m.Excludes, m.ExcludeFiles = trimList(m.Excludes), trimList(m.ExcludeFiles)
			if len(m.AdditionalArgs) == 0 {
				m.AdditionalArgs = nil
			}
			if j.Engine == engineRsync {
				m.ExcludeFiles, m.Retries, m.RetryWaitSeconds = nil, 0, 0
			} else if m.RetryWaitSeconds <= 0 {
				m.RetryWaitSeconds = 5
			}
		}
	}
}

func validList(label string, values []string) error {
	if len(values) > maxListItems {
		return fail("too many " + label)
	}
	for _, value := range values {
		if len(value) > maxString || strings.ContainsRune(value, 0) {
			return fail(label + " contain an invalid value")
		}
	}
	return nil
}

func validValue(label, value string) error {
	if len(value) > maxString || strings.ContainsRune(value, 0) {
		return fail(label + " is invalid")
	}
	return nil
}

// validateJob checks a normalized job on the given host OS. Schedule syntax
// is checked by the host scheduler.
func validateJob(j *Job, hostOS string) error {
	if j.Name == "" {
		return fail("name is required")
	}
	if len(j.Name) > maxNameLength {
		return fail("name is too long")
	}
	if j.TimeoutSeconds < 0 || j.TimeoutSeconds > maxTimeoutSeconds {
		return fail("timeout must be between 0 and " + strconv.Itoa(maxTimeoutSeconds) + " seconds")
	}
	switch j.Engine {
	case engineRestic:
		r := j.Restic
		if r == nil {
			return fail("restic settings are required")
		}
		if r.Repository == "" {
			return fail("repository is required")
		}
		if len(r.Sources) == 0 {
			return fail("add at least one folder to back up")
		}
		for _, source := range r.Sources {
			if !absolutePath(source, hostOS) {
				return fail("folder \"" + source + "\" must be an absolute path")
			}
		}
		if r.PasswordFile == "" && r.EnvironmentFile == "" {
			return fail("restic needs a password file or an environment file that sets RESTIC_PASSWORD or RESTIC_PASSWORD_FILE")
		}
		for label, value := range map[string]string{"password file": r.PasswordFile, "environment file": r.EnvironmentFile} {
			if value != "" && !absolutePath(value, hostOS) {
				return fail("the " + label + " must be an absolute path")
			}
		}
		for label, value := range map[string]string{"executable": r.Executable, "repository": r.Repository, "password file": r.PasswordFile, "environment file": r.EnvironmentFile} {
			if err := validValue(label, value); err != nil {
				return err
			}
		}
		if err := validList("folders", r.Sources); err != nil {
			return err
		}
		if err := validList("exclusions", r.Excludes); err != nil {
			return err
		}
		if err := validList("tags", r.Tags); err != nil {
			return err
		}
		for _, tag := range r.Tags {
			if strings.ContainsAny(tag, ", ") {
				return fail("tags cannot contain commas or spaces")
			}
		}
		if r.UseFsSnapshot && hostOS != "windows" {
			return fail("file system snapshots (VSS) are only available on Windows")
		}
		if k := r.Retention; k != nil {
			for _, n := range []int{k.KeepLast, k.KeepDaily, k.KeepWeekly, k.KeepMonthly, k.KeepYearly} {
				if n < 0 || n > maxRetention {
					return fail("retention values must be between 0 and " + strconv.Itoa(maxRetention))
				}
			}
			if !validDuration(k.KeepWithin) {
				return fail("\"keep within\" must be a duration such as 30d, 6m or 1y2m")
			}
		}
	case engineRsync, engineRobocopy:
		if j.Engine == engineRsync && hostOS == "windows" {
			return fail("rsync is only available on Linux; use robocopy on Windows")
		}
		if j.Engine == engineRobocopy && hostOS != "windows" {
			return fail("robocopy is only available on Windows; use rsync on Linux")
		}
		m := j.Mirror
		if m == nil {
			return fail("source and destination are required")
		}
		if m.Source == "" || m.Destination == "" {
			return fail("source and destination are required")
		}
		if !absolutePath(m.Source, hostOS) && !uncPath(m.Source, hostOS) {
			return fail("source must be an absolute path")
		}
		if sameFolder(m.Source, m.Destination, hostOS) {
			return fail("source and destination must be different folders")
		}
		for label, value := range map[string]string{"executable": m.Executable, "source": m.Source, "destination": m.Destination} {
			if err := validValue(label, value); err != nil {
				return err
			}
		}
		if err := validList("exclusions", m.Excludes); err != nil {
			return err
		}
		if err := validList("file exclusions", m.ExcludeFiles); err != nil {
			return err
		}
		if err := validList("additional arguments", m.AdditionalArgs); err != nil {
			return err
		}
		if m.Retries < 0 || m.Retries > 1000 || m.RetryWaitSeconds < 0 || m.RetryWaitSeconds > 3600 {
			return fail("retries must be 0-1000 and the retry wait 0-3600 seconds")
		}
	default:
		return fail("engine must be restic, rsync or robocopy")
	}
	return nil
}

// validDuration accepts restic's --keep-within format: an optional series
// of <number><unit> with y, m, d and h units, for example 1y2m3d.
func validDuration(value string) bool {
	if value == "" {
		return true
	}
	digits := false
	for _, ch := range value {
		switch {
		case ch >= '0' && ch <= '9':
			digits = true
		case ch == 'y' || ch == 'm' || ch == 'd' || ch == 'h':
			if !digits {
				return false
			}
			digits = false
		default:
			return false
		}
	}
	return !digits && len(value) <= 32
}

func cloneJob(j Job) Job {
	c := j
	if j.Schedule != nil {
		s := *j.Schedule
		c.Schedule = &s
	}
	if j.Restic != nil {
		r := *j.Restic
		r.Sources = append([]string(nil), j.Restic.Sources...)
		r.Excludes = append([]string(nil), j.Restic.Excludes...)
		r.Tags = append([]string(nil), j.Restic.Tags...)
		if j.Restic.Retention != nil {
			k := *j.Restic.Retention
			r.Retention = &k
		}
		c.Restic = &r
	}
	if j.Mirror != nil {
		m := *j.Mirror
		m.Excludes = append([]string(nil), j.Mirror.Excludes...)
		m.ExcludeFiles = append([]string(nil), j.Mirror.ExcludeFiles...)
		m.AdditionalArgs = append([]string(nil), j.Mirror.AdditionalArgs...)
		c.Mirror = &m
	}
	normalizeJob(&c)
	return c
}
