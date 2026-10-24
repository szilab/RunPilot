package plugins

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// capabilityHandler serves one published host capability. name is the
// requested capability, so one handler can serve a whole family.
type capabilityHandler func(r *Runtime, ctx context.Context, name string, params json.RawMessage) capabilityResponse

type identityHost interface {
	SystemIdentity(context.Context) (json.RawMessage, error)
}

// capabilityHandlers is the complete published capability table. Adding a
// capability means adding one entry here and its optional host interface.
var capabilityHandlers = buildCapabilityHandlers()

// PublishedCapabilities lists every capability name this RunPilot version serves.
func PublishedCapabilities() []string {
	names := make([]string, 0, len(capabilityHandlers))
	for name := range capabilityHandlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func buildCapabilityHandlers() map[string]capabilityHandler {
	handlers := map[string]capabilityHandler{
		"log.write":       logWrite,
		"system.status":   systemStatus,
		"system.identity": systemIdentity,
		"storage.get":     storageGet,
		"storage.set":     storageSet,
		"scheduler.register": hostCapability("scheduler is unavailable", func(h SchedulerHost, ctx context.Context, owner, _ string, params json.RawMessage) (json.RawMessage, error) {
			return h.ScheduleRegister(ctx, owner, params)
		}),
		"scheduler.validate": hostCapability("scheduler is unavailable", func(h SchedulerValidator, ctx context.Context, _, _ string, params json.RawMessage) (json.RawMessage, error) {
			return nil, h.ScheduleValidate(ctx, params)
		}),
		"scheduler.remove": schedulerRemove,
		"scheduler.list":   schedulerList,
		"process.start": hostCapability("process manager is unavailable", func(h ProcessHost, ctx context.Context, owner, _ string, params json.RawMessage) (json.RawMessage, error) {
			return h.ProcessStart(ctx, owner, params)
		}),
		"process.run": hostCapability("bounded process execution is unavailable", func(h ProcessRunHost, ctx context.Context, owner, _ string, params json.RawMessage) (json.RawMessage, error) {
			return h.ProcessRun(ctx, owner, params)
		}),
		"process.status":    processByID,
		"process.terminate": processByID,
		"events.publish":    eventsPublish,
	}
	family := func(handler capabilityHandler, names ...string) {
		for _, name := range names {
			handlers[name] = handler
		}
	}
	family(hostCapability("plugin workspace is unavailable", func(h WorkspaceHost, ctx context.Context, owner, name string, params json.RawMessage) (json.RawMessage, error) {
		return h.Workspace(ctx, owner, strings.TrimPrefix(name, "workspace."), params)
	}), "workspace.list", "workspace.stat", "workspace.read", "workspace.write", "workspace.mkdir", "workspace.remove")
	family(hostCapability("execution history is unavailable", func(h HistoryHost, ctx context.Context, owner, name string, params json.RawMessage) (json.RawMessage, error) {
		return h.History(ctx, owner, strings.TrimPrefix(name, "history."), params)
	}), "history.begin", "history.append", "history.finish", "history.list", "history.get", "history.output")
	family(hostCapability("browser capabilities unavailable", func(h BrowserHost, ctx context.Context, owner, name string, params json.RawMessage) (json.RawMessage, error) {
		return h.BrowserCapability(ctx, owner, name, params)
	}), "browser.runtime.register", "browser.runtime.remove", "browser.publication.register", "browser.publication.remove", "browser.stream.ticket", "http.gateway.open", "http.gateway.close")

	networkTools := map[string]func(NetworkToolsHost, context.Context, string, json.RawMessage) (json.RawMessage, error){
		"network.probe":           NetworkToolsHost.NetworkProbe,
		"network.neighbor.lookup": NetworkToolsHost.NeighborLookup,
		"network.wake":            NetworkToolsHost.NetworkWake,
	}
	for name, call := range networkTools {
		call := call
		handlers[name] = hostCapability("network tools are unavailable", func(h NetworkToolsHost, ctx context.Context, owner, _ string, params json.RawMessage) (json.RawMessage, error) {
			return call(h, ctx, owner, params)
		})
	}
	sessions := map[string]func(ProcessSessionHost, context.Context, string, json.RawMessage) (json.RawMessage, error){
		"process.session.create":    ProcessSessionHost.ProcessSessionCreate,
		"process.session.write":     ProcessSessionHost.ProcessSessionWrite,
		"process.session.resize":    ProcessSessionHost.ProcessSessionResize,
		"process.session.status":    ProcessSessionHost.ProcessSessionStatus,
		"process.session.terminate": ProcessSessionHost.ProcessSessionTerminate,
	}
	for name, call := range sessions {
		call := call
		handlers[name] = hostCapability("interactive process sessions are unavailable", func(h ProcessSessionHost, ctx context.Context, owner, _ string, params json.RawMessage) (json.RawMessage, error) {
			return call(h, ctx, owner, params)
		})
	}
	streams := map[string]func(NetworkStreamHost, context.Context, string, json.RawMessage) (json.RawMessage, error){
		"network.stream.open":  NetworkStreamHost.NetworkStreamOpen,
		"network.stream.read":  NetworkStreamHost.NetworkStreamRead,
		"network.stream.write": NetworkStreamHost.NetworkStreamWrite,
		"network.stream.close": NetworkStreamHost.NetworkStreamClose,
	}
	for name, call := range streams {
		call := call
		handlers[name] = hostCapability("network streams are unavailable", func(h NetworkStreamHost, ctx context.Context, owner, _ string, params json.RawMessage) (json.RawMessage, error) {
			return call(h, ctx, owner, params)
		})
	}
	return handlers
}

// hostCapability adapts an optional host interface: it reports "unavailable"
// when the embedder does not implement H and maps host errors uniformly.
func hostCapability[H any](unavailable string, call func(H, context.Context, string, string, json.RawMessage) (json.RawMessage, error)) capabilityHandler {
	return func(r *Runtime, ctx context.Context, name string, params json.RawMessage) capabilityResponse {
		host, ok := any(r.host).(H)
		if !ok {
			return capabilityFailure("unavailable", unavailable)
		}
		value, err := call(host, ctx, r.manifest.ID, name, params)
		if err != nil {
			return detailedFailure(name, err)
		}
		return capabilitySuccess(value)
	}
}

func emptyParams(params json.RawMessage) bool {
	return len(params) == 0 || string(params) == "{}" || string(params) == "null"
}

func logWrite(r *Runtime, ctx context.Context, _ string, params json.RawMessage) capabilityResponse {
	var args struct {
		Message string `json:"message"`
	}
	if err := decodeParams(params, &args); err != nil || strings.TrimSpace(args.Message) == "" {
		return capabilityFailure("invalid_argument", "log.write requires a non-empty message")
	}
	if err := r.host.Log(ctx, args.Message); err != nil {
		return capabilityFailure("failed", "log.write failed")
	}
	return capabilitySuccess(nil)
}

func systemStatus(r *Runtime, ctx context.Context, _ string, params json.RawMessage) capabilityResponse {
	if !emptyParams(params) {
		return capabilityFailure("invalid_argument", "system.status does not accept parameters")
	}
	value, err := r.host.SystemStatus(ctx)
	if err != nil {
		return capabilityFailure("failed", "system.status failed")
	}
	if !json.Valid(value) {
		return capabilityFailure("failed", "system.status returned invalid JSON")
	}
	return capabilitySuccess(value)
}

func systemIdentity(r *Runtime, ctx context.Context, name string, params json.RawMessage) capabilityResponse {
	if !emptyParams(params) {
		return capabilityFailure("invalid_argument", "system.identity does not accept parameters")
	}
	return hostCapability("system identity is unavailable", func(h identityHost, ctx context.Context, _, _ string, _ json.RawMessage) (json.RawMessage, error) {
		return h.SystemIdentity(ctx)
	})(r, ctx, name, params)
}

func storageGet(r *Runtime, ctx context.Context, _ string, params json.RawMessage) capabilityResponse {
	var args struct {
		Key string `json:"key"`
	}
	if err := decodeParams(params, &args); err != nil || !validStorageKey(args.Key) {
		return capabilityFailure("invalid_argument", "storage.get requires a valid key")
	}
	storage, ok := r.host.(StorageHost)
	if !ok {
		return capabilityFailure("unavailable", "plugin storage is unavailable")
	}
	value, err := storage.StorageGet(ctx, r.manifest.ID, args.Key)
	if err != nil {
		return capabilityFailure("failed", "storage.get failed")
	}
	return capabilitySuccess(value)
}

func storageSet(r *Runtime, ctx context.Context, _ string, params json.RawMessage) capabilityResponse {
	var args struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if err := decodeParams(params, &args); err != nil || !validStorageKey(args.Key) || !json.Valid(args.Value) {
		return capabilityFailure("invalid_argument", "storage.set requires a valid key and JSON value")
	}
	storage, ok := r.host.(StorageHost)
	if !ok {
		return capabilityFailure("unavailable", "plugin storage is unavailable")
	}
	if err := storage.StorageSet(ctx, r.manifest.ID, args.Key, args.Value); err != nil {
		return capabilityFailure("failed", "storage.set failed")
	}
	return capabilitySuccess(nil)
}

func schedulerRemove(r *Runtime, ctx context.Context, _ string, params json.RawMessage) capabilityResponse {
	var args struct {
		ID string `json:"id"`
	}
	if err := decodeParams(params, &args); err != nil || strings.TrimSpace(args.ID) == "" {
		return capabilityFailure("invalid_argument", "scheduler.remove requires id")
	}
	scheduler, ok := r.host.(SchedulerHost)
	if !ok {
		return capabilityFailure("unavailable", "scheduler is unavailable")
	}
	if err := scheduler.ScheduleRemove(ctx, r.manifest.ID, args.ID); err != nil {
		return detailedFailure("scheduler.remove", err)
	}
	return capabilitySuccess(nil)
}

func schedulerList(r *Runtime, ctx context.Context, _ string, _ json.RawMessage) capabilityResponse {
	scheduler, ok := r.host.(SchedulerHost)
	if !ok {
		return capabilityFailure("unavailable", "scheduler is unavailable")
	}
	value, err := scheduler.ScheduleList(ctx, r.manifest.ID)
	if err != nil {
		return capabilityFailure("failed", "scheduler.list failed")
	}
	return capabilitySuccess(value)
}

func processByID(r *Runtime, ctx context.Context, name string, params json.RawMessage) capabilityResponse {
	var args struct {
		ID string `json:"id"`
	}
	if err := decodeParams(params, &args); err != nil || strings.TrimSpace(args.ID) == "" {
		return capabilityFailure("invalid_argument", name+" requires id")
	}
	process, ok := r.host.(ProcessHost)
	if !ok {
		return capabilityFailure("unavailable", "process manager is unavailable")
	}
	var value json.RawMessage
	var err error
	if name == "process.status" {
		value, err = process.ProcessStatus(ctx, r.manifest.ID, args.ID)
	} else {
		value, err = process.ProcessTerminate(ctx, r.manifest.ID, args.ID)
	}
	if err != nil {
		return detailedFailure(name, err)
	}
	return capabilitySuccess(value)
}

func eventsPublish(r *Runtime, ctx context.Context, _ string, params json.RawMessage) capabilityResponse {
	var args struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
	}
	if err := decodeParams(params, &args); err != nil || strings.TrimSpace(args.Event) == "" || !json.Valid(args.Data) {
		return capabilityFailure("invalid_argument", "events.publish requires event and JSON data")
	}
	events, ok := r.host.(EventHost)
	if !ok {
		return capabilityFailure("unavailable", "event publication is unavailable")
	}
	if err := events.PublishEvent(ctx, r.manifest.ID, args.Event, args.Data); err != nil {
		return detailedFailure("events.publish", err)
	}
	return capabilitySuccess(nil)
}
