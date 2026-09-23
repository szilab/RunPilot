package main

import (
	"context"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/pluginremote"
	"github.com/szilab/RunPilot/internal/remote/rdp"
	"os/signal"
	"sync"
	"syscall"
)

type configuration struct {
	mu    sync.RWMutex
	value model.GuacdConfig
}

func (c *configuration) Configure(v model.GuacdConfig) { c.mu.Lock(); c.value = v; c.mu.Unlock() }
func (c *configuration) Get() model.GuacdConfig        { c.mu.RLock(); defer c.mu.RUnlock(); return c.value }
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	config := &configuration{value: model.DefaultGuacdConfig()}
	_ = pluginremote.New(rdp.New(config.Get), "remote.rdp", "1.0.0", []string{"remote-provider"}, config).Run(ctx)
}
