package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/browserpath"
	"github.com/szilab/RunPilot/internal/httpgateway"
	"github.com/szilab/RunPilot/internal/plugins"
)

type BrowserPublication struct {
	ID        string `json:"id"`
	Owner     string `json:"owner"`
	MountPath string `json:"mountPath"`
	Bootstrap string `json:"bootstrap"`
	Worker    string `json:"worker"`
	RuntimeID string `json:"runtimeId,omitempty"`
}

// BrowserRuntime owns the single gateway worker scope for this listener. The
// scope and serving routes are host-defined, never supplied by a package.
type BrowserRuntime struct {
	ID        string `json:"id"`
	Owner     string `json:"owner"`
	Bootstrap string `json:"bootstrap"`
	Worker    string `json:"worker"`
}
type BrowserStreamGrant struct{ Owner, StreamID string }
type browserTicket struct {
	BrowserStreamGrant
	expires time.Time
}
type browserGateway struct {
	owner, publication string
	conn               net.Conn
	gateway            *httpgateway.Gateway
	attached           bool
	timer              *time.Timer
	done               chan struct{}
}
type browserPublications struct {
	mu       sync.Mutex
	mounts   map[string]BrowserPublication
	gateways map[string]*browserGateway
	tickets  map[string]browserTicket
	closed   bool
	basePath string
	runtime  *BrowserRuntime
	emit     func(string, string)
}

