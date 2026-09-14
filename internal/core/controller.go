package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/szilab/RunPilot/internal/backup"
	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/dockercompose"
	"github.com/szilab/RunPilot/internal/history"
	"github.com/szilab/RunPilot/internal/jobs"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/processmgr"
	"github.com/szilab/RunPilot/internal/remote"
	"github.com/szilab/RunPilot/internal/remote/rdp"
	"github.com/szilab/RunPilot/internal/remote/xpra"
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
	docker    *dockercompose.Manager
	storage   *storage.Registry
	remote    *remote.Service
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
		docker:    dockercompose.NewManager(dataDir),
		remote:    remote.New(dataDir, xpra.New(), rdp.New()),
	}
	if platform.CurrentCapabilities().DockerCompose {
		c.storage = storage.NewRegistry(c.docker)
	} else {
		c.storage = storage.NewRegistry(nil)
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
	c.remote.Close()
	c.scheduler.Stop()
	snap := c.config.Snapshot()
	for _, p := range snap.Processes {
		_ = c.processes.Stop(p.ID)
	}
}

func (c *Controller) Remote() *remote.Service { return c.remote }

func (c *Controller) RemoteTargets() []model.RemoteTarget {
	targets := c.config.Snapshot().RemoteTargets
	for i := range targets {
		switch targets[i].Provider {
		case "xpra":
			options, err := model.NormalizeXpraRemoteOptions(targets[i].Xpra)
			if err != nil {
				// A manually edited invalid legacy YAML value must not make all Remote
				// pages unusable. API writes still reject invalid values below.
				options = model.DefaultXpraRemoteOptions()
			}
			targets[i].Xpra = &options
		case "rdp":
			options, err := model.NormalizeRDPRemoteOptions(targets[i].RDP)
			if err == nil {
				targets[i].RDP = &options
			}
		}
	}
	return targets
}

func (c *Controller) RemoteTarget(id string) (model.RemoteTarget, error) {
	for _, target := range c.RemoteTargets() {
		if target.ID == id {
			return target, nil
		}
	}
	return model.RemoteTarget{}, fmt.Errorf("unknown remote target %q", id)
}

func (c *Controller) UpsertRemoteTarget(target model.RemoteTarget) (model.RemoteTarget, error) {
	if strings.TrimSpace(target.Name) == "" {
		return target, fmt.Errorf("remote target name is required")
	}
	if target.Provider == "" {
		target.Provider = "xpra"
	}
	if target.Type != model.RemoteTargetApplication && target.Type != model.RemoteTargetDesktop {
		return target, fmt.Errorf("remote target type must be application or desktop")
	}
	switch target.Provider {
	case "xpra":
		if strings.TrimSpace(target.Command.Path) == "" {
			return target, fmt.Errorf("remote target command path is required")
		}
		if target.Command.Interpreter != "" && target.Command.Interpreter != "direct" && target.Command.Interpreter != "auto" {
			return target, fmt.Errorf("remote targets require a direct executable command")
		}
		target.Command.Interpreter = "direct"
		if target.DBusMode == "" {
			if target.ForwardDBus {
				target.DBusMode = model.RemoteDBusHost
			} else {
				target.DBusMode = model.RemoteDBusIsolated
			}
		}
		if target.DBusMode != model.RemoteDBusIsolated && target.DBusMode != model.RemoteDBusHost {
			return target, fmt.Errorf("remote target D-Bus mode must be isolated or host-session")
		}
		target.ForwardDBus = false
		options, err := model.NormalizeXpraRemoteOptions(target.Xpra)
		if err != nil {
			return target, err
		}
		target.Xpra = &options
		target.RDP = nil
		if err := model.ValidateCommand(target.Command); err != nil {
			return target, err
		}
	case "rdp":
		if target.Type != model.RemoteTargetDesktop {
			return target, fmt.Errorf("RDP supports desktop sessions only")
		}
		options, err := model.NormalizeRDPRemoteOptions(target.RDP)
		if err != nil {
			return target, err
		}
		target.RDP = &options
		target.Xpra = nil
		target.Command = model.CommandSpec{}
		target.DBusMode = ""
		target.ForwardDBus = false
	default:
		return target, fmt.Errorf("unknown remote provider %q", target.Provider)
	}
	if target.ID == "" {
		target.ID = config.NewID("remote-target")
	}
	err := c.config.Update(func(cfg *model.Config) error {
		for i := range cfg.RemoteTargets {
			if cfg.RemoteTargets[i].ID == target.ID {
				cfg.RemoteTargets[i] = target
				return nil
			}
		}
		cfg.RemoteTargets = append(cfg.RemoteTargets, target)
		return nil
	})
	return target, err
}

func (c *Controller) DeleteRemoteTarget(id string) error {
	return c.config.Update(func(cfg *model.Config) error {
		out := cfg.RemoteTargets[:0]
		found := false
		for _, target := range cfg.RemoteTargets {
			if target.ID == id {
				found = true
				continue
			}
			out = append(out, target)
		}
		if !found {
			return fmt.Errorf("unknown remote target %q", id)
		}
		cfg.RemoteTargets = out
		return nil
	})
}

func (c *Controller) DataDir() string        { return c.dataDir }
func (c *Controller) ConfigPath() string     { return c.config.Path() }
func (c *Controller) Snapshot() model.Config { return c.config.Snapshot() }
func (c *Controller) TokenCreated() bool     { return c.config.TokenCreated() }

func (c *Controller) StorageLocations(ctx context.Context) []storage.Descriptor {
	return c.storage.List(ctx)
}
func (c *Controller) StorageProvider(ctx context.Context, id string) (storage.Provider, error) {
	return c.storage.Provider(ctx, id)
}
func (c *Controller) Docker() *dockercompose.Manager { return c.docker }

func (c *Controller) SoftwareDefinitions() []model.SoftwareProviderDefinition {
	definitions := c.config.Snapshot().Software.Providers
	capabilities := platform.CurrentCapabilities()
	available := make([]model.SoftwareProviderDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Type == model.SoftwareProviderScoop && !capabilities.Scoop {
			continue
		}
		available = append(available, definition)
	}
	return available
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
	overview.TaskCount = overview.ProcessCount + overview.JobCount
	overview.RunningTasks = overview.RunningProcesses + overview.RunningJobs
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
