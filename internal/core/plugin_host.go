package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/plugins"
	"github.com/szilab/RunPilot/internal/scheduler"
	"github.com/szilab/RunPilot/internal/version"
)

// controllerPluginHost implements the plugins.Host capability interfaces on top
// of the controller's owner-scoped managers. Capability names and parameter
// validation live in internal/plugins/capabilities.go.
type controllerPluginHost struct{ controller *Controller }

func (h controllerPluginHost) Log(_ context.Context, message string) error {
	log.Printf("plugin: %s", message)
	return nil
}

func (h controllerPluginHost) SystemStatus(context.Context) (json.RawMessage, error) {
	return json.Marshal(platform.HostStatus())
}

func (h controllerPluginHost) SystemIdentity(context.Context) (json.RawMessage, error) {
	username, uid := serviceIdentity(user.Current, os.Getuid, os.Getenv)
	if username == "" && uid == "" {
		return nil, &plugins.HostFailure{Code: "unavailable", Message: "service account identity is unavailable"}
	}
	identity := struct {
		Username   string `json:"username"`
		UID        string `json:"uid"`
		Deployment string `json:"deployment,omitempty"`
	}{Username: username, UID: uid, Deployment: version.Deployment}
	return json.Marshal(identity)
}

// serviceIdentity names the account RunPilot runs as. A static (non-cgo)
// build cannot resolve an account missing from /etc/passwd, which is common
// for containers started with an arbitrary --user; the numeric UID and $USER
// still identify it.
func serviceIdentity(current func() (*user.User, error), getuid func() int, getenv func(string) string) (username, uid string) {
	if account, err := current(); err == nil {
		return account.Username, account.Uid
	}
	if id := getuid(); id >= 0 {
		uid = strconv.Itoa(id)
	}
	return strings.TrimSpace(getenv("USER")), uid
}

func (h controllerPluginHost) StorageGet(_ context.Context, pluginID, key string) (json.RawMessage, error) {
	return h.controller.pluginStorageGet(pluginID, key)
}

func (h controllerPluginHost) StorageSet(_ context.Context, pluginID, key string, value json.RawMessage) error {
	return h.controller.pluginStorageSet(pluginID, key, value)
}

func (h controllerPluginHost) PublishEvent(_ context.Context, pluginID, event string, data json.RawMessage) error {
	if !h.controller.publishPluginEvent(pluginID, event, data) && strings.HasPrefix(event, "process.session.") {
		return &plugins.HostFailure{Code: "io_error", Message: "interactive process output delivery queue is full"}
	}
	return nil
}

func (h controllerPluginHost) PluginStopped(pluginID string) {
	h.controller.scheduler.RemovePluginOwner(pluginID)
	h.controller.pluginProcesses.stopOwner(pluginID)
	h.controller.pluginSessions.stopOwner(pluginID)
	h.controller.pluginNetworkStreams.stopOwner(pluginID)
	h.controller.browserPublications.stop(pluginID)
}

func (h controllerPluginHost) NetworkStreamOpen(ctx context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.pluginNetworkStreams.open(ctx, owner, raw)
}

func (h controllerPluginHost) NetworkStreamRead(ctx context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.pluginNetworkStreams.read(ctx, owner, raw)
}

func (h controllerPluginHost) NetworkStreamWrite(ctx context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.pluginNetworkStreams.write(ctx, owner, raw)
}

func (h controllerPluginHost) NetworkStreamClose(ctx context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.pluginNetworkStreams.closeStream(owner, raw)
}

func (h controllerPluginHost) ScheduleRegister(_ context.Context, pluginID string, raw json.RawMessage) (json.RawMessage, error) {
	var value struct {
		ID       string             `json:"id"`
		Schedule model.ScheduleSpec `json:"schedule"`
		Callback string             `json:"callback"`
		Data     json.RawMessage    `json:"data"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.Callback) == "" || (len(value.Data) > 0 && !json.Valid(value.Data)) {
		return nil, fmt.Errorf("invalid schedule registration")
	}
	if len(value.Data) == 0 {
		value.Data = json.RawMessage("{}")
	}
	err := h.controller.scheduler.RegisterPlugin(scheduler.PluginSchedule{Owner: pluginID, ID: value.ID, Callback: value.Callback, Schedule: value.Schedule, Data: value.Data}, func(item scheduler.PluginSchedule) {
		h.controller.deliverPluginEvent(pluginID, "scheduler.fired", map[string]any{"id": item.ID, "callback": item.Callback, "data": json.RawMessage(item.Data)})
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"id": value.ID})
}

func (h controllerPluginHost) ScheduleRemove(_ context.Context, pluginID, id string) error {
	if !h.controller.scheduler.RemovePlugin(pluginID, id) {
		return fmt.Errorf("unknown schedule")
	}
	return nil
}

func (h controllerPluginHost) ScheduleList(_ context.Context, pluginID string) (json.RawMessage, error) {
	return json.Marshal(h.controller.scheduler.ListPlugin(pluginID))
}

func (h controllerPluginHost) ProcessStart(_ context.Context, pluginID string, raw json.RawMessage) (json.RawMessage, error) {
	var input pluginProcessStart
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	result, err := h.controller.pluginProcesses.start(pluginID, input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (h controllerPluginHost) ProcessStatus(_ context.Context, pluginID, id string) (json.RawMessage, error) {
	result, err := h.controller.pluginProcesses.status(pluginID, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (h controllerPluginHost) ProcessTerminate(_ context.Context, pluginID, id string) (json.RawMessage, error) {
	result, err := h.controller.pluginProcesses.terminate(pluginID, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (h controllerPluginHost) ProcessSessionCreate(_ context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.pluginSessions.create(owner, raw)
}

func (h controllerPluginHost) ProcessSessionWrite(_ context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.pluginSessions.write(owner, raw)
}

func (h controllerPluginHost) ProcessSessionResize(_ context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.pluginSessions.resize(owner, raw)
}

func (h controllerPluginHost) ProcessSessionStatus(_ context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.pluginSessions.status(owner, raw)
}

func (h controllerPluginHost) ProcessSessionTerminate(_ context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.pluginSessions.terminate(owner, raw)
}

func (c *Controller) pluginStoragePath(pluginID string) (string, error) {
	if !safePluginStorageID(pluginID) {
		return "", fmt.Errorf("invalid plugin ID")
	}
	return filepath.Join(c.dataDir, "plugins", pluginID, "data", "storage.json"), nil
}

func safePluginStorageID(id string) bool {
	if id == "" || id != filepath.Base(id) {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (c *Controller) pluginStorageGet(pluginID, key string) (json.RawMessage, error) {
	path, err := c.pluginStoragePath(pluginID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return json.RawMessage("null"), nil
	}
	if err != nil {
		return nil, err
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("read plugin storage: %w", err)
	}
	if value, ok := values[key]; ok {
		return append(json.RawMessage(nil), value...), nil
	}
	return json.RawMessage("null"), nil
}

func (c *Controller) pluginStorageSet(pluginID, key string, value json.RawMessage) error {
	path, err := c.pluginStoragePath(pluginID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	values := map[string]json.RawMessage{}
	if existing, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(existing, &values); err != nil {
			return fmt.Errorf("read plugin storage: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	values[key] = append(json.RawMessage(nil), value...)
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
