package xpra

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

func TestStatusReportsMissingOrAvailableBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only provider behavior")
	}
	missing := New()
	missing.lookPath = func(string) (string, error) { return "", errors.New("missing") }
	if status := missing.Status(context.Background()); status.State != "not-installed" {
		t.Fatalf("missing state=%q", status.State)
	}
	available := New()
	available.lookPath = func(string) (string, error) { return "/usr/bin/xpra", nil }
	available.run = func(context.Context, string, ...string) ([]byte, error) { return []byte("6.3\n"), nil }
	if status := available.Status(context.Background()); status.State != "available" || status.Version != "6.3" {
		t.Fatalf("available=%#v", status)
	}
}
func TestApplicationAndDesktopArgumentsHaveDifferentLifecycles(t *testing.T) {
	application := strings.Join(sessionArgs(model.RemoteTargetApplication, model.RemoteDBusIsolated, "xterm", "1234", "/tmp/xpra", "/usr/bin/dbus-launch", true), " ")
	if !strings.Contains(application, "start ") || !strings.Contains(application, "--start=xterm") || strings.Contains(application, "--start-child=") || strings.Contains(application, "--exit-with-children") || !strings.Contains(application, "--dbus-launch=/usr/bin/dbus-launch") {
		t.Fatalf("application args = %s", application)
	}
	desktop := strings.Join(sessionArgs(model.RemoteTargetDesktop, model.RemoteDBusHost, "startxfce4", "1234", "/tmp/xpra", "/usr/bin/dbus-launch", true), " ")
	if !strings.Contains(desktop, "start-desktop ") || !strings.Contains(desktop, "--start-child=startxfce4") || !strings.Contains(desktop, "--exit-with-children=yes") || !strings.Contains(desktop, "--dbus-launch=no") {
		t.Fatalf("desktop args = %s", desktop)
	}
}
func TestWindowCount(t *testing.T) {
	if count, ok := windowCount("state.windows=2\n"); !ok || count != 2 {
		t.Fatalf("count=%d ok=%v", count, ok)
	}
	if _, ok := windowCount("unrecognized output"); ok {
		t.Fatal("unrecognized output parsed")
	}
}
func TestReadinessTimeoutAndShellQuoting(t *testing.T) {
	if err := waitEndpoint(context.Background(), "127.0.0.1:1", time.Millisecond, nil); err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("readiness error=%v", err)
	}
	if got := shellQuote("it's safe"); got != "'it'\\''s safe'" {
		t.Fatalf("quote=%q", got)
	}
}
