// Package rdp provides RunPilot's RDP / guacd provider. guacd is the only
// external RDP runtime; no full Guacamole web application is involved.
package rdp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/remote"
)

type Provider struct{ config func() model.GuacdConfig }

func New(config func() model.GuacdConfig) *Provider {
	if config == nil {
		config = model.DefaultGuacdConfig
	}
	return &Provider{config: config}
}
func (p *Provider) ID() string { return "rdp" }
func (p *Provider) settings() (model.GuacdConfig, error) {
	return model.NormalizeGuacdConfig(p.config())
}
func (p *Provider) Status(ctx context.Context) model.RemoteProviderStatus {
	config, err := p.settings()
	if err != nil {
		return model.RemoteProviderStatus{ID: p.ID(), Name: "RDP / Guacamole", State: "configuration invalid", Message: err.Error(), Platform: "linux, windows", HTML5Available: true}
	}
	return ProbeGuacd(ctx, config)
}

// ProbeGuacd validates and performs the lightweight TCP reachability test used
// for provider status and unsaved settings candidates.
func ProbeGuacd(ctx context.Context, config model.GuacdConfig) model.RemoteProviderStatus {
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(config.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(probeCtx, "tcp", address)
	if err != nil {
		return model.RemoteProviderStatus{ID: "rdp", Name: "RDP / Guacamole", State: "unavailable", Message: "Cannot connect to guacd at " + address + ": " + err.Error(), Platform: "linux, windows", HTML5Available: true}
	}
	_ = conn.Close()
	return model.RemoteProviderStatus{ID: "rdp", Name: "RDP / Guacamole", State: "available", Message: "guacd: " + address, Version: "Apache Guacamole guacd", Platform: "linux, windows", HTML5Available: true, Capabilities: model.RemoteProviderCapabilities{DesktopSessions: true, Clipboard: true, DynamicResize: true, Fullscreen: true}}
}

func (p *Provider) Start(_ context.Context, request remote.StartRequest) (remote.Runtime, error) {
	config, err := p.settings()
	if err != nil {
		return remote.Runtime{}, err
	}
	return p.start(request, config)
}

// StartWithGuacd is used only by the plugin control server. It deliberately
// receives its configuration by value so an unsaved probe cannot race or
// mutate the configuration used by an active session.
func (p *Provider) StartWithGuacd(_ context.Context, request remote.StartRequest, config model.GuacdConfig) (remote.Runtime, error) {
	config, err := model.NormalizeGuacdConfig(config)
	if err != nil {
		return remote.Runtime{}, err
	}
	return p.start(request, config)
}
func (p *Provider) ProbeGuacd(ctx context.Context, config model.GuacdConfig) model.RemoteProviderStatus {
	config, err := model.NormalizeGuacdConfig(config)
	if err != nil {
		return model.RemoteProviderStatus{ID: p.ID(), Name: "RDP / Guacamole", State: "configuration invalid", Message: err.Error(), Platform: "linux, windows", HTML5Available: true}
	}
	return ProbeGuacd(ctx, config)
}
func (p *Provider) start(request remote.StartRequest, config model.GuacdConfig) (remote.Runtime, error) {
	if request.Target.Type != model.RemoteTargetDesktop {
		return remote.Runtime{}, fmt.Errorf("RDP supports desktop sessions only")
	}
	if _, err := model.NormalizeRDPRemoteOptions(request.Target.RDP); err != nil {
		return remote.Runtime{}, err
	}
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var once sync.Once
	return remote.Runtime{Client: remote.ClientDescriptor{Kind: remote.ClientGuacamole}, Stop: func(context.Context) error { once.Do(func() { cancel(); close(done) }); return nil }, Done: done, Log: func() string {
		return fmt.Sprintf("RDP / guacd\n  guacd: %s\n  RDP target: %s:%d", address, request.Target.RDP.Host, request.Target.RDP.Port)
	}, Dial: func(callCtx context.Context) (net.Conn, error) {
		combined, stop := context.WithCancel(callCtx)
		defer stop()
		go func() {
			select {
			case <-ctx.Done():
				stop()
			case <-combined.Done():
			}
		}()
		var dialer net.Dialer
		conn, err := dialer.DialContext(combined, "tcp", address)
		if err != nil {
			return nil, fmt.Errorf("cannot connect to guacd at %s: %w", address, err)
		}
		if config.TLS {
			tlsConn := tls.Client(conn, &tls.Config{ServerName: config.Host, MinVersion: tls.VersionTLS12})
			if err := tlsConn.HandshakeContext(combined); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return tlsConn, nil
		}
		return conn, nil
	}}, nil
}
