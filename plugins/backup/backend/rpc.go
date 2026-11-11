package main

import (
	"encoding/json"
	"sort"
	"strings"
)

func decodeRequest(request json.RawMessage, destination any) *rpcError {
	if len(request) == 0 || string(request) == "null" {
		request = json.RawMessage("{}")
	}
	if err := json.Unmarshal(request, destination); err != nil {
		return rpcFail("invalid_argument", "invalid request: "+err.Error())
	}
	return nil
}

type idRequest struct {
	ID string `json:"id"`
}

// handle dispatches one browser RPC.
func (p *plugin) handle(operation string, request json.RawMessage) (any, *rpcError) {
	switch operation {
	case "backup.list":
		p.reconcile()
		result := map[string]any{"jobs": p.views(), "repositories": p.repoViews(), "os": p.hostOS(), "tools": p.toolsFor(false)}
		if p.loadError != "" {
			result["loadError"] = p.loadError
		}
		return result, nil
	case "backup.tools":
		var args struct {
			Refresh bool `json:"refresh"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		return map[string]any{"tools": p.toolsFor(args.Refresh)}, nil
	case "backup.create", "backup.update":
		var args struct {
			Job Job `json:"job"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		return p.save1(args.Job, operation == "backup.create")
	case "backup.delete":
		j, err := p.jobFor(request)
		if err != nil {
			return nil, err
		}
		return p.remove(j)
	case "backup.run", "backup.stop":
		j, err := p.jobFor(request)
		if err != nil {
			return nil, err
		}
		if operation == "backup.stop" {
			if e := p.stop(j.ID, j.Name); e != nil {
				return nil, hostFailure(e)
			}
			return p.view(j, p.nextRuns()), nil
		}
		if e := p.checkJob(j); e != nil {
			return nil, rpcFail("invalid_argument", e.Error())
		}
		repo := p.repoOf(j)
		runID, e := p.start(jobSubject(j, repo), kindBackup, backupPlan(j, repo, p.hostOS()), true)
		if e != nil {
			return nil, hostFailure(e)
		}
		return map[string]any{"runId": runID, "job": p.view(j, p.nextRuns())}, nil
	case "backup.repository.create", "backup.repository.update":
		var args struct {
			Repository Repository `json:"repository"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		return p.saveRepository(args.Repository, operation == "backup.repository.create")
	case "backup.repository.delete":
		repo, err := p.repoFor(request)
		if err != nil {
			return nil, err
		}
		return p.removeRepository(repo)
	case "backup.repository.stop":
		repo, err := p.repoFor(request)
		if err != nil {
			return nil, err
		}
		if e := p.stop(repo.ID, repo.Name); e != nil {
			return nil, hostFailure(e)
		}
		return p.repoView(repo), nil
	case "backup.repository.maintenance":
		var args struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		repo, err := p.usableRepo(args.ID)
		if err != nil {
			return nil, err
		}
		if _, ok := maintenance[args.Kind]; !ok {
			return nil, rpcFail("invalid_argument", "unknown maintenance operation")
		}
		runID, e := p.start(repoSubject(repo), args.Kind, maintenancePlan(args.Kind, repo, p.hostOS()), true)
		if e != nil {
			return nil, hostFailure(e)
		}
		return map[string]any{"runId": runID}, nil
	case "backup.repository.snapshots":
		var args idRequest
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		repo, err := p.usableRepo(args.ID)
		if err != nil {
			return nil, err
		}
		return p.snapshots(repo)
	case "backup.repository.restore":
		var args struct {
			ID       string   `json:"id"`
			Snapshot string   `json:"snapshot"`
			Target   string   `json:"target"`
			Include  []string `json:"include"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		repo, err := p.usableRepo(args.ID)
		if err != nil {
			return nil, err
		}
		return p.restore(repo, strings.TrimSpace(args.Snapshot), strings.TrimSpace(args.Target), trimList(args.Include))
	case "backup.history":
		var args struct {
			ID    string `json:"id"`
			Limit int    `json:"limit"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		var list struct {
			Executions []json.RawMessage `json:"executions"`
		}
		if err := callHost("history.list", map[string]any{"subject": args.ID, "limit": args.Limit}, &list); err != nil {
			return nil, hostFailure(err)
		}
		if list.Executions == nil {
			list.Executions = []json.RawMessage{}
		}
		return map[string]any{"runs": list.Executions}, nil
	case "backup.output":
		var args struct {
			RunID    string `json:"runId"`
			MaxBytes int    `json:"maxBytes"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		var run json.RawMessage
		if err := callHost("history.get", map[string]any{"id": args.RunID}, &run); err != nil {
			return nil, hostFailure(err)
		}
		var output struct {
			Output    string `json:"output"`
			Size      int64  `json:"size"`
			Truncated bool   `json:"truncated"`
		}
		if err := callHost("history.output", map[string]any{"id": args.RunID, "maxBytes": args.MaxBytes}, &output); err != nil {
			return nil, hostFailure(err)
		}
		return map[string]any{"run": run, "output": output.Output, "size": output.Size, "truncated": output.Truncated}, nil
	case "backup.browse":
		var args struct {
			Path string `json:"path"`
		}
		if err := decodeRequest(request, &args); err != nil {
			return nil, err
		}
		return p.browse(args.Path)
	default:
		return nil, rpcFail("unknown_method", "unknown backup method")
	}
}

func (p *plugin) writable() *rpcError {
	if p.loadError != "" {
		return rpcFail("state_unreadable", p.loadError)
	}
	return nil
}

func (p *plugin) jobFor(request json.RawMessage) (*Job, *rpcError) {
	var args idRequest
	if err := decodeRequest(request, &args); err != nil {
		return nil, err
	}
	if err := p.writable(); err != nil {
		return nil, err
	}
	j := p.find(args.ID)
	if j == nil {
		return nil, rpcFail("not_found", "unknown backup job")
	}
	return j, nil
}

func (p *plugin) repoFor(request json.RawMessage) (*Repository, *rpcError) {
	var args idRequest
	if err := decodeRequest(request, &args); err != nil {
		return nil, err
	}
	if err := p.writable(); err != nil {
		return nil, err
	}
	repo := p.findRepo(args.ID)
	if repo == nil {
		return nil, rpcFail("not_found", "unknown repository")
	}
	return repo, nil
}

// usableRepo returns a repository that can be handed to restic.
func (p *plugin) usableRepo(id string) (*Repository, *rpcError) {
	raw, _ := json.Marshal(idRequest{ID: id})
	repo, err := p.repoFor(raw)
	if err != nil {
		return nil, err
	}
	if e := validateRepository(repo, p.hostOS()); e != nil {
		return nil, rpcFail("invalid_argument", e.Error())
	}
	return repo, nil
}

// save1 creates or updates a job. The previous schedule is restored when
// saving fails.
func (p *plugin) save1(input Job, create bool) (any, *rpcError) {
	if err := p.writable(); err != nil {
		return nil, err
	}
	j := cloneJob(input)
	index := -1
	if create {
		if len(p.jobs) >= maxJobs {
			return nil, rpcFail("limit_reached", "too many backup jobs")
		}
		j.ID = "job-" + itoa(p.next)
	} else {
		for i, existing := range p.jobs {
			if existing.ID == j.ID {
				index = i
			}
		}
		if index < 0 {
			return nil, rpcFail("not_found", "unknown backup job")
		}
		if p.jobs[index].Engine != j.Engine {
			return nil, rpcFail("invalid_argument", "the engine of a job cannot be changed")
		}
		// The repository is chosen once; moving a backup to another
		// repository would orphan its snapshots and retention.
		if old := p.jobs[index].Restic; old != nil && j.Restic != nil && old.RepositoryID != j.Restic.RepositoryID {
			return nil, rpcFail("invalid_argument", "the repository of a backup cannot be changed; delete the backup and create a new one")
		}
	}
	if err := validateJob(&j, p.hostOS()); err != nil {
		return nil, rpcFail("invalid_argument", err.Error())
	}
	if err := p.checkJob(&j); err != nil {
		return nil, rpcFail("invalid_argument", err.Error())
	}
	if err := p.validateSchedule(&j); err != nil {
		return nil, hostFailure(err)
	}
	jobs := append([]*Job(nil), p.jobs...)
	next := p.next
	if create {
		jobs, next = append(jobs, &j), next+1
	} else {
		jobs[index] = &j
	}
	restore := func() {
		if index >= 0 {
			_ = p.applySchedule(p.jobs[index])
		} else {
			p.unregister(j.ID)
		}
	}
	if err := p.applySchedule(&j); err != nil {
		restore()
		return nil, hostFailure(err)
	}
	if err := p.save(jobs, next); err != nil {
		restore()
		return nil, hostFailure(err)
	}
	p.jobs, p.next = jobs, next
	p.runtimeOf(j.ID).err = ""
	p.publish("backup.changed", map[string]any{"id": j.ID})
	return p.view(&j, p.nextRuns()), nil
}

func (p *plugin) remove(j *Job) (any, *rpcError) {
	if p.runtimeOf(j.ID).op != nil {
		return nil, rpcFail("already_running", "stop \""+j.Name+"\" before deleting it")
	}
	jobs := []*Job{}
	for _, existing := range p.jobs {
		if existing.ID != j.ID {
			jobs = append(jobs, existing)
		}
	}
	if err := p.save(jobs, p.next); err != nil {
		return nil, hostFailure(err)
	}
	p.jobs = jobs
	p.unregister(j.ID)
	delete(p.rt, j.ID)
	p.publish("backup.changed", map[string]any{"id": j.ID})
	return map[string]any{"deleted": j.ID}, nil
}

// saveRepository creates or updates a repository. Its location is fixed once
// backups use it, because they own the snapshots stored there.
func (p *plugin) saveRepository(input Repository, create bool) (any, *rpcError) {
	if err := p.writable(); err != nil {
		return nil, err
	}
	repo := input
	normalizeRepository(&repo)
	index := -1
	next := p.nextRepo
	if create {
		if len(p.repos) >= maxRepositories {
			return nil, rpcFail("limit_reached", "too many repositories")
		}
		repo.ID, next = "repo-"+itoa(next), next+1
	} else {
		for i, existing := range p.repos {
			if existing.ID == repo.ID {
				index = i
			}
		}
		if index < 0 {
			return nil, rpcFail("not_found", "unknown repository")
		}
		if p.repos[index].Location != repo.Location && p.jobsUsing(repo.ID) > 0 {
			return nil, rpcFail("invalid_argument", "the location of a repository used by backups cannot be changed")
		}
	}
	if err := validateRepository(&repo, p.hostOS()); err != nil {
		return nil, rpcFail("invalid_argument", err.Error())
	}
	repos := append([]*Repository(nil), p.repos...)
	if create {
		repos = append(repos, &repo)
	} else {
		repos[index] = &repo
	}
	if err := p.saveRepos(repos, next); err != nil {
		return nil, hostFailure(err)
	}
	p.repos, p.nextRepo = repos, next
	p.runtimeOf(repo.ID).err = ""
	p.publish("backup.changed", map[string]any{"id": repo.ID})
	return p.repoView(&repo), nil
}

func (p *plugin) removeRepository(repo *Repository) (any, *rpcError) {
	if p.runtimeOf(repo.ID).op != nil {
		return nil, rpcFail("already_running", "stop \""+repo.Name+"\" before deleting it")
	}
	if n := p.jobsUsing(repo.ID); n > 0 {
		return nil, rpcFail("in_use", "\""+repo.Name+"\" is used by "+itoa(n)+" backup(s); delete them first")
	}
	repos := []*Repository{}
	for _, existing := range p.repos {
		if existing.ID != repo.ID {
			repos = append(repos, existing)
		}
	}
	if err := p.saveRepos(repos, p.nextRepo); err != nil {
		return nil, hostFailure(err)
	}
	p.repos = repos
	delete(p.rt, repo.ID)
	p.publish("backup.changed", map[string]any{"id": repo.ID})
	return map[string]any{"deleted": repo.ID}, nil
}

type snapshot struct {
	ID       string   `json:"id"`
	ShortID  string   `json:"shortId"`
	Time     string   `json:"time"`
	Hostname string   `json:"hostname"`
	Paths    []string `json:"paths"`
	Tags     []string `json:"tags,omitempty"`
	Files    int64    `json:"files,omitempty"`
	Bytes    int64    `json:"bytes,omitempty"`
}

// snapshots lists the repository's snapshots without locking the repository, so it
// also works while a backup runs. It is a bounded call; slow remote
// repositories time out instead of blocking the plugin.
func (p *plugin) snapshots(repo *Repository) (any, *rpcError) {
	env, err := p.environment(repo)
	if err != nil {
		return nil, rpcFail("invalid_argument", err.Error())
	}
	args := append(resticBase(repo), "--no-lock", "snapshots", "--json")
	var result struct {
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		TimedOut bool   `json:"timedOut"`
		Trunc    bool   `json:"stdoutTruncated"`
	}
	command := executable(repo.Executable, "restic", p.hostOS())
	if err := callHost("process.run", map[string]any{"command": command, "args": args, "environment": env, "timeoutSeconds": 100, "maxOutputBytes": 2 << 20, "tailOutput": false}, &result); err != nil {
		if isNotFound(err) {
			return nil, rpcFail("tool_missing", command+" is not installed on this host")
		}
		return nil, hostFailure(err)
	}
	if result.TimedOut {
		return nil, rpcFail("timeout", "the repository did not answer in time")
	}
	if result.ExitCode != 0 {
		return nil, rpcFail("failed", lastLine(result.Stderr, "restic exited with code "+itoa(result.ExitCode)))
	}
	if result.Trunc {
		return nil, rpcFail("failed", "the snapshot list is too large to show")
	}
	var raw []struct {
		ID       string   `json:"id"`
		ShortID  string   `json:"short_id"`
		Time     string   `json:"time"`
		Hostname string   `json:"hostname"`
		Paths    []string `json:"paths"`
		Tags     []string `json:"tags"`
		Summary  *struct {
			TotalFiles int64 `json:"total_files_processed"`
			TotalBytes int64 `json:"total_bytes_processed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &raw); err != nil {
		return nil, rpcFail("failed", "restic returned an unreadable snapshot list")
	}
	out := make([]snapshot, 0, len(raw))
	for _, item := range raw {
		s := snapshot{ID: item.ID, ShortID: item.ShortID, Time: item.Time, Hostname: item.Hostname, Paths: item.Paths, Tags: item.Tags}
		if item.Summary != nil {
			s.Files, s.Bytes = item.Summary.TotalFiles, item.Summary.TotalBytes
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Time > out[b].Time })
	return map[string]any{"snapshots": out}, nil
}

func lastLine(text, fallback string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			if len(line) > 300 {
				line = line[:300]
			}
			return line
		}
	}
	return fallback
}

// restore restores a snapshot into a new or empty folder. It never writes
// into the original location.
func (p *plugin) restore(repo *Repository, snapshotID, target string, include []string) (any, *rpcError) {
	if !validSnapshot(snapshotID) {
		return nil, rpcFail("invalid_argument", "invalid snapshot")
	}
	hostOS := p.hostOS()
	if !absolutePath(target, hostOS) {
		return nil, rpcFail("invalid_argument", "the restore target must be an absolute path")
	}
	for _, j := range p.jobs {
		if j.Restic == nil || j.Restic.RepositoryID != repo.ID {
			continue
		}
		for _, source := range j.Restic.Sources {
			if sameFolder(source, target, hostOS) {
				return nil, rpcFail("invalid_argument", "restore into a new folder outside the backed-up folders")
			}
		}
	}
	if err := validList("paths", include); err != nil {
		return nil, rpcFail("invalid_argument", err.Error())
	}
	if err := p.emptyOrMissing(target); err != nil {
		return nil, err
	}
	runID, err := p.start(repoSubject(repo), kindRestore, restorePlan(repo, hostOS, snapshotID, target, include), true)
	if err != nil {
		return nil, hostFailure(err)
	}
	return map[string]any{"runId": runID}, nil
}

// event handles host callbacks. Unknown events are ignored.
func (p *plugin) event(operation string, payload json.RawMessage) {
	switch operation {
	case "process.exit":
		var exit struct {
			ID         string `json:"id"`
			ExitCode   int    `json:"exitCode"`
			Terminated bool   `json:"terminated"`
			TimedOut   bool   `json:"timedOut"`
		}
		if json.Unmarshal(payload, &exit) == nil {
			p.processExited(exit.ID, exitInfo{code: exit.ExitCode, terminated: exit.Terminated, timedOut: exit.TimedOut})
		}
	case "scheduler.fired":
		var fired struct {
			ID       string `json:"id"`
			Callback string `json:"callback"`
		}
		if json.Unmarshal(payload, &fired) != nil {
			return
		}
		switch fired.Callback {
		case callbackCheck:
			p.reconcile()
		case callbackJob:
			j := p.find(fired.ID)
			if j == nil || p.loadError != "" || !j.Enabled || j.Schedule == nil {
				p.unregister(fired.ID)
				return
			}
			if p.checkJob(j) != nil {
				return
			}
			repo := p.repoOf(j)
			_, _ = p.start(jobSubject(j, repo), kindBackup, backupPlan(j, repo, p.hostOS()), false)
		}
	}
}
