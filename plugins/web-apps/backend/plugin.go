package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/szilab/RunPilot/internal/browserpath"
	"github.com/szilab/RunPilot/internal/pluginapi"
)

var callHost = pluginapi.CallHost

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(code, message string) *rpcError { return &rpcError{code, message} }
func hostError(err error) *rpcError {
	if value, ok := pluginapi.AsCapabilityError(err); ok {
		return fail(value.Code, value.Message)
	}
	return fail("failed", "browser host operation failed")
}

type target struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	MountPath        string            `json:"mountPath"`
	UpstreamURL      string            `json:"upstreamURL"`
	UpstreamBasePath string            `json:"upstreamBasePath"`
	BasePathHeader   string            `json:"basePathHeader,omitempty"`
	ForwardHost      bool              `json:"forwardPublicHost,omitempty"`
	ForwardScheme    bool              `json:"forwardPublicScheme,omitempty"`
	CustomHeaders    map[string]string `json:"customHeaders,omitempty"`
}
type targetStore struct {
	Version int      `json:"version"`
	NextID  uint64   `json:"nextId"`
	Targets []target `json:"targets"`
}
type session struct {
	ID            string `json:"id"`
	TargetID      string `json:"targetId"`
	PublicPrefix  string `json:"publicPrefix"`
	PublicationID string `json:"publicationId"`
	RuntimeID     string `json:"runtimeId"`
	BaseURL       string `json:"baseURL"`
}
type plugin struct {
	publications map[string]string
	sessions     map[string]session
	runtimeID    string
}

func newPlugin() *plugin {
	return &plugin{publications: map[string]string{}, sessions: map[string]session{}}
}
func decode(raw json.RawMessage, out any) *rpcError {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return fail("invalid_argument", "invalid request")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fail("invalid_argument", "extra JSON")
	}
	return nil
}
func validID(id string) bool {
	if !strings.HasPrefix(id, "app-") {
		return false
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(id, "app-"), 10, 64)
	return err == nil && n > 0 && id == fmt.Sprintf("app-%d", n)
}
func normalizeTarget(t target) (target, *rpcError) {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || len(t.Name) > 100 || !utf8.ValidString(t.Name) || strings.IndexFunc(t.Name, unicode.IsControl) >= 0 {
		return target{}, fail("invalid_argument", "name must contain 1 to 100 printable bytes")
	}
	if t.ID != "" && !validID(t.ID) {
		return target{}, fail("invalid_argument", "invalid generated target ID")
	}
	t.MountPath = strings.TrimSuffix(t.MountPath, "/")
	if browserpath.Mount(t.MountPath) != nil {
		return target{}, fail("invalid_argument", "mount must be a normalized nonreserved path")
	}
	if t.UpstreamBasePath == "" {
		t.UpstreamBasePath = "/"
	}
	if t.UpstreamBasePath != "/" {
		t.UpstreamBasePath = strings.TrimSuffix(t.UpstreamBasePath, "/")
	}
	if browserpath.Validate(t.UpstreamBasePath, true) != nil {
		return target{}, fail("invalid_argument", "upstream base path must be normalized")
	}
	if t.BasePathHeader != "" && t.BasePathHeader != "X-Forwarded-Prefix" && t.BasePathHeader != "X-Script-Name" {
		return target{}, fail("invalid_argument", "unsupported base path header")
	}
	if len(t.CustomHeaders) > 16 {
		return target{}, fail("invalid_argument", "at most 16 custom headers are allowed")
	}
	cleanHeaders := make(map[string]string, len(t.CustomHeaders))
	seenHeaders := make(map[string]bool, len(t.CustomHeaders))
	headerBytes := 0
	for name, value := range t.CustomHeaders {
		lower := strings.ToLower(name)
		if !validHeaderName(name) || seenHeaders[lower] || forbiddenCustomHeader(lower) || len(name) > 128 || len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
			return target{}, fail("invalid_argument", "custom header name or value is invalid or forbidden")
		}
		seenHeaders[lower] = true
		headerBytes += len(name) + len(value)
		if headerBytes > 8192 {
			return target{}, fail("invalid_argument", "custom headers exceed 8 KiB")
		}
		cleanHeaders[http.CanonicalHeaderKey(name)] = value
	}
	t.CustomHeaders = cleanHeaders
	u, err := url.Parse(t.UpstreamURL)
	if err != nil || len(t.UpstreamURL) > 2048 || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" || u.ForceQuery || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(t.UpstreamURL, "\r\n\x00") {
		return target{}, fail("invalid_argument", "upstream URL must be an HTTP(S) origin without userinfo, path, query or fragment")
	}
	if u.Port() != "" {
		port, e := strconv.Atoi(u.Port())
		if e != nil || port < 1 || port > 65535 {
			return target{}, fail("invalid_argument", "invalid upstream port")
		}
	}
	u.Path = ""
	t.UpstreamURL = u.String()
	return t, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

