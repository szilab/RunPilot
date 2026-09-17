// Package vnc provides RunPilot's built-in VNC / noVNC provider. It owns no
// daemon: each session dials only its snapshotted VNC target when the browser
// opens the session-scoped RFB WebSocket bridge.
package vnc

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/remote"
)

type Provider struct{}

func New() *Provider           { return &Provider{} }
func (p *Provider) ID() string { return "vnc" }

func (p *Provider) Status(context.Context) model.RemoteProviderStatus {
	return model.RemoteProviderStatus{
		ID:             p.ID(),
		Name:           "VNC / noVNC",
		State:          "available",
		Message:        "Embedded noVNC browser client",
		Version:        "noVNC 1.7.0",
		Platform:       "linux, windows",
		HTML5Available: true,
		Capabilities:   model.RemoteProviderCapabilities{DesktopSessions: true, Clipboard: true, Fullscreen: true},
	}
}

func (p *Provider) Start(_ context.Context, request remote.StartRequest) (remote.Runtime, error) {
	if request.Target.Type != model.RemoteTargetDesktop {
		return remote.Runtime{}, fmt.Errorf("VNC supports desktop sessions only")
	}
	options, err := model.NormalizeVNCRemoteOptions(request.Target.VNC)
	if err != nil {
		return remote.Runtime{}, err
	}
	address := net.JoinHostPort(options.Host, strconv.Itoa(options.Port))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var once sync.Once
	return remote.Runtime{
		Client: remote.ClientDescriptor{Kind: remote.ClientNoVNC},
		Stop: func(context.Context) error {
			once.Do(func() { cancel(); close(done) })
			return nil
		},
		Done: done,
		Log: func() string {
			return fmt.Sprintf("VNC / noVNC\n  VNC target: %s", address)
		},
		Dial: func(callCtx context.Context) (net.Conn, error) {
			dialCtx, stop := context.WithTimeout(callCtx, time.Duration(options.ConnectTimeoutSeconds)*time.Second)
			defer stop()
			stopOnSessionEnd := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					stop()
				case <-stopOnSessionEnd:
				}
			}()
			defer close(stopOnSessionEnd)
			var dialer net.Dialer
			conn, err := dialer.DialContext(dialCtx, "tcp", address)
			if err != nil {
				return nil, fmt.Errorf("cannot connect to VNC server at %s: %w", address, err)
			}
			return conn, nil
		},
	}, nil
}
