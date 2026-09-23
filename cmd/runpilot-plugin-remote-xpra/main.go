package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/szilab/RunPilot/internal/pluginremote"
	"github.com/szilab/RunPilot/internal/remote/xpra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	_ = pluginremote.New(xpra.New(), "remote.xpra", "1.0.0", []string{"remote-provider"}, nil).Run(ctx)
}
