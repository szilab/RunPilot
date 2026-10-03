package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/backup"
	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/dockercompose"
	"github.com/szilab/RunPilot/internal/history"
	"github.com/szilab/RunPilot/internal/jobs"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/plugins"
	"github.com/szilab/RunPilot/internal/processmgr"
	"github.com/szilab/RunPilot/internal/remote"
	"github.com/szilab/RunPilot/internal/remote/rdp"
	"github.com/szilab/RunPilot/internal/remote/vnc"
	"github.com/szilab/RunPilot/internal/remote/xpra"
	"github.com/szilab/RunPilot/internal/scheduler"
	"github.com/szilab/RunPilot/internal/software"
	"github.com/szilab/RunPilot/internal/storage"
)

type Controller struct {
	dataDir              string
	config               *config.Store
	history              *history.Store
	processes            *processmgr.Manager
	jobs                 *jobs.Runner
	scheduler            *scheduler.Scheduler
	software             *software.Manager
	docker               *dockercompose.Manager
	storage              *storage.Registry
	remote               *remote.Service
	plugins              *plugins.Manager
	pluginMu             sync.Mutex
	runtimeMu            sync.RWMutex
	runtimes             map[string]*plugins.Runtime
	pluginProcesses      *pluginProcessManager
	pluginSessions       *pluginSessionManager
	pluginNetworkStreams *pluginNetworkStreamManager
	eventMu              sync.RWMutex
	eventSubscribers     map[uint64]chan plugins.Event
	nextEventSubscriber  uint64
	// loading holds a channel per plugin whose backend is initializing; events
	// for it wait for the load to finish instead of being lost.
	loading     map[string]chan struct{}
	queueMu     sync.Mutex
	eventQueues map[string]chan queuedPluginEvent
	stopEvents  chan struct{}
}

type queuedPluginEvent struct {
	event string
	data  any
}

const (
	pluginEventQueueSize   = 256
	pluginEventEnqueueWait = 10 * time.Second
	pluginLoadEventWait    = 30 * time.Second
)

func Open(dataDir string) (*Controller, error) {
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	cfg, err := config.Open(dataDir)
	if err != nil {
		return nil, err
	}
	h, err := history.Open(dataDir)
	if err != nil {
		return nil, err
	}
	jr := jobs.New(h)
	c := &Controller{
		dataDir:          dataDir,
		config:           cfg,
		history:          h,
		processes:        processmgr.New(h),
		jobs:             jr,
		scheduler:        scheduler.New(jr),
		eventSubscribers: map[uint64]chan plugins.Event{},
		loading:          map[string]chan struct{}{},
		eventQueues:      map[string]chan queuedPluginEvent{},
		stopEvents:       make(chan struct{}),
		software:         software.NewManager(dataDir),
		docker:           dockercompose.NewManager(dataDir),
	}
	c.pluginProcesses = newPluginProcessManager(func(owner, event string, data any) { c.deliverPluginEvent(owner, event, data) })
	c.pluginSessions = newPluginSessionManager(c.deliverPluginEvent)
	c.pluginNetworkStreams = newPluginNetworkStreamManager(func(owner, id string) {
		c.deliverPluginEvent(owner, "network.stream.closed", map[string]string{"id": id})
	})
	c.pluginProcesses.history = h
	c.pluginProcesses.publish = func(owner, event string, data any) {
		if raw, err := json.Marshal(data); err == nil {
			c.publishPluginEvent(owner, event, raw)
		}
	}
	c.plugins = plugins.New(dataDir, func(id string) (bool, bool) {
		setting, ok := c.config.Snapshot().Plugins[id]
		return boolValue(setting.Enabled), ok && setting.Enabled != nil
	})
	removedSystemFixture, err := plugins.CleanupObsoleteSystemFixture(c.plugins.Root())
	if err != nil {
		return nil, err
	}
	if removedSystemFixture {
		if err := c.config.Update(func(cfg *model.Config) error {
			if setting, ok := cfg.Plugins["system"]; ok && boolValue(setting.Enabled) {
				disabled := false
				setting.Enabled = &disabled
				cfg.Plugins["system"] = setting
			}
			return nil
		}); err != nil {
			return nil, err
		}
		log.Printf("removed obsolete automatically installed System fixture")
	}
	for _, pluginErr := range c.plugins.Reload() {
		log.Printf("plugin discovery: %v", pluginErr)
	}
	// Remote remains a working core feature until its plugin migration reaches
	// parity. Phase 1 freezes framework contracts only; it must not make the
	// existing providers disappear from fresh installations.
	c.remote = remote.New(dataDir, xpra.New(), rdp.New(c.GuacdConfig), vnc.New())
	if err := c.loadPluginRuntimes(); err != nil {
		return nil, err
	}
	c.plugins.FreezeActivation()
	if platform.CurrentCapabilities().DockerCompose {
		c.storage = storage.NewRegistry(c.docker)
	} else {
		c.storage = storage.NewRegistry(nil)
	}
	snap := cfg.Snapshot()
	c.processes.Reconcile(snap.Processes)
	if err := c.scheduler.Reload(snap.Jobs); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Controller) Start() {
	c.processes.StartAutostart()
}

