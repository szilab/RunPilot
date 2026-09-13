package xpra

import (
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestSessionEnvironmentIsolationAndHostBusMode(t *testing.T) {
	host := []string{"HOME=/home/test", "PATH=/usr/bin", "LANG=en_US.UTF-8", "DISPLAY=:0", "WAYLAND_DISPLAY=wayland-0", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "DBUS_SESSION_BUS_PID=42", "XAUTHORITY=/tmp/Xauthority", "XDG_RUNTIME_DIR=/run/user/1000"}
	without, err := sanitizedServerEnvironment(host, model.RemoteDBusIsolated, "unix:path=/run/user/1000/bus")
	joined := "\n" + strings.Join(without, "\n") + "\n"
	if err != nil || strings.Contains(joined, "\nDISPLAY=") || strings.Contains(joined, "\nWAYLAND_DISPLAY=") || strings.Contains(joined, "\nDBUS_SESSION_BUS_ADDRESS=") || strings.Contains(joined, "\nXAUTHORITY=") || !strings.Contains(joined, "\nHOME=/home/test\n") || !strings.Contains(joined, "\nPATH=/usr/bin\n") || !strings.Contains(joined, "\nXDG_RUNTIME_DIR=/run/user/1000\n") {
		t.Fatalf("isolated environment = %#v, %v", without, err)
	}
	with, err := sanitizedServerEnvironment(host, model.RemoteDBusHost, "unix:path=/run/user/1000/bus")
	if err != nil || environmentValue(with, "DBUS_SESSION_BUS_ADDRESS") != "unix:path=/run/user/1000/bus" {
		t.Fatalf("host-session environment = %#v, %v", with, err)
	}
	if _, err := sanitizedServerEnvironment(host, model.RemoteDBusHost, ""); err == nil {
		t.Fatal("missing D-Bus address was accepted")
	}
	child, err := childEnvironment(model.RemoteTarget{Command: model.CommandSpec{Environment: map[string]string{"CUSTOM": "yes"}}}, "")
	if err != nil || child["CUSTOM"] != "yes" || child["GDK_BACKEND"] != "x11" || child["WAYLAND_DISPLAY"] != "" {
		t.Fatalf("child environment = %#v, %v", child, err)
	}
}
