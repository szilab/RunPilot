// Package pluginprovider adapts the typed Remote capability protocol to the
// core Remote Provider interface.
package pluginprovider

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/pluginapi"
	"github.com/szilab/RunPilot/internal/plugins"
	"github.com/szilab/RunPilot/internal/remote"
)

type Provider struct {
	pluginID, providerID string
	manager              *plugins.Manager
	guacd                func() model.GuacdConfig
}

func New(manager *plugins.Manager, pluginID, providerID string, guacd func() model.GuacdConfig) *Provider {
	return &Provider{manager: manager, pluginID: pluginID, providerID: providerID, guacd: guacd}
}
func (p *Provider) ID() string { return p.providerID }
func (p *Provider) Status(ctx context.Context) model.RemoteProviderStatus {
	var status model.RemoteProviderStatus
	err := p.manager.Control(ctx, p.pluginID, "POST", "/v1/remote/status", pluginapi.RemoteStatusRequest{Guacd: p.guacd()}, &status)
	if err != nil {
		return model.RemoteProviderStatus{ID: p.providerID, Name: p.providerID, State: "unavailable", Message: err.Error()}
	}
	return status
}
func (p *Provider) Start(ctx context.Context, request remote.StartRequest) (remote.Runtime, error) {
	var response pluginapi.RemoteStartResponse
	err := p.manager.Control(ctx, p.pluginID, "POST", "/v1/remote/sessions", pluginapi.RemoteStartRequest{ID: request.ID, Target: request.Target, DataDir: request.DataDir, Guacd: p.guacd()}, &response)
	if err != nil {
		return remote.Runtime{}, err
	}
	runtime := remote.Runtime{Client: remote.ClientDescriptor{Kind: remote.ClientKind(response.ClientKind)}, Endpoint: response.Endpoint, ClientParams: response.ClientParams, Message: response.Message}
	if response.TransportEndpoint != "" {
		if err := loopback(response.TransportEndpoint); err != nil {
			return remote.Runtime{}, err
		}
		runtime.Dial = func(call context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(call, "tcp", response.TransportEndpoint)
		}
	}
	runtime.Stop = func(call context.Context) error {
		return p.manager.Control(call, p.pluginID, "POST", "/v1/remote/sessions/"+request.ID+"/stop", nil, nil)
	}
	runtime.Log = func() string {
		var body map[string]string
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := p.manager.Control(ctx, p.pluginID, "GET", "/v1/remote/sessions/"+request.ID+"/log", nil, &body); err != nil {
			return err.Error()
		}
		return body["log"]
	}
	runtime.Probe = func(call context.Context) (remote.SessionProbe, error) {
		var body pluginapi.SessionProbe
		err := p.manager.Control(call, p.pluginID, "GET", "/v1/remote/sessions/"+request.ID+"/probe", nil, &body)
		return remote.SessionProbe{WindowCount: body.WindowCount, Message: body.Message}, err
	}
	return runtime, nil
}
func (p *Provider) TestGuacd(ctx context.Context, config model.GuacdConfig) (model.RemoteProviderStatus, error) {
	var status model.RemoteProviderStatus
	err := p.manager.Control(ctx, p.pluginID, "POST", "/v1/remote/status", pluginapi.RemoteStatusRequest{Guacd: config}, &status)
	return status, err
}
func loopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid plugin transport endpoint")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("plugin transport endpoint must be loopback-only")
	}
	return nil
}
