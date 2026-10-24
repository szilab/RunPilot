package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/dockercompose"
	"github.com/szilab/RunPilot/internal/history"
	"github.com/szilab/RunPilot/internal/jobs"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/plugins"
	"github.com/szilab/RunPilot/internal/remote"
	"github.com/szilab/RunPilot/internal/remote/xpra"
	"github.com/szilab/RunPilot/internal/scheduler"
	"github.com/szilab/RunPilot/internal/software"
	"github.com/szilab/RunPilot/internal/storage"
)

type Controller struct {
	dataDir              string
	config               *config.Store
	history              *history.Store
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
	browserPublications  *browserPublications
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
	return OpenWithConfigDir(dataDir, "")
}

func OpenWithConfigDir(dataDir, configDir string) (*Controller, error) {
	var err error
	dataDir, err = config.ResolveDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	cfg, err := config.OpenWithConfigDir(dataDir, configDir)
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
	c.browserPublications = newBrowserPublications(func(owner, id string) {
		c.deliverPluginEvent(owner, "http.gateway.closed", map[string]string{"id": id})
	})
	c.browserPublications.basePath = c.Snapshot().Server.BasePath
	c.pluginProcesses.history = h
	c.pluginProcesses.publish = func(owner, event string, data any) {
		if raw, err := json.Marshal(data); err == nil {
			c.publishPluginEvent(owner, event, raw)
		}
	}
	if err := migrateLegacyDockerProjects(dataDir); err != nil {
		log.Printf("legacy Docker project migration: %v", err)
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
	// Xpra remains the temporary legacy Remote runtime; its user interface has been removed.
	c.remote = remote.New(dataDir, xpra.New())
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
	if err := c.scheduler.Reload(snap.Jobs); err != nil {
		return nil, err
	}
	return c, nil
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
	c.browserPublications.stop("")
	close(c.stopEvents)
	c.remote.Close()
	c.scheduler.Stop()
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

// AttachPluginNetworkStream claims an owned stream for one browser WebSocket.
func (c *Controller) AttachPluginNetworkStream(owner, id string) (net.Conn, func(), error) {
	return c.pluginNetworkStreams.attach(owner, id)
}

// OpenPluginNetworkStream is the controller-side equivalent of the generic
// network.stream.open capability, used by trusted host integrations.
func (c *Controller) OpenPluginNetworkStream(ctx context.Context, owner string, params json.RawMessage) (json.RawMessage, error) {
	return c.pluginNetworkStreams.open(ctx, owner, params)
}

func (c *Controller) DataDir() string        { return c.dataDir }
func (c *Controller) ConfigPath() string     { return c.config.Path() }
func (c *Controller) Snapshot() model.Config { return c.config.Snapshot() }
func (c *Controller) ConfigureAdmin(username, passwordHash string) error {
	return c.config.ConfigureAdmin(username, passwordHash)
}

func (c *Controller) ChangeAdminPassword(username, currentHash, nextHash string) error {
	return c.config.ChangeAdminPassword(username, currentHash, nextHash)
}

func (c *Controller) SetBearerAuth(enabled bool) error { return c.config.SetBearerAuth(enabled) }
func (c *Controller) SetProxyAuth(trustedProxies, header string) error {
	return c.config.SetProxyAuth(trustedProxies, header)
}

func (c *Controller) SetWebSocketPayloadMode(mode string) error {
	return c.config.SetWebSocketPayloadMode(mode)
}

func (c *Controller) Overview() model.Overview {
	jobs := c.JobViews()
	overview := model.Overview{
		Host:     platform.HostStatus(),
		JobCount: len(jobs),
		Issues:   []model.HealthIssue{},
	}
	if overview.Host.Error != "" {
		overview.Issues = append(overview.Issues, model.HealthIssue{
			Source:  "host",
			Name:    overview.Host.Hostname,
			Message: overview.Host.Error,
		})
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
	overview.TaskCount = overview.JobCount
	overview.RunningTasks = overview.RunningJobs
	return overview
}
