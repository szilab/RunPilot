package rdp

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/remote"
)

func TestStartWithGuacdKeepsRequestConfigurationsIndependent(t *testing.T) {
	p := New(nil)
	request := remote.StartRequest{Target: model.RemoteTarget{Type: model.RemoteTargetDesktop, RDP: &model.RDPRemoteOptions{Host: "target"}}}
	configs := []model.GuacdConfig{
		{Host: "first-guacd", Port: 4822, ConnectTimeoutSeconds: 5},
		{Host: "second-guacd", Port: 4823, ConnectTimeoutSeconds: 5},
	}
	logs := make([]string, len(configs))
	var group sync.WaitGroup
	for i := range configs {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			runtime, err := p.StartWithGuacd(context.Background(), request, configs[i])
			if err != nil {
				t.Errorf("StartWithGuacd: %v", err)
				return
			}
			logs[i] = runtime.Log()
		}(i)
	}
	group.Wait()
	for i, config := range configs {
		if !strings.Contains(logs[i], config.Host) {
			t.Fatalf("runtime %d used another request's guacd configuration: %q", i, logs[i])
		}
	}
}
