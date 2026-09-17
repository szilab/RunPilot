// Package remote owns configured graphical targets and their ephemeral
// provider sessions. Providers never leak their listener addresses to callers.
package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
)

var (
	ErrUnknownProvider = errors.New("unknown remote provider")
	ErrUnknownSession  = errors.New("unknown remote session")
)

type StartRequest struct {
	ID      string
	Target  model.RemoteTarget
	DataDir string
}

type ClientKind string

const (
	ClientXpraHTML5 ClientKind = "xpra-html5"
	ClientGuacamole ClientKind = "guacamole"
	ClientNoVNC     ClientKind = "novnc"
)

// ClientDescriptor tells the web layer how to render a provider client. It
// carries client-safe metadata only; provider endpoints stay internal.
type ClientDescriptor struct {
	Kind   ClientKind
	Params map[string]string
}

// Runtime belongs solely to a Provider. Endpoint is local-only and used by
// Xpra's HTTP bridge; Dial is used by transport-oriented clients such as RDP.
type Runtime struct {
	Client   ClientDescriptor
	Endpoint string
	// ClientParams are provider-generated, non-sensitive HTML client settings.
	// The web bridge applies them only during the initial authorized redirect.
	ClientParams map[string]string
	Stop         func(context.Context) error
	Done         <-chan error
	Probe        func(context.Context) (SessionProbe, error)
	Log          func() string
	Dial         func(context.Context) (net.Conn, error)
	Message      string
}

// SessionProbe contains provider-neutral runtime observations. It deliberately
// reports visibility rather than attempting to interpret an application's UI.
type SessionProbe struct {
	WindowCount int
	Message     string
}

type Provider interface {
	ID() string
	Status(context.Context) model.RemoteProviderStatus
	Start(context.Context, StartRequest) (Runtime, error)
}

type Service struct {
	dataDir       string
	providers     map[string]Provider
	providerOrder []string
	mu            sync.RWMutex
	sessions      map[string]*session
}

type session struct {
	view        model.RemoteSession
	runtime     Runtime
	connections map[net.Conn]struct{}
	diagnostics []string
}

func New(dataDir string, providers ...Provider) *Service {
	p := make(map[string]Provider, len(providers))
	order := make([]string, 0, len(providers))
	for _, provider := range providers {
		if _, exists := p[provider.ID()]; !exists {
			order = append(order, provider.ID())
		}
		p[provider.ID()] = provider
	}
	return &Service{dataDir: dataDir, providers: p, providerOrder: order, sessions: map[string]*session{}}
}

func (s *Service) ProviderStatuses(ctx context.Context) []model.RemoteProviderStatus {
	statuses := make([]model.RemoteProviderStatus, 0, len(s.providerOrder))
	for _, id := range s.providerOrder {
		statuses = append(statuses, s.providers[id].Status(ctx))
	}
	return statuses
}

func (s *Service) Sessions() []model.RemoteSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.RemoteSession, 0, len(s.sessions))
	for _, item := range s.sessions {
		out = append(out, item.view)
	}
	return out
}

func (s *Service) Diagnostics(id string) (model.RemoteSession, string, error) {
	s.mu.RLock()
	item := s.sessions[id]
	if item == nil {
		s.mu.RUnlock()
		return model.RemoteSession{}, "", ErrUnknownSession
	}
	view, log, diagnostics := item.view, item.runtime.Log, append([]string(nil), item.diagnostics...)
	s.mu.RUnlock()
	base := ""
	if log != nil {
		base = log()
	}
	if len(diagnostics) == 0 {
		return view, base, nil
	}
	if base != "" {
		base += "\n\n"
	}
	return view, base + strings.Join(diagnostics, "\n"), nil
}

// AddDiagnostic records credential-safe session setup progress. Callers must
// never pass credentials, tickets, cookies, or other secrets here.
func (s *Service) AddDiagnostic(id, line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if item := s.sessions[id]; item != nil {
		item.diagnostics = append(item.diagnostics, line)
	}
}

func (s *Service) ClientParams(id string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item := s.sessions[id]
	if item == nil {
		return nil, ErrUnknownSession
	}
	params := make(map[string]string, len(item.runtime.Client.Params)+len(item.runtime.ClientParams))
	for key, value := range item.runtime.Client.Params {
		params[key] = value
	}
	for key, value := range item.runtime.ClientParams {
		params[key] = value
	}
	return params, nil
}

func (s *Service) Client(id string) (ClientDescriptor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item := s.sessions[id]
	if item == nil {
		return ClientDescriptor{}, ErrUnknownSession
	}
	if item.view.State != model.RemoteSessionRunning {
		return ClientDescriptor{}, fmt.Errorf("remote session is not running")
	}
	client := item.runtime.Client
	client.Params = make(map[string]string, len(client.Params))
	for key, value := range item.runtime.Client.Params {
		client.Params[key] = value
	}
	return client, nil
}

// Dial opens the provider-controlled transport bound to a session snapshot.
// Callers never supply a destination and cannot repurpose RunPilot as a TCP proxy.
func (s *Service) Dial(ctx context.Context, id string) (net.Conn, error) {
	s.mu.RLock()
	item := s.sessions[id]
	if item == nil {
		s.mu.RUnlock()
		return nil, ErrUnknownSession
	}
	if item.view.State != model.RemoteSessionRunning || item.runtime.Dial == nil {
		s.mu.RUnlock()
		return nil, fmt.Errorf("remote session has no transport")
	}
	dial := item.runtime.Dial
	s.mu.RUnlock()
	conn, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	item = s.sessions[id]
	if item == nil || item.view.State != model.RemoteSessionRunning {
		s.mu.Unlock()
		_ = conn.Close()
		return nil, fmt.Errorf("remote session is not running")
	}
	item.connections[conn] = struct{}{}
	s.mu.Unlock()
	return conn, nil
}

