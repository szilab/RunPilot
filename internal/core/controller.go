package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/szilab/RunPilot/internal/backup"
	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/history"
	"github.com/szilab/RunPilot/internal/jobs"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/processmgr"
	"github.com/szilab/RunPilot/internal/scheduler"
	"github.com/szilab/RunPilot/internal/software"
	"github.com/szilab/RunPilot/internal/storage"
)

type Controller struct {
	dataDir   string
	config    *config.Store
	history   *history.Store
	processes *processmgr.Manager
	jobs      *jobs.Runner
	scheduler *scheduler.Scheduler
	software  *software.Manager
}

func Open(dataDir string) (*Controller, error) {
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	cfg, err := config.Open(dataDir)
	if err != nil {
		return nil, err
	}
	h, err := history.Open(dataDir)
	if err != nil {
		return nil, err
	}
	jr := jobs.New(h)
	c := &Controller{
		dataDir:   dataDir,
		config:    cfg,
		history:   h,
		processes: processmgr.New(h),
		jobs:      jr,
		scheduler: scheduler.New(jr),
		software:  software.NewManager(dataDir),
	}
	snap := cfg.Snapshot()
	c.processes.Reconcile(snap.Processes)
	if err := c.scheduler.Reload(snap.Jobs); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Controller) Start() {
	c.processes.StartAutostart()
}

func (c *Controller) Close() {
	c.scheduler.Stop()
	snap := c.config.Snapshot()
	for _, p := range snap.Processes {
		_ = c.processes.Stop(p.ID)
	}
}

func (c *Controller) DataDir() string        { return c.dataDir }
func (c *Controller) ConfigPath() string     { return c.config.Path() }
func (c *Controller) Snapshot() model.Config { return c.config.Snapshot() }
func (c *Controller) TokenCreated() bool     { return c.config.TokenCreated() }

func (c *Controller) StorageDefinitions() []model.StorageDefinition {
	return c.config.Snapshot().Storage
}
func (c *Controller) UpsertStorage(d model.StorageDefinition) (model.StorageDefinition, error) {
	if strings.TrimSpace(d.Name) == "" {
		return d, fmt.Errorf("storage name is required")
	}
	if d.Type != model.StorageLocal || d.Local == nil {
		return d, fmt.Errorf("storage type local is required")
	}
	if d.Local.Scope == model.LocalStorageScopeHost {
		d.Local.Root = ""
	}
	if _, err := storage.ProviderFor(d); err != nil {
		return d, err
	}
	if d.ID == "" {
		d.ID = config.NewID("storage")
	}
	err := c.config.Update(func(cfg *model.Config) error {
		for i := range cfg.Storage {
			if cfg.Storage[i].ID == d.ID {
				cfg.Storage[i] = d
				return nil
			}
		}
		cfg.Storage = append(cfg.Storage, d)
		return nil
	})
	return d, err
}
func (c *Controller) DeleteStorage(id string) error {
	return c.config.Update(func(cfg *model.Config) error {
		out := cfg.Storage[:0]
		found := false
		for _, d := range cfg.Storage {
			if d.ID == id {
				found = true
				continue
			}
			out = append(out, d)
		}
		if !found {
			return fmt.Errorf("unknown storage %q", id)
		}
		cfg.Storage = out
		return nil
	})
}
func (c *Controller) StorageProvider(id string) (storage.Provider, error) {
	for _, d := range c.config.Snapshot().Storage {
		if d.ID == id {
			return storage.ProviderFor(d)
		}
	}
	return nil, fmt.Errorf("unknown storage %q", id)
}

func (c *Controller) SoftwareDefinitions() []model.SoftwareProviderDefinition {
	return c.config.Snapshot().Software.Providers
}

func (c *Controller) SoftwareProvider(id string) (software.Provider, error) {
	d, err := software.Find(c.SoftwareDefinitions(), id)
	if err != nil {
		return nil, err
	}
	return c.software.Provider(d)
}

// UpsertSoftwareProvider switches the active root when it changes. It never
// migrates, copies, or removes the prior managed Scoop installation.
func (c *Controller) UpsertSoftwareProvider(d model.SoftwareProviderDefinition) (model.SoftwareProviderDefinition, error) {
	d, err := software.ValidateDefinition(c.dataDir, d)
	if err != nil {
		return d, err
	}
	err = c.config.Update(func(cfg *model.Config) error {
		for i := range cfg.Software.Providers {
			if cfg.Software.Providers[i].ID == d.ID {
				cfg.Software.Providers[i] = d
				return nil
			}
		}
		return fmt.Errorf("unknown software provider %q", d.ID)
	})
	return d, err
}

func (c *Controller) ProcessViews() []processmgr.View {
	return c.processes.Views()
}

func (c *Controller) JobViews() []jobs.View {
	return c.jobs.Views(c.config.Snapshot().Jobs)
}

func (c *Controller) Overview() model.Overview {
	processes := c.ProcessViews()
	jobs := c.JobViews()
	overview := model.Overview{
		Host:         platform.HostStatus(),
		ProcessCount: len(processes),
		JobCount:     len(jobs),
		Issues:       []model.HealthIssue{},
	}
	if overview.Host.Error != "" {
		overview.Issues = append(overview.Issues, model.HealthIssue{
			Source:  "host",
			Name:    overview.Host.Hostname,
			Message: overview.Host.Error,
		})
	}
	for _, process := range processes {
		if process.Status.State == "running" || process.Status.State == "starting" || process.Status.State == "stopping" {
			overview.RunningProcesses++
		}
		if process.Status.ExitCode != nil && *process.Status.ExitCode != 0 {
			overview.Issues = append(overview.Issues, model.HealthIssue{
				Source:  "process",
				Name:    process.Definition.Name,
				Message: fmt.Sprintf("last exit code: %d", *process.Status.ExitCode),
			})
		}
	}
	for _, job := range jobs {
		if job.Status.Running {
			overview.RunningJobs++
		}
		if job.Status.LastSuccess != nil && !*job.Status.LastSuccess {
			message := "last run failed"
			if job.Status.LastExitCode != nil {
				message = fmt.Sprintf("last exit code: %d", *job.Status.LastExitCode)
			}
			overview.Issues = append(overview.Issues, model.HealthIssue{
				Source:  string(job.Definition.Type),
				Name:    job.Definition.Name,
				Message: message,
			})
		}
	}
	return overview
}

