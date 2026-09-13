package xpra

import (
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestChildEnvironmentForwardsDBusOnlyWhenRequested(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	without, err := childEnvironment(model.RemoteTarget{Command: model.CommandSpec{Environment: map[string]string{"CUSTOM": "yes"}}})
	if err != nil || without["DBUS_SESSION_BUS_ADDRESS"] != "" || without["CUSTOM"] != "yes" {
		t.Fatalf("isolated environment = %#v, %v", without, err)
	}
	with, err := childEnvironment(model.RemoteTarget{ForwardDBus: true})
	if err != nil || with["DBUS_SESSION_BUS_ADDRESS"] != "unix:path=/run/user/1000/bus" {
		t.Fatalf("forwarded environment = %#v, %v", with, err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	if _, err := childEnvironment(model.RemoteTarget{ForwardDBus: true}); err == nil {
		t.Fatal("missing D-Bus address was accepted")
	}
}
