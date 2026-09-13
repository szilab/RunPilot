package remote

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

type fakeProvider struct {
	mu     sync.Mutex
	starts int
	fail   error
	done   chan error
}

func (p *fakeProvider) ID() string { return "fake" }
func (p *fakeProvider) Status(context.Context) model.RemoteProviderStatus {
	return model.RemoteProviderStatus{ID: "fake", Name: "Fake", State: "available"}
}
func (p *fakeProvider) Start(context.Context, StartRequest) (Runtime, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts++
	if p.fail != nil {
		return Runtime{}, p.fail
	}
	done := p.done
	if done == nil {
		done = make(chan error, 1)
	}
	return Runtime{Endpoint: "127.0.0.1:12345", Done: done, Stop: func(context.Context) error { done <- nil; return nil }}, nil
}

func target() model.RemoteTarget {
	return model.RemoteTarget{ID: "target", Name: "Test", Provider: "fake", Type: model.RemoteTargetApplication, Enabled: true, Command: model.CommandSpec{Path: "xterm"}}
}
func TestSessionLifecycleAndMultipleSessions(t *testing.T) {
	p := &fakeProvider{}
	service := New(t.TempDir(), p)
	a, err := service.Start(context.Background(), target())
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.Start(context.Background(), target())
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.State != model.RemoteSessionRunning || b.State != model.RemoteSessionRunning {
		t.Fatalf("sessions = %#v %#v", a, b)
	}
	if _, err := service.Endpoint(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Stop(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		value, _ := service.Get(a.ID)
		if value.State == model.RemoteSessionStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state=%s", value.State)
		}
		time.Sleep(time.Millisecond)
	}
	if err := service.Stop(context.Background(), "missing"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("err=%v", err)
	}
}
func TestStartFailureIsRecorded(t *testing.T) {
	service := New(t.TempDir(), &fakeProvider{fail: errors.New("boom")})
	view, err := service.Start(context.Background(), target())
	if err == nil {
		t.Fatal("start succeeded")
	}
	if view.State != model.RemoteSessionFailed || view.Failure != "boom" {
		t.Fatalf("view=%#v", view)
	}
}
