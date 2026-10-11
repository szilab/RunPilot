package web

import (
	"testing"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
)

func openReadyController(t *testing.T, dataDir string) (*core.Controller, error) {
	t.Helper()
	store, err := config.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := store.Update(func(c *model.Config) error {
		c.Server.SetupComplete = true
		c.Server.BearerAuthEnabled = true
		return nil
	}); err != nil {
		return nil, err
	}
	return core.Open(dataDir)
}