func (c *Controller) Close() {
	// Plugins shut down first so they can stop and record their own work; the
	// host then terminates anything they left behind and flushes captured logs
	// before the history store closes.
	c.runtimeMu.Lock()
	runtimes := c.runtimes
	c.runtimes = nil
	c.runtimeMu.Unlock()
	for _, runtime := range runtimes {
		_ = runtime.Close(context.Background())
	}
	c.pluginProcesses.close()
	c.pluginSessions.close()
	c.pluginNetworkStreams.close()
	close(c.stopEvents)
	c.remote.Close()
	c.scheduler.Stop()
	snap := c.config.Snapshot()
	for _, p := range snap.Processes {
		_ = c.processes.Stop(p.ID)
	}
	_ = c.history.Close()
}

// SubscribePluginEvents is the shared browser transport's bounded fanout.
// Subscribers receive no replay; disconnected and slow clients never retain
// an unbounded plugin event backlog.
func (c *Controller) SubscribePluginEvents() (<-chan plugins.Event, func()) {
	c.eventMu.Lock()
	id := c.nextEventSubscriber
	c.nextEventSubscriber++
	ch := make(chan plugins.Event, 64)
	c.eventSubscribers[id] = ch
	c.eventMu.Unlock()
	return ch, func() {
		c.eventMu.Lock()
		if current := c.eventSubscribers[id]; current != nil {
			delete(c.eventSubscribers, id)
			close(current)
		}
		c.eventMu.Unlock()
	}
}
func (c *Controller) publishPluginEvent(plugin, event string, data json.RawMessage) bool {
	item := plugins.Event{Plugin: plugin, Event: event, Data: append(json.RawMessage(nil), data...)}
	c.eventMu.RLock()
	defer c.eventMu.RUnlock()
	strict := strings.HasPrefix(event, "process.session.")
	if strict && len(c.eventSubscribers) == 0 {
		return false
	}
	delivered := true
	for _, ch := range c.eventSubscribers {
		select {
		case ch <- item:
		default:
			if strict {
				delivered = false
			}
		}
	}
	return delivered
}
func (c *Controller) deliverPluginEvent(plugin, event string, data any) bool {
	item := queuedPluginEvent{event: event, data: data}
	queue := c.pluginEventQueue(plugin)
	if isPluginOutputEvent(event) {
		select {
		case queue <- item:
			return true
		default: // ordinary process output is lossy; session output treats this as fatal.
			return false
		}
	}
	timer := time.NewTimer(pluginEventEnqueueWait)
	defer timer.Stop()
	select {
	case queue <- item:
		return true
	case <-timer.C:
		log.Printf("plugin %s event %s dropped: delivery queue full", plugin, event)
		return false
	case <-c.stopEvents:
		return false
	}
}

func isPluginOutputEvent(event string) bool {
	return strings.HasPrefix(event, "process.stdout") || strings.HasPrefix(event, "process.stderr") || event == "process.session.output"
}

// pluginEventQueue returns the plugin's bounded, ordered delivery queue. One
// worker per plugin keeps events (for example last output before exit) in the
// order the host produced them without a goroutine per event.
func (c *Controller) pluginEventQueue(plugin string) chan queuedPluginEvent {
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	if queue := c.eventQueues[plugin]; queue != nil {
		return queue
	}
	queue := make(chan queuedPluginEvent, pluginEventQueueSize)
	c.eventQueues[plugin] = queue
	go func() {
		for {
			select {
			case item := <-queue:
				c.runPluginEvent(plugin, item)
			case <-c.stopEvents:
				return
			}
		}
	}()
	return queue
}