func newBrowserPublications(emit func(string, string)) *browserPublications {
	return &browserPublications{mounts: map[string]BrowserPublication{}, gateways: map[string]*browserGateway{}, tickets: map[string]browserTicket{}, emit: emit}
}
func browserID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func (m *browserPublications) call(owner, base, method string, raw json.RawMessage) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, networkInvalid("browser host is shutting down")
	}
	if m.basePath != "" {
		base = m.basePath
	}
	switch method {
	case "browser.runtime.register":
		var in struct {
			Bootstrap string `json:"bootstrap"`
			Worker    string `json:"worker"`
		}
		if decodeNetworkParams(raw, &in) != nil || !browserAsset(in.Bootstrap) || !browserAsset(in.Worker) || !strings.HasSuffix(in.Bootstrap, ".js") || !strings.HasSuffix(in.Worker, ".js") {
			return nil, networkInvalid("invalid browser runtime assets")
		}
		if m.runtime != nil {
			if m.runtime.Owner != owner || m.runtime.Bootstrap != in.Bootstrap || m.runtime.Worker != in.Worker {
				return nil, &plugins.HostFailure{Code: "already_exists", Message: "listener browser runtime is already owned"}
			}
			return json.Marshal(m.runtime)
		}
		id := browserID()
		if id == "" {
			return nil, networkInvalid("cannot allocate browser runtime")
		}
		m.runtime = &BrowserRuntime{id, owner, in.Bootstrap, in.Worker}
		return json.Marshal(m.runtime)
	case "browser.runtime.remove":
		var in struct {
			ID string `json:"id"`
		}
		if decodeNetworkParams(raw, &in) != nil || in.ID == "" {
			return nil, networkInvalid("runtime ID required")
		}
		if m.runtime == nil || m.runtime.ID != in.ID || m.runtime.Owner != owner {
			return nil, &plugins.HostFailure{Code: "not_found", Message: "unknown browser runtime"}
		}
		for id, g := range m.gateways {
			if m.mounts[g.publication].RuntimeID == in.ID {
				m.closeLocked(id, g)
			}
		}
		for id, p := range m.mounts {
			if p.RuntimeID == in.ID {
				delete(m.mounts, id)
			}
		}
		m.runtime = nil
		return json.RawMessage(`{}`), nil
	case "browser.publication.register":
		var in struct {
			MountPath string `json:"mountPath"`
			Bootstrap string `json:"bootstrap"`
			Worker    string `json:"worker"`
			RuntimeID string `json:"runtimeId"`
		}
		if decodeNetworkParams(raw, &in) != nil || browserpath.Mount(in.MountPath) != nil || !browserAsset(in.Bootstrap) {
			return nil, networkInvalid("invalid browser publication")
		}
		if in.RuntimeID != "" {
			if in.Worker != "" || m.runtime == nil || m.runtime.ID != in.RuntimeID || m.runtime.Owner != owner {
				return nil, networkInvalid("publication requires the owner's active browser runtime")
			}
		} else if !browserAsset(in.Worker) {
			return nil, networkInvalid("publication worker required")
		}
		for _, p := range m.mounts {
			if browserpath.Overlap(p.MountPath, in.MountPath) {
				return nil, &plugins.HostFailure{Code: "already_exists", Message: "browser mount overlaps an existing publication"}
			}
		}
		if len(m.mounts) >= 256 {
			return nil, &plugins.HostFailure{Code: "resource_limit", Message: "browser mount limit reached"}
		}
		id := browserID()
		if id == "" {
			return nil, networkInvalid("cannot allocate publication")
		}
		p := BrowserPublication{ID: id, Owner: owner, MountPath: in.MountPath, Bootstrap: in.Bootstrap, Worker: in.Worker, RuntimeID: in.RuntimeID}
		m.mounts[id] = p
		return json.Marshal(p)
	case "browser.publication.remove":
		var in struct {
			ID string `json:"id"`
		}
		if decodeNetworkParams(raw, &in) != nil || in.ID == "" {
			return nil, networkInvalid("publication ID required")
		}
		if p, ok := m.mounts[in.ID]; ok {
			if p.Owner != owner {
				return nil, &plugins.HostFailure{Code: "not_found", Message: "unknown publication"}
			}
			for id, g := range m.gateways {
				if g.publication == in.ID {
					m.closeLocked(id, g)
				}
			}
			delete(m.mounts, in.ID)
		}
		return json.RawMessage(`{}`), nil
	case "http.gateway.open":
		var in struct {
			PublicationID       string            `json:"publicationId"`
			UpstreamURL         string            `json:"upstreamURL"`
			UpstreamBasePath    string            `json:"upstreamBasePath"`
			BasePathHeader      string            `json:"basePathHeader"`
			BasePathHeaderValue string            `json:"basePathHeaderValue"`
			InsecureSkipVerify  bool              `json:"insecureSkipVerify"`
			ForwardPublicHost   bool              `json:"forwardPublicHost"`
			ForwardPublicScheme bool              `json:"forwardPublicScheme"`
			PublicHost          string            `json:"publicHost"`
			PublicScheme        string            `json:"publicScheme"`
			CustomHeaders       map[string]string `json:"customHeaders"`
		}
		if decodeNetworkParams(raw, &in) != nil {
			return nil, networkInvalid("invalid gateway request")
		}
		p, ok := m.mounts[in.PublicationID]
		if !ok || p.Owner != owner {
			return nil, &plugins.HostFailure{Code: "not_found", Message: "unknown publication"}
		}
		owned := 0
		for _, g := range m.gateways {
			if g.owner == owner {
				owned++
			}
		}
		if owned >= 8 || len(m.gateways) >= 64 {
			return nil, &plugins.HostFailure{Code: "resource_limit", Message: "HTTP gateway limit reached"}
		}
		browser, host := net.Pipe()
		customHeaders := make(http.Header, len(in.CustomHeaders))
		for key, value := range in.CustomHeaders {
			customHeaders.Set(key, value)
		}
		gateway, err := httpgateway.New(httpgateway.Config{UpstreamURL: in.UpstreamURL, UpstreamBasePath: in.UpstreamBasePath, PublicPrefix: browserpath.Join(base, p.MountPath), BasePathHeader: in.BasePathHeader, BasePathHeaderValue: in.BasePathHeaderValue, InsecureSkipVerify: in.InsecureSkipVerify, ForwardPublicHost: in.ForwardPublicHost, ForwardPublicScheme: in.ForwardPublicScheme, PublicHost: in.PublicHost, PublicScheme: in.PublicScheme, CustomHeaders: customHeaders}, host)
		if err != nil {
			_ = browser.Close()
			_ = host.Close()
			return nil, networkInvalid(err.Error())
		}
		id := browserID()
		if id == "" {
			gateway.Close()
			_ = browser.Close()
			return nil, networkInvalid("cannot allocate gateway")
		}
		g := &browserGateway{owner: owner, publication: p.ID, conn: browser, gateway: gateway, done: make(chan struct{})}
		m.gateways[id] = g
		g.timer = time.AfterFunc(time.Minute, func() {
			m.mu.Lock()
			if m.gateways[id] == g && !g.attached {
				m.closeLocked(id, g)
			}
			m.mu.Unlock()
		})
		go func() {
			gateway.Serve()
			close(g.done)
			m.mu.Lock()
			if m.gateways[id] == g {
				m.closeLocked(id, g)
			}
			m.mu.Unlock()
		}()
		return json.Marshal(map[string]string{"id": id, "publicPrefix": browserpath.Escape(browserpath.Join(base, p.MountPath)), "publicationId": p.ID, "runtimeId": p.RuntimeID, "baseURL": browserpath.Escape(strings.TrimSuffix(base, "/")) + "/"})
	case "http.gateway.close":
		var in struct {
			ID string `json:"id"`
		}
		if decodeNetworkParams(raw, &in) != nil || in.ID == "" {
			return nil, networkInvalid("gateway ID required")
		}
		if g := m.gateways[in.ID]; g != nil {
			if g.owner != owner {
				return nil, &plugins.HostFailure{Code: "not_found", Message: "unknown gateway"}
			}
			m.closeLocked(in.ID, g)
		}
		return json.RawMessage(`{}`), nil
	case "browser.stream.ticket":
		var in struct {
			StreamID string `json:"streamId"`
		}
		if decodeNetworkParams(raw, &in) != nil {
			return nil, networkInvalid("stream ID required")
		}
		g := m.gateways[in.StreamID]
		if g == nil || g.owner != owner || g.attached {
			return nil, &plugins.HostFailure{Code: "not_found", Message: "unknown unattached gateway"}
		}
		for t, grant := range m.tickets {
			if time.Now().After(grant.expires) {
				delete(m.tickets, t)
			}
		}
		if len(m.tickets) >= 256 {
			return nil, &plugins.HostFailure{Code: "resource_limit", Message: "browser ticket limit reached"}
		}
		ticket := browserID()
		if ticket == "" {
			return nil, networkInvalid("cannot allocate ticket")
		}
		m.tickets[ticket] = browserTicket{BrowserStreamGrant{owner, in.StreamID}, time.Now().Add(time.Minute)}
		return json.Marshal(map[string]string{"ticket": ticket})
	}
	return nil, networkInvalid("unknown browser capability")
}
func browserAsset(value string) bool {
	return path.Clean(value) == value && strings.TrimSpace(value) == value && strings.HasPrefix(value, "web/") && !strings.ContainsAny(value, "\\?#:\x00\r\n") && !strings.Contains(value, "..") && len(value) < 256
}
func (m *browserPublications) closeLocked(id string, g *browserGateway) {
	delete(m.gateways, id)
	g.timer.Stop()
	_ = g.conn.Close()
	g.gateway.Close()
	<-g.done // Serve closes done before acquiring the manager lock for retirement.
	for t, grant := range m.tickets {
		if grant.StreamID == id {
			delete(m.tickets, t)
		}
	}
	if m.emit != nil {
		go m.emit(g.owner, id)
	}
}
func (m *browserPublications) consume(ticket string) (BrowserStreamGrant, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[ticket]
	delete(m.tickets, ticket)
	g := m.gateways[t.StreamID]
	return t.BrowserStreamGrant, ok && time.Now().Before(t.expires) && g != nil && g.owner == t.Owner && !g.attached
}
func (m *browserPublications) attach(owner, id string) (net.Conn, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.gateways[id]
	if g == nil || g.owner != owner || g.attached {
		return nil, nil, &plugins.HostFailure{Code: "not_found", Message: "unknown available gateway"}
	}
	g.attached = true
	g.timer.Stop()
	var once sync.Once
	release := func() {
		once.Do(func() {
			m.mu.Lock()
			if m.gateways[id] == g {
				m.closeLocked(id, g)
			}
			m.mu.Unlock()
		})
	}
	return g.conn, release, nil
}
func (m *browserPublications) stop(owner string) {
	m.mu.Lock()
	if owner == "" {
		m.closed = true
	}
	var done []chan struct{}
	for id, g := range m.gateways {
		if owner == "" || g.owner == owner {
			done = append(done, g.done)
			m.closeLocked(id, g)
		}
	}
	for id, p := range m.mounts {
		if owner == "" || p.Owner == owner {
			delete(m.mounts, id)
		}
	}
	if m.runtime != nil && (owner == "" || m.runtime.Owner == owner) {
		m.runtime = nil
	}
	m.mu.Unlock()
	for _, closed := range done {
		<-closed
	}
}
func (c *Controller) BrowserRuntime(id string) (BrowserRuntime, []BrowserPublication, bool) {
	m := c.browserPublications
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runtime == nil || m.runtime.ID != id {
		return BrowserRuntime{}, nil, false
	}
	publications := []BrowserPublication{}
	for _, p := range m.mounts {
		if p.RuntimeID == id && p.Owner == m.runtime.Owner {
			publications = append(publications, p)
		}
	}
	return *m.runtime, publications, true
}
func (c *Controller) BrowserPublication(relative string) (BrowserPublication, bool) {
	c.browserPublications.mu.Lock()
	defer c.browserPublications.mu.Unlock()
	for _, p := range c.browserPublications.mounts {
		if relative == p.MountPath || strings.HasPrefix(relative, p.MountPath+"/") {
			return p, true
		}
	}
	return BrowserPublication{}, false
}
func (c *Controller) ConsumeBrowserStreamTicket(ticket string) (BrowserStreamGrant, bool) {
	return c.browserPublications.consume(ticket)
}
func (c *Controller) AttachBrowserStream(owner, id string) (net.Conn, func(), error) {
	return c.browserPublications.attach(owner, id)
}
func (h controllerPluginHost) BrowserCapability(_ context.Context, owner, method string, raw json.RawMessage) (json.RawMessage, error) {
	return h.controller.browserPublications.call(owner, h.controller.Snapshot().Server.BasePath, method, raw)
}

// SetBrowserBasePath pins browser publications to the actual normalized listener
// prefix, including a command-line override of persisted configuration.
func (c *Controller) SetBrowserBasePath(base string) error {

	m := c.browserPublications
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.gateways) > 0 && m.basePath != base {
		return networkInvalid("cannot change browser base path with active gateways")
	}
	m.basePath = base
	return nil
}
