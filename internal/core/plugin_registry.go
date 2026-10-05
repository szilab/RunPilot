package core

import (
	"context"
	"fmt"
	"runtime"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/plugins"
)

func (c *Controller) PluginRegistryURL() string {
	if url := c.config.Snapshot().PluginRegistry.URL; url != "" {
		return url
	}
	return plugins.DefaultRegistryURL
}
func (c *Controller) PluginCatalog(ctx context.Context) ([]plugins.CatalogStatus, error) {
	url := c.PluginRegistryURL()
	catalog, err := plugins.FetchCatalog(ctx, nil, url)
	if err != nil {
		return nil, err
	}
	return catalog.Statuses(runtime.GOOS, c.plugins.Statuses(), url), nil
}
func (c *Controller) InstallCatalogPlugin(ctx context.Context, id, version string) error {
	c.pluginMu.Lock()
	defer c.pluginMu.Unlock()
	url := c.PluginRegistryURL()
	catalog, err := plugins.FetchCatalog(ctx, nil, url)
	if err != nil {
		return err
	}
	for _, p := range catalog.Plugins {
		if p.ID == id {
			for _, v := range p.Versions {
				if v.Version == version {
					if local, ok := c.plugins.Manifest(id); ok && plugins.CompareVersions(version, local.Version) <= 0 {
						return fmt.Errorf("plugin %s is already current or newer", id)
					}
					if _, err := plugins.DownloadAndInstall(ctx, nil, url, c.plugins.Root(), p, v); err != nil {
						return err
					}
					c.plugins.Reload()
					c.plugins.SetRestartRequired(true)
					return nil
				}
			}
		}
	}
	return fmt.Errorf("plugin version is absent from catalog")
}
func (c *Controller) UninstallPlugin(id string) error {
	c.pluginMu.Lock()
	defer c.pluginMu.Unlock()
	if _, ok := c.plugins.Manifest(id); !ok {
		return fmt.Errorf("unknown plugin %q", id)
	}
	if err := c.config.Update(func(cfg *model.Config) error {
		value := false
		if cfg.Plugins == nil {
			cfg.Plugins = map[string]model.PluginSettings{}
		}
		cfg.Plugins[id] = model.PluginSettings{Enabled: &value}
		return nil
	}); err != nil {
		return err
	}
	return c.plugins.Uninstall(id)
}
