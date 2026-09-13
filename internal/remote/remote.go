// Package remote owns configured graphical targets and their ephemeral
// provider sessions. Providers never leak their listener addresses to callers.
package remote

import (
	"context"
	"errors"
	"fmt"
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

// Runtime belongs solely to a Provider. Endpoint is local-only and is used by
// the HTTP bridge; the public API receives only the session metadata.
type Runtime struct {
	Endpoint string
	// ClientParams are provider-generated, non-sensitive HTML client settings.
	// The web bridge applies them only during the initial authorized redirect.
	ClientParams map[string]string
	Stop         func(context.Context) error
	Done         <-chan error
	Probe        func(context.Context) (SessionProbe, error)
	Log          func() string
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
	dataDir   string
	providers map[string]Provider
	mu        sync.RWMutex
	sessions  map[string]*session
}

type session struct {
	view    model.RemoteSession
	runtime Runtime
}

func New(dataDir string, providers ...Provider) *Service {
	p := make(map[string]Provider, len(providers))
	for _, provider := range providers {
		p[provider.ID()] = provider
	}
	return &Service{dataDir: dataDir, providers: p, sessions: map[string]*session{}}
}

func (s *Service) ProviderStatuses(ctx context.Context) []model.RemoteProviderStatus {
	statuses := make([]model.RemoteProviderStatus, 0, len(s.providers))
	for _, provider := range s.providers {
		statuses = append(statuses, provider.Status(ctx))
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
	view, log := item.view, item.runtime.Log
	s.mu.RUnlock()
	if log == nil {
		return view, "", nil
	}
	return view, log(), nil
}

func (s *Service) ClientParams(id string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item := s.sessions[id]
	if item == nil {
		return nil, ErrUnknownSession
	}
	params := make(map[string]string, len(item.runtime.ClientParams))
	for key, value := range item.runtime.ClientParams {
		params[key] = value
	}
	return params, nil
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
	id := config.NewID("remote")
	now := time.Now().UTC()
	item := &session{view: model.RemoteSession{ID: id, Provider: target.Provider, TargetID: target.ID, TargetName: target.Name, Type: target.Type, State: model.RemoteSessionStarting, CreatedAt: now, Xpra: target.Xpra}}
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
	s.mu.Unlock()
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