func (c *Controller) StartProcess(id string) error   { return c.processes.Start(id) }
func (c *Controller) StopProcess(id string) error    { return c.processes.Stop(id) }
func (c *Controller) RestartProcess(id string) error { return c.processes.Restart(id) }

func (c *Controller) UpsertProcess(p model.ProcessDefinition) (model.ProcessDefinition, error) {
	if strings.TrimSpace(p.Name) == "" {
		return p, fmt.Errorf("process name is required")
	}
	if strings.TrimSpace(p.Command.Path) == "" {
		return p, fmt.Errorf("process command path is required")
	}
	if err := model.ValidateCommand(p.Command); err != nil {
		return p, err
	}
	if p.ID == "" {
		p.ID = config.NewID("proc")
	}
	model.NormalizeProcess(&p)
	err := c.config.Update(func(cfg *model.Config) error {
		for i := range cfg.Processes {
			if cfg.Processes[i].ID == p.ID {
				cfg.Processes[i] = p
				return nil
			}
		}
		cfg.Processes = append(cfg.Processes, p)
		return nil
	})
	if err != nil {
		return p, err
	}
	c.processes.Reconcile(c.config.Snapshot().Processes)
	return p, nil
}

func (c *Controller) DeleteProcess(id string) error {
	if err := c.config.Update(func(cfg *model.Config) error {
		out := cfg.Processes[:0]
		found := false
		for _, p := range cfg.Processes {
			if p.ID == id {
				found = true
				continue
			}
			out = append(out, p)
		}
		if !found {
			return fmt.Errorf("unknown process %q", id)
		}
		cfg.Processes = out
		return nil
	}); err != nil {
		return err
	}
	c.processes.Reconcile(c.config.Snapshot().Processes)
	return nil
}

func (c *Controller) UpsertJob(j model.JobDefinition) (model.JobDefinition, error) {
	if strings.TrimSpace(j.Name) == "" {
		return j, fmt.Errorf("job name is required")
	}
	if j.Type != model.JobCommand && j.Type != model.JobBackup {
		return j, fmt.Errorf("job type must be command or backup")
	}
	model.NormalizeJob(&j)
	if _, err := scheduler.Expression(j.Schedule); err != nil {
		return j, err
	}
	switch j.Type {
	case model.JobCommand:
		if j.Command == nil || strings.TrimSpace(j.Command.Path) == "" {
			return j, fmt.Errorf("command job requires a command")
		}
		if err := model.ValidateCommand(*j.Command); err != nil {
			return j, err
		}
		j.Backup = nil
	case model.JobBackup:
		if j.Backup == nil {
			return j, fmt.Errorf("backup job requires a provider configuration")
		}
		if err := backup.Validate(*j.Backup); err != nil {
			return j, err
		}
		if _, err := backup.Build(*j.Backup); err != nil {
			return j, err
		}
		j.Command = nil
	}
	if j.ID == "" {
		j.ID = config.NewID("job")
	}
	if err := c.config.Update(func(cfg *model.Config) error {
		for i := range cfg.Jobs {
			if cfg.Jobs[i].ID == j.ID {
				cfg.Jobs[i] = j
				return nil
			}
		}
		cfg.Jobs = append(cfg.Jobs, j)
		return nil
	}); err != nil {
		return j, err
	}
	if err := c.scheduler.Reload(c.config.Snapshot().Jobs); err != nil {
		return j, err
	}
	return j, nil
}

func (c *Controller) DeleteJob(id string) error {
	if err := c.config.Update(func(cfg *model.Config) error {
		out := cfg.Jobs[:0]
		found := false
		for _, j := range cfg.Jobs {
			if j.ID == id {
				found = true
				continue
			}
			out = append(out, j)
		}
		if !found {
			return fmt.Errorf("unknown job %q", id)
		}
		cfg.Jobs = out
		return nil
	}); err != nil {
		return err
	}
	return c.scheduler.Reload(c.config.Snapshot().Jobs)
}

func (c *Controller) RunJob(id string) (string, error) {
	for _, j := range c.config.Snapshot().Jobs {
		if j.ID == id {
			return c.jobs.Run(j)
		}
	}
	return "", fmt.Errorf("unknown job %q", id)
}

func (c *Controller) RecentRuns(limit int) ([]model.RunRecord, error) {
	return c.history.Recent(limit)
}

func (c *Controller) ProcessLog(id string, lines int) (string, error) {
	p, err := c.processes.LogPath(id)
	if err != nil {
		return "", err
	}
	return history.Tail(p, lines)
}

func (c *Controller) RunLog(runID string, lines int) (string, error) {
	if !safeID(runID) {
		return "", fmt.Errorf("invalid run id")
	}
	path := c.history.RunLogPath(runID)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return history.Tail(path, lines)
}

func safeID(id string) bool {
	if id == "" || id != filepath.Base(id) {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
