// Package rdp provides browser-side IronRDP desktop sessions. The RDP
// protocol stays in the bundled IronRDP WASM client; this provider owns only
// the configured target snapshot and controlled TCP dial capability.
package rdp

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/remote"
)

const ironRDPVersion = "IronRDP web 0.11.0 / RDP 0.7.0"

type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) ID() string { return "rdp" }

func (p *Provider) Status(context.Context) model.RemoteProviderStatus {
	return model.RemoteProviderStatus{
		ID:       p.ID(),
		Name:     "RDP",
		State:    "available",
		Message:  "IronRDP web client embedded",
		Version:  ironRDPVersion,
		Platform: "linux, windows",
		Capabilities: model.RemoteProviderCapabilities{
			DesktopSessions: true,
			Clipboard:       false,
			DynamicResize:   false,
			Fullscreen:      true,
		},
		HTML5Available: true,
	}
}

func (p *Provider) Start(_ context.Context, request remote.StartRequest) (remote.Runtime, error) {
	if request.Target.Type != model.RemoteTargetDesktop {
		return remote.Runtime{}, fmt.Errorf("RDP supports desktop sessions only")
	}
	options, err := model.NormalizeRDPRemoteOptions(request.Target.RDP)
	if err != nil {
		return remote.Runtime{}, err
	}
	address := net.JoinHostPort(options.Host, fmt.Sprintf("%d", options.Port))
	if _, err := netip.ParseAddrPort(address); err != nil {
		// DNS names are expected; JoinHostPort already handles IPv6 literals.
		if _, _, splitErr := net.SplitHostPort(address); splitErr != nil {
			return remote.Runtime{}, fmt.Errorf("invalid RDP address: %w", splitErr)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var once sync.Once
	stop := func(context.Context) error {
		once.Do(func() {
			cancel()
			close(done)
		})
		return nil
	}
	dial := func(callCtx context.Context) (net.Conn, error) {
		combined, stopCall := context.WithCancel(callCtx)
		go func() {
			select {
			case <-ctx.Done():
				stopCall()
			case <-combined.Done():
			}
		}()
		defer stopCall()
		var dialer net.Dialer
		return dialer.DialContext(combined, "tcp", address)
	}
	return remote.Runtime{
		Client: remote.ClientDescriptor{Kind: remote.ClientIronRDP},
		Stop:   stop,
		Done:   done,
		Dial:   dial,
		Log:    func() string { return "RDP target transport is owned by the session-bound IronRDP bridge." },
	}, nil
}
