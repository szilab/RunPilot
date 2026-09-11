package platform

import (
	"runtime"
	"testing"
)

func TestCurrentCapabilitiesMatchRuntime(t *testing.T) {
	c := CurrentCapabilities()
	if c.OS != runtime.GOOS {
		t.Fatalf("OS = %q, want %q", c.OS, runtime.GOOS)
	}
	if runtime.GOOS == "windows" && (!c.Windows || c.ServiceManager != "windows-scm" || !c.Robocopy) {
		t.Fatalf("unexpected Windows capabilities: %#v", c)
	}
	if runtime.GOOS == "linux" && (!c.Linux || c.ServiceManager != "systemd" || c.Robocopy) {
		t.Fatalf("unexpected Linux capabilities: %#v", c)
	}
}
