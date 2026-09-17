package vnc

import (
	"context"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/remote"
)

func TestProviderAllowsOnlyDesktopTargets(t *testing.T) {
	p := New()
	if got := p.Status(context.Background()); got.ID != "vnc" || got.Name != "VNC / noVNC" || got.State != "available" || !got.Capabilities.DesktopSessions || got.Capabilities.ApplicationSessions {
		t.Fatalf("status=%#v", got)
	}
	_, err := p.Start(context.Background(), remote.StartRequest{Target: model.RemoteTarget{Type: model.RemoteTargetApplication, VNC: &model.VNCRemoteOptions{Host: "127.0.0.1"}}})
	if err == nil {
		t.Fatal("application target was accepted")
	}
}
