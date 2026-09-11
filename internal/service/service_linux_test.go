//go:build linux

package service

import (
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/daemon"
)

func TestUnitContentsIncludesConfiguredRuntime(t *testing.T) {
	unit := unitContents("/opt/Run Pilot/runpilot", "/var/lib/run pilot", daemon.Options{Port: 9080, BasePath: "/runpilot"})
	for _, want := range []string{"ExecStart=\"/opt/Run Pilot/runpilot\" service-run --data-dir \"/var/lib/run pilot\" --port 9080 --base-path \"/runpilot\"", "WorkingDirectory=\"/var/lib/run pilot\"", "WantedBy=multi-user.target"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}
