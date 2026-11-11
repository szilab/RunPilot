package main

import (
	"strconv"
	"strings"
)

// A step is one command of an operation. Each engine decides its own
// arguments and which exit codes count as success.
type step struct {
	Name    string
	Command string
	Args    []string
	Success func(int) bool
}

func exitZero(code int) bool { return code == 0 }

// rsync exit code 24 means files vanished while copying, which is normal
// for live folders.
func rsyncSuccess(code int) bool { return code == 0 || code == 24 }

// Robocopy exit codes 0-7 are success or non-fatal differences; 8 and
// above mean at least one copy failed.
func robocopySuccess(code int) bool { return code >= 0 && code < 8 }

func executable(configured, name, hostOS string) string {
	if configured != "" {
		return configured
	}
	if hostOS == "windows" {
		return name + ".exe"
	}
	return name
}

func resticBase(repo *Repository) []string {
	args := []string{"--repo", repo.Location}
	if repo.PasswordFile != "" {
		args = append(args, "--password-file", repo.PasswordFile)
	}
	return args
}

// resticTagFilter selects this job's snapshots: restic matches a comma
// separated list when a snapshot has all of the tags.
func resticTagFilter(r *Restic) []string {
	if len(r.Tags) == 0 {
		return nil
	}
	return []string{"--tag", strings.Join(r.Tags, ",")}
}

func resticStep(name string, repo *Repository, hostOS string, args ...string) step {
	return step{Name: name, Command: executable(repo.Executable, "restic", hostOS), Args: append(resticBase(repo), args...), Success: exitZero}
}

// backupPlan returns the steps of a scheduled or manual run. repo is the
// job's repository for restic jobs and nil for mirror jobs.
func backupPlan(j *Job, repo *Repository, hostOS string) []step {
	switch j.Engine {
	case engineRestic:
		r := j.Restic
		args := []string{"backup"}
		for _, value := range r.Excludes {
			args = append(args, "--exclude", value)
		}
		for _, tag := range r.Tags {
			args = append(args, "--tag", tag)
		}
		if r.UseFsSnapshot {
			args = append(args, "--use-fs-snapshot")
		}
		steps := []step{resticStep("backup", repo, hostOS, append(args, r.Sources...)...)}
		if k := r.Retention; k != nil {
			forget := append([]string{"forget"}, resticTagFilter(r)...)
			for _, keep := range []struct {
				flag  string
				count int
			}{{"--keep-last", k.KeepLast}, {"--keep-daily", k.KeepDaily}, {"--keep-weekly", k.KeepWeekly}, {"--keep-monthly", k.KeepMonthly}, {"--keep-yearly", k.KeepYearly}} {
				if keep.count > 0 {
					forget = append(forget, keep.flag, strconv.Itoa(keep.count))
				}
			}
			if k.KeepWithin != "" {
				forget = append(forget, "--keep-within", k.KeepWithin)
			}
			if k.Prune {
				forget = append(forget, "--prune")
			}
			steps = append(steps, resticStep("retention", repo, hostOS, forget...))
		}
		if r.CheckAfterBackup {
			steps = append(steps, resticStep("check", repo, hostOS, "check"))
		}
		return steps
	case engineRsync:
		m := j.Mirror
		args := []string{"--archive", "--human-readable", "--stats"}
		if m.Delete {
			args = append(args, "--delete")
		}
		for _, value := range m.Excludes {
			args = append(args, "--exclude", value)
		}
		args = append(args, m.AdditionalArgs...)
		// A trailing slash copies the folder's contents, so the destination
		// mirrors the source like robocopy does.
		source := strings.TrimRight(m.Source, "/") + "/"
		args = append(args, source, m.Destination)
		return []step{{Name: "copy", Command: executable(m.Executable, "rsync", hostOS), Args: args, Success: rsyncSuccess}}
	case engineRobocopy:
		m := j.Mirror
		args := []string{m.Source, m.Destination}
		if m.Delete {
			args = append(args, "/MIR")
		} else {
			args = append(args, "/E")
		}
		args = append(args, "/COPY:DAT", "/DCOPY:DAT", "/XJ", "/R:"+strconv.Itoa(m.Retries), "/W:"+strconv.Itoa(m.RetryWaitSeconds), "/NP")
		if len(m.Excludes) > 0 {
			args = append(append(args, "/XD"), m.Excludes...)
		}
		if len(m.ExcludeFiles) > 0 {
			args = append(append(args, "/XF"), m.ExcludeFiles...)
		}
		args = append(args, m.AdditionalArgs...)
		return []step{{Name: "copy", Command: executable(m.Executable, "robocopy", hostOS), Args: args, Success: robocopySuccess}}
	}
	return nil
}

// Repository operations.
var maintenance = map[string]string{"check": "Check repository", "unlock": "Remove stale locks", "init": "Initialize repository"}

func maintenancePlan(kind string, repo *Repository, hostOS string) []step {
	return []step{resticStep(kind, repo, hostOS, kind)}
}

func restorePlan(repo *Repository, hostOS, snapshot, target string, include []string) []step {
	args := []string{"restore", snapshot, "--target", target}
	for _, value := range include {
		args = append(args, "--include", value)
	}
	return []step{resticStep("restore", repo, hostOS, args...)}
}

// validSnapshot accepts a restic snapshot ID (full or short) or "latest".
func validSnapshot(value string) bool {
	if value == "latest" {
		return true
	}
	if len(value) < 8 || len(value) > 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

// tool describes whether an engine's command can run on this host.
type tool struct {
	Engine    string `json:"engine"`
	Command   string `json:"command"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Message   string `json:"message,omitempty"`
}

func engineNames(hostOS string) []string {
	if hostOS == "windows" {
		return []string{engineRestic, engineRobocopy}
	}
	return []string{engineRestic, engineRsync}
}

// detectTool runs the engine's version command. Robocopy has none, so any
// exit code shows that it exists.
func detectTool(engine, command string) tool {
	args := []string{"version"}
	switch engine {
	case engineRsync:
		args = []string{"--version"}
	case engineRobocopy:
		args = []string{"/?"}
	}
	var result struct {
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		TimedOut bool   `json:"timedOut"`
	}
	t := tool{Engine: engine, Command: command}
	err := callHost("process.run", map[string]any{"command": command, "args": args, "timeoutSeconds": 15, "maxOutputBytes": 4096}, &result)
	switch {
	case err != nil && isNotFound(err):
		t.Message = command + " is not installed on this host"
	case err != nil:
		t.Message = errText(err)
	case result.TimedOut:
		t.Message = command + " did not answer in time"
	case engine != engineRobocopy && result.ExitCode != 0:
		t.Message = command + " exited with code " + strconv.Itoa(result.ExitCode)
	default:
		t.Available = true
		if engine != engineRobocopy {
			line, _, _ := strings.Cut(strings.TrimSpace(result.Stdout), "\n")
			t.Version = strings.Join(strings.Fields(line), " ")
			if len(t.Version) > 120 {
				t.Version = t.Version[:120]
			}
		}
	}
	return t
}