func forbiddenCustomHeader(name string) bool {
	if strings.HasPrefix(name, "proxy-") || strings.HasPrefix(name, "sec-websocket-") {
		return true
	}
	switch name {
	case "host", "content-length", "transfer-encoding", "connection", "upgrade", "keep-alive", "te", "trailer", "cookie", "set-cookie", "authorization", "proxy-authorization", "forwarded", "origin", "referer", "service-worker", "accept-encoding", "range", "if-range", "x-real-ip", "x-forwarded-host", "x-forwarded-proto", "x-forwarded-for", "x-forwarded-prefix", "x-script-name":
		return true
	}
	return false
}
func (p *plugin) readTargets() (targetStore, *rpcError) {
	stored := targetStore{Version: 1, NextID: 1, Targets: []target{}}
	var raw json.RawMessage
	if err := callHost("storage.get", map[string]any{"key": "targets"}, &raw); err != nil {
		return stored, hostError(err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return stored, nil
	}
	if decode(raw, &stored) != nil || stored.Version != 1 || stored.NextID == 0 || stored.Targets == nil || len(stored.Targets) > 256 {
		return stored, fail("invalid_configuration", "invalid stored Web Apps targets")
	}
	seen := map[string]bool{}
	for i, t := range stored.Targets {
		normal, f := normalizeTarget(t)
		if f != nil || !validID(t.ID) || seen[t.ID] {
			return stored, fail("invalid_configuration", "invalid stored target")
		}
		for j := 0; j < i; j++ {
			if browserpath.Overlap(stored.Targets[j].MountPath, t.MountPath) {
				return stored, fail("invalid_configuration", "overlapping stored mounts")
			}
		}
		seen[t.ID] = true
		stored.Targets[i] = normal
	}
	return stored, nil
}
func (p *plugin) publish(t target) *rpcError {
	if f := p.ensureRuntime(); f != nil {
		return f
	}
	if p.publications[t.ID] != "" {
		return nil
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := callHost("browser.publication.register", map[string]any{"mountPath": t.MountPath, "bootstrap": "web/bootstrap.html", "runtimeId": p.runtimeID}, &out); err != nil {
		return hostError(err)
	}
	if out.ID == "" {
		return fail("failed", "publication returned no ID")
	}
	p.publications[t.ID] = out.ID
	return nil
}
func (p *plugin) ensureRuntime() *rpcError {
	if p.runtimeID != "" {
		return nil
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := callHost("browser.runtime.register", map[string]string{"bootstrap": "web/bootstrap.js", "worker": "web/sw.js"}, &out); err != nil {
		return hostError(err)
	}
	if out.ID == "" {
		return fail("failed", "browser runtime returned no ID")
	}
	p.runtimeID = out.ID
	return nil
}
func (p *plugin) initialize() *rpcError {
	stored, f := p.readTargets()
	if f != nil {
		return f
	}
	if f = p.ensureRuntime(); f != nil {
		return f
	}
	for _, t := range stored.Targets {
		if f = p.publish(t); f != nil {
			p.shutdown()
			return f
		}
	}
	return nil
}
func (p *plugin) active(targetID string) bool {
	for _, s := range p.sessions {
		if s.TargetID == targetID {
			return true
		}
	}
	return false
}
func (p *plugin) removePublication(id string) {
	if publication := p.publications[id]; publication != "" {
		_ = callHost("browser.publication.remove", map[string]any{"id": publication}, nil)
		delete(p.publications, id)
	}
}
func (p *plugin) writeTargets(stored targetStore) *rpcError {
	if err := callHost("storage.set", map[string]any{"key": "targets", "value": stored}, nil); err != nil {
		return hostError(err)
	}
	return nil
}
func (p *plugin) save(raw json.RawMessage) (any, *rpcError) {
	var q struct {
		Target target `json:"target"`
	}
	if f := decode(raw, &q); f != nil {
		return nil, f
	}
	t, f := normalizeTarget(q.Target)
	if f != nil {
		return nil, f
	}
	stored, f := p.readTargets()
	if f != nil {
		return nil, f
	}
	index := -1
	for i, old := range stored.Targets {
		if old.ID == t.ID {
			index = i
		}
		if old.ID != t.ID && browserpath.Overlap(old.MountPath, t.MountPath) {
			return nil, fail("already_exists", "mount overlaps another target")
		}
	}
	if t.ID != "" && index < 0 {
		return nil, fail("not_found", "unknown target")
	}
	if p.active(t.ID) {
		return nil, fail("failed_precondition", "close active gateway sessions before editing the target")
	}
	var old target
	if index < 0 {
		if len(stored.Targets) >= 256 {
			return nil, fail("resource_limit", "target limit reached")
		}
		for {
			if stored.NextID == ^uint64(0) {
				return nil, fail("resource_limit", "target ID sequence exhausted")
			}
			t.ID = fmt.Sprintf("app-%d", stored.NextID)
			stored.NextID++
			exists := false
			for _, v := range stored.Targets {
				if v.ID == t.ID {
					exists = true
				}
			}
			if !exists {
				break
			}
		}
	} else {
		old = stored.Targets[index]
	}
	changedMount := index < 0 || old.MountPath != t.MountPath
	if changedMount {
		// Allocate first when paths do not overlap. For an overlapping move,
		// remove the inactive old mount and restore it if the replacement fails.
		prior := p.publications[t.ID]
		removedPrior := prior != "" && browserpath.Overlap(old.MountPath, t.MountPath)
		if removedPrior {
			p.removePublication(t.ID)
		}
		delete(p.publications, t.ID)
		if f = p.publish(t); f != nil {
			if removedPrior {
				_ = p.publish(old)
			} else if prior != "" {
				p.publications[t.ID] = prior
			}
			return nil, f
		}
		next := p.publications[t.ID]
		if index < 0 {
			stored.Targets = append(stored.Targets, t)
		} else {
			stored.Targets[index] = t
		}
		if f = p.writeTargets(stored); f != nil {
			_ = callHost("browser.publication.remove", map[string]any{"id": next}, nil)
			delete(p.publications, t.ID)
			if removedPrior {
				_ = p.publish(old)
			} else if prior != "" {
				p.publications[t.ID] = prior
			} else {
				delete(p.publications, t.ID)
			}
			return nil, f
		}
		if prior != "" && !removedPrior {
			_ = callHost("browser.publication.remove", map[string]any{"id": prior}, nil)
		}
	} else {
		stored.Targets[index] = t
		if f = p.writeTargets(stored); f != nil {
			return nil, f
		}
	}
	return map[string]any{"target": t}, nil
}
func (p *plugin) handle(method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "apps.targets.list":
		stored, f := p.readTargets()
		if f != nil {
			return nil, f
		}
		return map[string]any{"targets": stored.Targets}, nil
	case "apps.targets.save":
		return p.save(raw)
	case "apps.targets.delete":
		var q struct {
			ID string `json:"id"`
		}
		if decode(raw, &q) != nil || !validID(q.ID) {
			return nil, fail("invalid_argument", "target ID required")
		}
		if p.active(q.ID) {
			return nil, fail("failed_precondition", "close active gateway sessions before deleting the target")
		}
		stored, f := p.readTargets()
		if f != nil {
			return nil, f
		}
		for i, t := range stored.Targets {
			if t.ID == q.ID {
				stored.Targets = append(stored.Targets[:i], stored.Targets[i+1:]...)
				if f = p.writeTargets(stored); f != nil {
					return nil, f
				}
				p.removePublication(q.ID)
				return map[string]any{"ok": true}, nil
			}
		}
		return nil, fail("not_found", "unknown target")
	case "apps.session.open":
		var q struct {
			TargetID     string `json:"targetId"`
			PublicHost   string `json:"publicHost"`
			PublicScheme string `json:"publicScheme"`
		}
		if decode(raw, &q) != nil || !validID(q.TargetID) {
			return nil, fail("invalid_argument", "target ID required")
		}
		if (q.PublicHost != "" && (len(q.PublicHost) > 255 || strings.ContainsAny(q.PublicHost, " /\\\r\n\x00"))) || (q.PublicScheme != "" && q.PublicScheme != "https" && q.PublicScheme != "http") || (q.PublicHost == "" && q.PublicScheme != "") || (q.PublicScheme == "" && q.PublicHost != "") {
			return nil, fail("invalid_argument", "invalid public origin")
		}
		if len(p.sessions) >= 8 {
			return nil, fail("resource_limit", "gateway session limit reached")
		}
		stored, f := p.readTargets()
		if f != nil {
			return nil, f
		}
		for _, t := range stored.Targets {
			if t.ID == q.TargetID {
				if f = p.publish(t); f != nil {
					return nil, f
				}
				var s session
				if err := callHost("http.gateway.open", map[string]any{"publicationId": p.publications[t.ID], "upstreamURL": t.UpstreamURL, "upstreamBasePath": t.UpstreamBasePath, "basePathHeader": t.BasePathHeader, "forwardPublicHost": t.ForwardHost, "forwardPublicScheme": t.ForwardScheme, "customHeaders": t.CustomHeaders, "publicHost": q.PublicHost, "publicScheme": q.PublicScheme}, &s); err != nil {
					return nil, hostError(err)
				}
				if s.ID == "" {
					return nil, fail("failed", "gateway returned no session ID")
				}
				var ticket struct {
					Ticket string `json:"ticket"`
				}
				if err := callHost("browser.stream.ticket", map[string]any{"streamId": s.ID}, &ticket); err != nil {
					_ = callHost("http.gateway.close", map[string]any{"id": s.ID}, nil)
					return nil, hostError(err)
				}
				s.TargetID = t.ID
				p.sessions[s.ID] = s
				return map[string]any{"session": s, "ticket": ticket.Ticket}, nil
			}
		}
		return nil, fail("not_found", "unknown target")
	case "apps.sessions.list":
		items := []session{}
		for _, s := range p.sessions {
			items = append(items, s)
		}
		return map[string]any{"sessions": items}, nil
	case "apps.session.close":
		var q struct {
			ID string `json:"id"`
		}
		if decode(raw, &q) != nil || q.ID == "" {
			return nil, fail("invalid_argument", "session ID required")
		}
		if _, ok := p.sessions[q.ID]; ok {
			if err := callHost("http.gateway.close", map[string]any{"id": q.ID}, nil); err != nil {
				return nil, hostError(err)
			}
			delete(p.sessions, q.ID)
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, fail("unknown_method", "unknown Web Apps method")
	}
}
func (p *plugin) event(name string, raw json.RawMessage) *rpcError {
	if name == "http.gateway.closed" {
		var e struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return fail("invalid_argument", "invalid gateway event")
		}
		delete(p.sessions, e.ID)
	}
	return nil
}
func (p *plugin) shutdown() {
	for id := range p.sessions {
		_ = callHost("http.gateway.close", map[string]any{"id": id}, nil)
		delete(p.sessions, id)
	}
	for id := range p.publications {
		p.removePublication(id)
	}
	if p.runtimeID != "" {
		_ = callHost("browser.runtime.remove", map[string]string{"id": p.runtimeID}, nil)
		p.runtimeID = ""
	}
}
