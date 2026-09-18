//go:build linux

package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/daemon"
)

func TestUnitContentsIncludesConfiguredRuntime(t *testing.T) {
	unit := unitContents("/opt/Run Pilot/runpilot", "/var/lib/run pilot", daemon.Options{Port: 9080, BasePath: "/runpilot"})
	for _, want := range []string{"ExecStart=\"/opt/Run Pilot/runpilot\" service-run --data-dir \"/var/lib/run pilot\" --port 9080 --base-path \"/runpilot\"", "WorkingDirectory=\"/var/lib/run pilot\"", "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "After=network.target") {
		t.Fatalf("user unit must not claim network ordering:\n%s", unit)
	}
	if strings.Contains(unit, "User=") {
		t.Fatalf("user service must not contain User=:\n%s", unit)
	}
}

func TestUserSystemdDirUsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config with spaces"))
	got, err := userSystemdDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd", "user")
	if got != want {
		t.Fatalf("user systemd directory = %q, want %q", got, want)
	}
}

func TestUserSystemdDirFallsBackToConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	oldHome := userHomeDir
	home := filepath.Join(t.TempDir(), "home with spaces")
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = oldHome })
	got, err := userSystemdDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "systemd", "user")
	if got != want {
		t.Fatalf("user systemd directory = %q, want %q", got, want)
	}
}

func TestSystemctlUsesUserManager(t *testing.T) {
	var got []string
	old := runSystemctl
	runSystemctl = func(args ...string) ([]byte, error) {
		got = append([]string(nil), args...)
		return nil, nil
	}
	t.Cleanup(func() { runSystemctl = old })
	if err := systemctl("start", systemdUnit); err != nil {
		t.Fatal(err)
	}
	want := []string{"--user", "start", systemdUnit}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("systemctl args = %#v, want %#v", got, want)
	}
}

func TestInstallSucceedsWhenLingerCannotBeQueried(t *testing.T) {
	oldSystemctl := runSystemctl
	oldLoginctl := runLoginctl
	oldUser := currentUserName
	var commands [][]string
	runSystemctl = func(args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		return nil, nil
	}
	runLoginctl = func(args ...string) ([]byte, error) { return nil, errors.New("loginctl unavailable") }
	currentUserName = func() (string, error) { return "runpilot", nil }
	t.Cleanup(func() {
		runSystemctl = oldSystemctl
		runLoginctl = oldLoginctl
		currentUserName = oldUser
	})
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	if err := Install(filepath.Join(t.TempDir(), "data")); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if len(commands) != 2 {
		t.Fatalf("systemctl calls = %#v, want daemon-reload and enable", commands)
	}
}

func TestLingerEnabledParsesLoginctlOutput(t *testing.T) {
	old := runLoginctl
	runLoginctl = func(args ...string) ([]byte, error) { return []byte("yes\n"), nil }
	t.Cleanup(func() { runLoginctl = old })
	got, err := lingerEnabled("runpilot")
	if err != nil || !got {
		t.Fatalf("lingerEnabled = %v, %v; want true, nil", got, err)
	}
}
