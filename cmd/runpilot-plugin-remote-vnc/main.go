package main

import (
	"context"
	"github.com/szilab/RunPilot/internal/pluginremote"
	"github.com/szilab/RunPilot/internal/remote/vnc"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	_ = pluginremote.New(vnc.New(), "remote.vnc", "1.0.0", []string{"remote-provider"}, nil).Run(ctx)
}