func (c *Controller) runPluginEvent(plugin string, item queuedPluginEvent) {
	runtime := c.awaitPluginRuntime(plugin)
	if runtime == nil {
		return
	}
	timeout := plugins.DefaultCallTimeout
	if isPluginOutputEvent(item.event) {
		timeout = 250 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := runtime.Event(ctx, item.event, item.data); err != nil {
		log.Printf("plugin %s event %s: %v", plugin, item.event, err)
		if item.event == "process.session.output" {
			if data, ok := item.data.(map[string]any); ok {
				if id, ok := data["id"].(string); ok {
					c.pluginSessions.deliveryFailed(plugin, id)
				}
			}
		}
	}
}

// awaitPluginRuntime returns the loaded runtime, waiting for a backend that is
// still initializing (it may already have started processes or schedules).
func (c *Controller) awaitPluginRuntime(plugin string) *plugins.Runtime {
	c.runtimeMu.RLock()
	runtime, ready := c.runtimes[plugin], c.loading[plugin]
	c.runtimeMu.RUnlock()
	if runtime != nil || ready == nil {
		return runtime
	}
	timer := time.NewTimer(pluginLoadEventWait)
	defer timer.Stop()
	select {
	case <-ready:
	case <-timer.C:
	case <-c.stopEvents:
		return nil
	}
	c.runtimeMu.RLock()
	defer c.runtimeMu.RUnlock()
	return c.runtimes[plugin]
}

func (c *Controller) Remote() *remote.Service   { return c.remote }
func (c *Controller) Plugins() *plugins.Manager { return c.plugins }

func boolValue(value *bool) bool { return value != nil && *value }

// SetPluginEnabled persists the desired state. Plugin activation is deliberately
// restart-only so a running process never changes its loaded module set.
func (c *Controller) SetPluginEnabled(id string, enabled bool) error {
	c.pluginMu.Lock()
	defer c.pluginMu.Unlock()
	manifest, ok := c.plugins.Manifest(id)
	if !ok {
		return fmt.Errorf("unknown plugin %q", id)
	}
	if enabled {
		if err := manifest.CompatibilityError(runtime.GOOS); err != nil {
			return err
		}
	}
	value := enabled
	if err := c.config.Update(func(cfg *model.Config) error {
		if cfg.Plugins == nil {
			cfg.Plugins = map[string]model.PluginSettings{}
		}
		cfg.Plugins[id] = model.PluginSettings{Enabled: &value}
		return nil
	}); err != nil {
		return err
	}
	c.plugins.SetRestartRequired(true)
	return nil
}

// RescanPlugins refreshes installed packages without changing active runtimes.
func (c *Controller) RescanPlugins() []error {
	c.pluginMu.Lock()
	defer c.pluginMu.Unlock()
	errs := c.plugins.Reload()
	c.plugins.SetRestartRequired(true)
	return errs
}

// PluginCall is the transport-independent route into an enabled WASM plugin.
func (c *Controller) PluginCall(ctx context.Context, id, method string, params json.RawMessage) (json.RawMessage, *plugins.ProtocolError) {
	c.runtimeMu.RLock()
	runtime := c.runtimes[id]
	c.runtimeMu.RUnlock()
	if runtime == nil {
		return nil, &plugins.ProtocolError{Code: "unknown_plugin", Message: "plugin is not loaded"}
	}
	var result json.RawMessage
	if err := runtime.Call(ctx, method, params, &result); err != nil {
		return nil, &plugins.ProtocolError{Code: "plugin_failure", Message: err.Error()}
	}
	return result, nil
}

func (c *Controller) loadPluginRuntimes() error {
	loaded := map[string]*plugins.Runtime{}
	attempted := map[string]chan struct{}{}
	for _, status := range c.plugins.Statuses() {
		if !status.Enabled || status.State == plugins.StateIncompatible || status.Manifest.Backend == nil {
			continue
		}
		dir, ok := c.plugins.PackageDir(status.Manifest.ID)
		if !ok {
			continue
		}
		ready := make(chan struct{})
		attempted[status.Manifest.ID] = ready
		c.runtimeMu.Lock()
		c.loading[status.Manifest.ID] = ready
		c.runtimeMu.Unlock()
		runtime, err := plugins.LoadRuntime(context.Background(), dir, status.Manifest, controllerPluginHost{controller: c})
		if err != nil {
			c.plugins.SetFailure(status.Manifest.ID, err)
			log.Printf("load plugin %q: %v", status.Manifest.ID, err)
			continue
		}
		loaded[status.Manifest.ID] = runtime
	}
	c.runtimeMu.Lock()
	old := c.runtimes
	c.runtimes = loaded
	for id, ready := range attempted {
		delete(c.loading, id)
		close(ready)
	}
	c.runtimeMu.Unlock()
	for _, runtime := range old {
		_ = runtime.Close(context.Background())
	}
	return nil
}

type controllerPluginHost struct{ controller *Controller }

func (h controllerPluginHost) Log(_ context.Context, message string) error {
	log.Printf("plugin: %s", message)
	return nil
}
func (h controllerPluginHost) ConfigGet(context.Context, string) (json.RawMessage, error) {
	return nil, fmt.Errorf("plugin configuration is not implemented")
}
func (h controllerPluginHost) ConfigSet(context.Context, string, json.RawMessage) error {
	return fmt.Errorf("plugin configuration is not implemented")
}
func (h controllerPluginHost) SystemStatus(context.Context) (json.RawMessage, error) {
	return json.Marshal(platform.HostStatus())
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

// AttachPluginNetworkStream claims an owned stream for one browser WebSocket.
func (c *Controller) AttachPluginNetworkStream(owner, id string) (net.Conn, func(), error) {
	return c.pluginNetworkStreams.attach(owner, id)
}

// OpenPluginNetworkStream is the controller-side equivalent of the generic
// network.stream.open capability, used by trusted host integrations.
func (c *Controller) OpenPluginNetworkStream(ctx context.Context, owner string, params json.RawMessage) (json.RawMessage, error) {
	return c.pluginNetworkStreams.open(ctx, owner, params)
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
	return filepath.Join(c.dataDir, "plugin-data", pluginID, "storage.json"), nil
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

func (c *Controller) GuacdConfig() model.GuacdConfig {
	value, err := model.NormalizeGuacdConfig(c.config.Snapshot().Remote.Guacd)
	if err != nil {
		return c.config.Snapshot().Remote.Guacd
	}
	return value
}

func (c *Controller) UpdateGuacdConfig(value model.GuacdConfig) (model.GuacdConfig, error) {
	if strings.TrimSpace(value.Host) == "" {
		return value, fmt.Errorf("guacd host is required")
	}
	value, err := model.NormalizeGuacdConfig(value)
	if err != nil {
		return value, err
	}
	err = c.config.Update(func(cfg *model.Config) error { cfg.Remote.Guacd = value; return nil })
	return value, err
}

func (c *Controller) TestGuacdConfig(ctx context.Context, value model.GuacdConfig) (model.RemoteProviderStatus, error) {
	if strings.TrimSpace(value.Host) == "" {
		return model.RemoteProviderStatus{}, fmt.Errorf("guacd host is required")
	}
	value, err := model.NormalizeGuacdConfig(value)
	if err != nil {
		return model.RemoteProviderStatus{}, err
	}
	return rdp.ProbeGuacd(ctx, value), nil
}

func (c *Controller) RemoteTargets() []model.RemoteTarget {
	targets := c.config.Snapshot().RemoteTargets
	for i := range targets {
		switch targets[i].Provider {
		case "xpra":
			options, err := model.NormalizeXpraRemoteOptions(targets[i].Xpra)
			if err != nil {
				// A manually edited invalid legacy YAML value must not make all Remote
				// pages unusable. API writes still reject invalid values below.
				options = model.DefaultXpraRemoteOptions()
			}
			targets[i].Xpra = &options
		case "rdp":
			options, err := model.NormalizeRDPRemoteOptions(targets[i].RDP)
			if err == nil {
				targets[i].RDP = &options
			}
		case "vnc":
			options, err := model.NormalizeVNCRemoteOptions(targets[i].VNC)
			if err == nil {
				targets[i].VNC = &options
			}
		}
	}
	return targets
}

func (c *Controller) RemoteTarget(id string) (model.RemoteTarget, error) {
	for _, target := range c.RemoteTargets() {
		if target.ID == id {
			return target, nil
		}
	}
	return model.RemoteTarget{}, fmt.Errorf("unknown remote target %q", id)
}

func (c *Controller) UpsertRemoteTarget(target model.RemoteTarget) (model.RemoteTarget, error) {
	if strings.TrimSpace(target.Name) == "" {
		return target, fmt.Errorf("remote target name is required")
	}
	if target.Provider == "" {
		return target, fmt.Errorf("remote target provider is required")
	}
	if target.ID == "" && !c.remote.HasProvider(target.Provider) {
		return target, fmt.Errorf("remote provider %q is not available", target.Provider)
	}
	if target.Type != model.RemoteTargetApplication && target.Type != model.RemoteTargetDesktop {
		return target, fmt.Errorf("remote target type must be application or desktop")
	}
	switch target.Provider {
	case "xpra":
		if strings.TrimSpace(target.Command.Path) == "" {
			return target, fmt.Errorf("remote target command path is required")
		}
		if target.Command.Interpreter != "" && target.Command.Interpreter != "direct" && target.Command.Interpreter != "auto" {
			return target, fmt.Errorf("remote targets require a direct executable command")
		}
		target.Command.Interpreter = "direct"
		if target.DBusMode == "" {
			if target.ForwardDBus {
				target.DBusMode = model.RemoteDBusHost
			} else {
				target.DBusMode = model.RemoteDBusIsolated
			}
		}
		if target.DBusMode != model.RemoteDBusIsolated && target.DBusMode != model.RemoteDBusHost {
			return target, fmt.Errorf("remote target D-Bus mode must be isolated or host-session")
		}
		target.ForwardDBus = false
		options, err := model.NormalizeXpraRemoteOptions(target.Xpra)
		if err != nil {
			return target, err
		}
		target.Xpra = &options
		target.RDP = nil
		target.VNC = nil
		if err := model.ValidateCommand(target.Command); err != nil {
			return target, err
		}
	case "rdp":
		if target.Type != model.RemoteTargetDesktop {
			return target, fmt.Errorf("RDP supports desktop sessions only")
		}
		options, err := model.NormalizeRDPRemoteOptions(target.RDP)
		if err != nil {
			return target, err
		}
		target.RDP = &options
		target.Xpra = nil
		target.VNC = nil
		target.Command = model.CommandSpec{}
		target.DBusMode = ""
		target.ForwardDBus = false
	case "vnc":
		if target.Type != model.RemoteTargetDesktop {
			return target, fmt.Errorf("VNC supports desktop sessions only")
		}
		options, err := model.NormalizeVNCRemoteOptions(target.VNC)
		if err != nil {
			return target, err
		}
		target.VNC = &options
		target.Xpra = nil
		target.RDP = nil
		target.Command = model.CommandSpec{}
		target.DBusMode = ""
		target.ForwardDBus = false
	default:
		return target, fmt.Errorf("unknown remote provider %q", target.Provider)
	}
	if target.ID == "" {
		target.ID = config.NewID("remote-target")
	}
	err := c.config.Update(func(cfg *model.Config) error {
		for i := range cfg.RemoteTargets {
			if cfg.RemoteTargets[i].ID == target.ID {
				cfg.RemoteTargets[i] = target
				return nil
			}
		}
		cfg.RemoteTargets = append(cfg.RemoteTargets, target)
		return nil
	})
	return target, err
}

func (c *Controller) DeleteRemoteTarget(id string) error {
	return c.config.Update(func(cfg *model.Config) error {
		out := cfg.RemoteTargets[:0]
		found := false
		for _, target := range cfg.RemoteTargets {
			if target.ID == id {
				found = true
				continue
			}
			out = append(out, target)
		}
		if !found {
			return fmt.Errorf("unknown remote target %q", id)
		}
		cfg.RemoteTargets = out
		return nil
	})
}

func (c *Controller) DataDir() string        { return c.dataDir }
func (c *Controller) ConfigPath() string     { return c.config.Path() }
func (c *Controller) Snapshot() model.Config { return c.config.Snapshot() }
func (c *Controller) TokenCreated() bool     { return c.config.TokenCreated() }

func (c *Controller) StorageLocations(ctx context.Context) []storage.Descriptor {
	return c.storage.List(ctx)
}
func (c *Controller) StorageProvider(ctx context.Context, id string) (storage.Provider, error) {
	return c.storage.Provider(ctx, id)
}
func (c *Controller) Docker() *dockercompose.Manager { return c.docker }

func (c *Controller) SoftwareDefinitions() []model.SoftwareProviderDefinition {
	definitions := c.config.Snapshot().Software.Providers
	capabilities := platform.CurrentCapabilities()
	available := make([]model.SoftwareProviderDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Type == model.SoftwareProviderScoop && !capabilities.Scoop {
			continue
		}
		available = append(available, definition)
	}
	return available
}

func (c *Controller) SoftwareProvider(id string) (software.Provider, error) {
	d, err := software.Find(c.SoftwareDefinitions(), id)
	if err != nil {
		return nil, err
	}
	return c.software.Provider(d)
}

// UpsertSoftwareProvider switches the active root when it changes. It never
// migrates, copies, or removes the prior managed Scoop installation.
func (c *Controller) UpsertSoftwareProvider(d model.SoftwareProviderDefinition) (model.SoftwareProviderDefinition, error) {
	d, err := software.ValidateDefinition(c.dataDir, d)
	if err != nil {
		return d, err
	}
	err = c.config.Update(func(cfg *model.Config) error {
		for i := range cfg.Software.Providers {
			if cfg.Software.Providers[i].ID == d.ID {
				cfg.Software.Providers[i] = d
				return nil
			}
		}
		return fmt.Errorf("unknown software provider %q", d.ID)
	})
	return d, err
}

func (c *Controller) ProcessViews() []processmgr.View {
	return c.processes.Views()
}

func (c *Controller) JobViews() []jobs.View {
	return c.jobs.Views(c.config.Snapshot().Jobs)
}

func (c *Controller) Overview() model.Overview {
	processes := c.ProcessViews()
	jobs := c.JobViews()
	overview := model.Overview{
		Host:         platform.HostStatus(),
		ProcessCount: len(processes),
		JobCount:     len(jobs),
		Issues:       []model.HealthIssue{},
	}
	if overview.Host.Error != "" {
		overview.Issues = append(overview.Issues, model.HealthIssue{
			Source:  "host",
			Name:    overview.Host.Hostname,
			Message: overview.Host.Error,
		})
	}
	for _, process := range processes {
		if process.Status.State == "running" || process.Status.State == "starting" || process.Status.State == "stopping" {
			overview.RunningProcesses++
		}
		if process.Status.ExitCode != nil && *process.Status.ExitCode != 0 {
			overview.Issues = append(overview.Issues, model.HealthIssue{
				Source:  "process",
				Name:    process.Definition.Name,
				Message: fmt.Sprintf("last exit code: %d", *process.Status.ExitCode),
			})
		}
	}
	for _, job := range jobs {
		if job.Status.Running {
			overview.RunningJobs++
		}
		if job.Status.LastSuccess != nil && !*job.Status.LastSuccess {
			message := "last run failed"
			if job.Status.LastExitCode != nil {
				message = fmt.Sprintf("last exit code: %d", *job.Status.LastExitCode)
			}
			overview.Issues = append(overview.Issues, model.HealthIssue{
				Source:  string(job.Definition.Type),
				Name:    job.Definition.Name,
				Message: message,
			})
		}
	}
	overview.TaskCount = overview.ProcessCount + overview.JobCount
	overview.RunningTasks = overview.RunningProcesses + overview.RunningJobs
	return overview
}

func (c *Controller) StartProcess(id string) error   { return c.processes.Start(id) }
func (c *Controller) StopProcess(id string) error    { return c.processes.Stop(id) }
func (c *Controller) RestartProcess(id string) error { return c.processes.Restart(id) }

func (c *Controller) UpsertProcess(p model.ProcessDefinition) (model.ProcessDefinition, error) {
	if strings.TrimSpace(p.Name) == "" {
		return p, fmt.Errorf("process name is required")
	}
	if strings.TrimSpace(p.Command.Path) == "" {
		return p, fmt.Errorf("process command path is required")
	}
	if err := model.ValidateCommand(p.Command); err != nil {
		return p, err
	}
	if p.ID == "" {
		p.ID = config.NewID("proc")
	}
	model.NormalizeProcess(&p)
	err := c.config.Update(func(cfg *model.Config) error {
		for i := range cfg.Processes {
			if cfg.Processes[i].ID == p.ID {
				cfg.Processes[i] = p
				return nil
			}
		}
		cfg.Processes = append(cfg.Processes, p)
		return nil
	})
	if err != nil {
		return p, err
	}
	c.processes.Reconcile(c.config.Snapshot().Processes)
	return p, nil
}

func (c *Controller) DeleteProcess(id string) error {
	if err := c.config.Update(func(cfg *model.Config) error {
		out := cfg.Processes[:0]
		found := false
		for _, p := range cfg.Processes {
			if p.ID == id {
				found = true
				continue
			}
			out = append(out, p)
		}
		if !found {
			return fmt.Errorf("unknown process %q", id)
		}
		cfg.Processes = out
		return nil
	}); err != nil {
		return err
	}
	c.processes.Reconcile(c.config.Snapshot().Processes)
	return nil
}

func (c *Controller) UpsertJob(j model.JobDefinition) (model.JobDefinition, error) {
	if strings.TrimSpace(j.Name) == "" {
		return j, fmt.Errorf("job name is required")
	}
	if j.Type != model.JobCommand && j.Type != model.JobBackup {
		return j, fmt.Errorf("job type must be command or backup")
	}
	model.NormalizeJob(&j)
	if _, err := scheduler.Expression(j.Schedule); err != nil {
		return j, err
	}
	switch j.Type {
	case model.JobCommand:
		if j.Command == nil || strings.TrimSpace(j.Command.Path) == "" {
			return j, fmt.Errorf("command job requires a command")
		}
		if err := model.ValidateCommand(*j.Command); err != nil {
			return j, err
		}
		j.Backup = nil
	case model.JobBackup:
		if j.Backup == nil {
			return j, fmt.Errorf("backup job requires a provider configuration")
		}
		if err := backup.Validate(*j.Backup); err != nil {
			return j, err
		}
		if _, err := backup.Build(*j.Backup); err != nil {
			return j, err
		}
		j.Command = nil
	}
	if j.ID == "" {
		j.ID = config.NewID("job")
	}
	if err := c.config.Update(func(cfg *model.Config) error {
		for i := range cfg.Jobs {
			if cfg.Jobs[i].ID == j.ID {
				cfg.Jobs[i] = j
				return nil
			}
		}
		cfg.Jobs = append(cfg.Jobs, j)
		return nil
	}); err != nil {
		return j, err
	}
	if err := c.scheduler.Reload(c.config.Snapshot().Jobs); err != nil {
		return j, err
	}
	return j, nil
}

func (c *Controller) DeleteJob(id string) error {
	if err := c.config.Update(func(cfg *model.Config) error {
		out := cfg.Jobs[:0]
		found := false
		for _, j := range cfg.Jobs {
			if j.ID == id {
				found = true
				continue
			}
			out = append(out, j)
		}
		if !found {
			return fmt.Errorf("unknown job %q", id)
		}
		cfg.Jobs = out
		return nil
	}); err != nil {
		return err
	}
	return c.scheduler.Reload(c.config.Snapshot().Jobs)
}

func (c *Controller) RunJob(id string) (string, error) {
	for _, j := range c.config.Snapshot().Jobs {
		if j.ID == id {
			return c.jobs.Run(j)
		}
	}
	return "", fmt.Errorf("unknown job %q", id)
}

func (c *Controller) RecentRuns(targetID string, limit int) ([]model.RunRecord, error) {
	return c.history.Recent(targetID, limit)
}

func (c *Controller) ProcessLog(id string, lines int) (string, error) {
	p, err := c.processes.LogPath(id)
	if err != nil {
		return "", err
	}
	return history.Tail(p, lines)
}

func (c *Controller) RunLog(runID string, lines int) (string, error) {
	if !safeID(runID) {
		return "", fmt.Errorf("invalid run id")
	}
	path := c.history.RunLogPath(runID)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return history.Tail(path, lines)
}

func safeID(id string) bool {
	if id == "" || id != filepath.Base(id) {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
