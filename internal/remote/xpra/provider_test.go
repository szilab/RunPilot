package xpra

import (
	"context"
	"errors"
	"net/url"
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
	options := model.DefaultXpraRemoteOptions()
	application := strings.Join(sessionArgs(model.RemoteTargetApplication, model.RemoteDBusIsolated, "xterm", "1234", "/tmp/xpra", "/usr/bin/dbus-launch", true, options), " ")
	if !strings.Contains(application, "start ") || !strings.Contains(application, "--start-after-connect=xterm") || strings.Contains(application, "--start-child=") || strings.Contains(application, "--exit-with-children") || !strings.Contains(application, "--dbus-launch=/usr/bin/dbus-launch") || strings.Contains(application, "--dpi=") {
		t.Fatalf("application args = %s", application)
	}
	desktop := strings.Join(sessionArgs(model.RemoteTargetDesktop, model.RemoteDBusHost, "startxfce4", "1234", "/tmp/xpra", "/usr/bin/dbus-launch", true, options), " ")
	if !strings.Contains(desktop, "start-desktop ") || !strings.Contains(desktop, "--start-child-after-connect=startxfce4") || !strings.Contains(desktop, "--exit-with-children=yes") || !strings.Contains(desktop, "--dbus-launch=no") {
		t.Fatalf("desktop args = %s", desktop)
	}
}

func TestXpraDisplayArgumentsAndClientParameters(t *testing.T) {
	options := model.DefaultXpraRemoteOptions()
	options.DPIMode, options.DPI = model.XpraDPI96, 96
	options.LaunchAfterConnect = boolPointer(false)
	args := strings.Join(sessionArgs(model.RemoteTargetApplication, model.RemoteDBusIsolated, "xterm", "1234", "/tmp/xpra", "/usr/bin/dbus-launch", true, options), " ")
	for _, want := range []string{"--dpi=96", "--start=xterm", "--resize-display=yes", "--clipboard=yes", "--file-transfer=no", "--printing=no", "--speaker=off", "--microphone=off"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q: %s", want, args)
		}
	}
	params := clientParams(options)
	for key, want := range map[string]string{"encoding": "webp", "video": "no", "clipboard": "yes", "sound": "no", "printing": "no", "file_transfer": "no", "floating_menu": "yes", "autohide": "yes", "toolbar_position": "top-left"} {
		if params[key] != want {
			t.Fatalf("parameter %s=%q, want %q", key, params[key], want)
		}
	}
	query := url.Values{}
	for key, value := range params {
		query.Set(key, value)
	}
	if encoded := query.Encode(); strings.Contains(encoded, " ") || !strings.Contains(encoded, "file_transfer=no") {
		t.Fatalf("client query was not encoded safely: %q", encoded)
	}
}

func TestCustomDPIProducesServerArgument(t *testing.T) {
	options := model.DefaultXpraRemoteOptions()
	options.DPIMode, options.DPI = model.XpraDPICustom, 144
	args := strings.Join(sessionArgs(model.RemoteTargetApplication, model.RemoteDBusIsolated, "xterm", "1234", "/tmp/xpra", "/usr/bin/dbus-launch", true, options), " ")
	if !strings.Contains(args, "--dpi=144") {
		t.Fatalf("args = %s", args)
	}
}

func boolPointer(value bool) *bool { return &value }
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
