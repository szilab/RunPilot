package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/szilab/RunPilot/internal/backup"
	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/dockercompose"
	"github.com/szilab/RunPilot/internal/jobs"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/remote"
	"github.com/szilab/RunPilot/internal/scheduler"
	"github.com/szilab/RunPilot/internal/software"
	"github.com/szilab/RunPilot/internal/storage"
)

// Legacy feature accessors for Storage, Software, Backup jobs, the legacy
// Docker manager and Xpra. Their REST routes are not mounted; the code stays
// until each feature's plugin migration reaches parity.

func (c *Controller) Remote() *remote.Service { return c.remote }

// RemoteTargets retains only legacy Xpra configuration for the internal
// runtime. RDP and VNC definitions are owned by their plugins.
func (c *Controller) RemoteTargets() []model.RemoteTarget {
	targets := c.config.Snapshot().RemoteTargets
	for i := range targets {
		if targets[i].Provider != "xpra" {
			continue
		}
		options, err := model.NormalizeXpraRemoteOptions(targets[i].Xpra)
		if err != nil {
			options = model.DefaultXpraRemoteOptions()
		}
		targets[i].Xpra = &options
	}
	return targets
}

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

func (c *Controller) JobViews() []jobs.View {
	return c.jobs.Views(c.config.Snapshot().Jobs)
}

func (c *Controller) UpsertJob(j model.JobDefinition) (model.JobDefinition, error) {
	if strings.TrimSpace(j.Name) == "" {
		return j, fmt.Errorf("job name is required")
	}
	if j.Type != model.JobBackup {
		return j, fmt.Errorf("only retained Backup jobs are supported")
	}
	model.NormalizeJob(&j)
	if _, err := scheduler.Expression(j.Schedule); err != nil {
		return j, err
	}
	if j.Backup == nil {
		return j, fmt.Errorf("backup job requires a provider configuration")
	}
	if err := backup.Validate(*j.Backup); err != nil {
		return j, err
	}
	if _, err := backup.Build(*j.Backup); err != nil {
		return j, err
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