func (s *Service) ReleaseDial(id string, conn net.Conn) {
	if conn == nil {
		return
	}
	s.mu.Lock()
	if item := s.sessions[id]; item != nil {
		delete(item.connections, conn)
	}
	s.mu.Unlock()
	_ = conn.Close()
}

func (s *Service) Start(ctx context.Context, target model.RemoteTarget) (model.RemoteSession, error) {
	provider := s.providers[target.Provider]
	if provider == nil {
		return model.RemoteSession{}, fmt.Errorf("%w: %s", ErrUnknownProvider, target.Provider)
	}
	status := provider.Status(ctx)
	if status.State != "available" {
		return model.RemoteSession{}, fmt.Errorf("provider %s is %s%s", status.Name, status.State, suffix(status.Message))
	}
	if target.Provider == "xpra" {
		options, err := model.NormalizeXpraRemoteOptions(target.Xpra)
		if err != nil {
			return model.RemoteSession{}, err
		}
		target.Xpra = &options
	}
	if target.Provider == "rdp" {
		options, err := model.NormalizeRDPRemoteOptions(target.RDP)
		if err != nil {
			return model.RemoteSession{}, err
		}
		target.RDP = &options
	}
	if target.Provider == "vnc" {
		options, err := model.NormalizeVNCRemoteOptions(target.VNC)
		if err != nil {
			return model.RemoteSession{}, err
		}
		target.VNC = &options
	}
	id := config.NewID("remote")
	now := time.Now().UTC()
	item := &session{view: model.RemoteSession{ID: id, Provider: target.Provider, TargetID: target.ID, TargetName: target.Name, Type: target.Type, State: model.RemoteSessionStarting, CreatedAt: now, Xpra: target.Xpra, RDP: target.RDP, VNC: target.VNC}, connections: map[net.Conn]struct{}{}}
	s.mu.Lock()
	s.sessions[id] = item
	s.mu.Unlock()
	runtime, err := provider.Start(ctx, StartRequest{ID: id, Target: target, DataDir: s.dataDir})
	if err != nil {
		s.finishFailed(id, err)
		view, _ := s.Get(id)
		return view, err
	}
	s.mu.Lock()
	item.runtime = runtime
	started := time.Now().UTC()
	item.view.StartedAt = &started
	item.view.State = model.RemoteSessionRunning
	item.view.Message = runtime.Message
	view := item.view
	s.mu.Unlock()
	go s.watch(id, runtime.Done)
	go s.monitor(id, runtime.Probe)
	return view, nil
}

func (s *Service) monitor(id string, probe func(context.Context) (SessionProbe, error)) {
	if probe == nil {
		return
	}
	check := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		result, err := probe(ctx)
		if err != nil {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		item := s.sessions[id]
		if item == nil || item.view.State != model.RemoteSessionRunning {
			return
		}
		count := result.WindowCount
		item.view.WindowCount = &count
		item.view.Message = result.Message
	}
	check()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.RLock()
		item := s.sessions[id]
		running := item != nil && item.view.State == model.RemoteSessionRunning
		s.mu.RUnlock()
		if !running {
			return
		}
		check()
	}
}

func suffix(value string) string {
	if value == "" {
		return ""
	}
	return ": " + value
}

func (s *Service) Get(id string) (model.RemoteSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item := s.sessions[id]
	if item == nil {
		return model.RemoteSession{}, ErrUnknownSession
	}
	return item.view, nil
}

func (s *Service) Stop(ctx context.Context, id string) error {
	s.mu.Lock()
	item := s.sessions[id]
	if item == nil {
		s.mu.Unlock()
		return ErrUnknownSession
	}
	if item.view.State != model.RemoteSessionRunning && item.view.State != model.RemoteSessionStarting {
		s.mu.Unlock()
		return nil
	}
	item.view.State = model.RemoteSessionStopping
	stop := item.runtime.Stop
	connections := make([]net.Conn, 0, len(item.connections))
	for conn := range item.connections {
		connections = append(connections, conn)
	}
	s.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	if stop == nil {
		s.finishStopped(id)
		return nil
	}
	if err := stop(ctx); err != nil {
		s.finishFailed(id, err)
		return err
	}
	return nil
}

func (s *Service) Endpoint(id string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item := s.sessions[id]
	if item == nil {
		return "", ErrUnknownSession
	}
	if item.view.State != model.RemoteSessionRunning || item.runtime.Endpoint == "" {
		return "", fmt.Errorf("remote session is not running")
	}
	return item.runtime.Endpoint, nil
}

func (s *Service) watch(id string, done <-chan error) {
	if done == nil {
		return
	}
	err, ok := <-done
	if !ok {
		err = nil
	}
	if err != nil {
		s.finishFailed(id, err)
	} else {
		s.finishStopped(id)
	}
}
func (s *Service) finishFailed(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.sessions[id]
	if item == nil || item.view.State == model.RemoteSessionStopped {
		return
	}
	item.view.State = model.RemoteSessionFailed
	item.view.Failure = err.Error()
	now := time.Now().UTC()
	item.view.StoppedAt = &now
}
func (s *Service) finishStopped(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.sessions[id]
	if item == nil || item.view.State == model.RemoteSessionFailed {
		return
	}
	item.view.State = model.RemoteSessionStopped
	now := time.Now().UTC()
	item.view.StoppedAt = &now
}
func (s *Service) Close() {
	s.mu.RLock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, id := range ids {
		_ = s.Stop(ctx, id)
	}
}
