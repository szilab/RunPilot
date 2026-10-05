package httpgateway

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/browserpath"
)

type Config struct {
	UpstreamURL      string `json:"upstreamURL"`
	UpstreamBasePath string `json:"upstreamBasePath"`
	PublicPrefix     string `json:"publicPrefix"`
}
type Request struct {
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Headers http.Header `json:"headers"`
}
type Response struct {
	Status  int         `json:"status"`
	Headers http.Header `json:"headers"`
}
type exchange struct {
	cancel   context.CancelFunc
	upload   chan []byte
	pipe     *io.PipeWriter
	mu       sync.Mutex
	credit   int
	uploaded int
	ended    bool
	wake     chan struct{}
}
type Gateway struct {
	lastID    uint32
	config    Config
	upstream  *url.URL
	client    *http.Client
	jar       *boundedCookieJar
	transport *http.Transport
	ctx       context.Context
	cancel    context.CancelFunc
	conn      net.Conn
	writeMu   sync.Mutex
	mu        sync.Mutex
	exchanges map[uint32]*exchange
	wg        sync.WaitGroup
}

func New(config Config, conn net.Conn) (*Gateway, error) {
	upstream, err := url.Parse(config.UpstreamURL)
	if err != nil || len(config.UpstreamURL) > 2048 || upstream == nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" || upstream.Hostname() == "" || upstream.ForceQuery || upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" || (upstream.Path != "" && upstream.Path != "/") {
		return nil, errors.New("upstream must be an HTTP(S) origin without credentials")
	}
	if err := browserpath.Validate(config.PublicPrefix, false); err != nil {
		return nil, err
	}
	if err := browserpath.Validate(config.UpstreamBasePath, true); err != nil {
		return nil, err
	}
	jar := newCookieJar()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Always reach the configured host directly.
	transport.DisableCompression = true
	transport.MaxResponseHeaderBytes = MaxMetadata
	transport.MaxConnsPerHost = MaxExchanges
	transport.ResponseHeaderTimeout = 30 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	return &Gateway{config: config, upstream: upstream, transport: transport, jar: jar, client: &http.Client{Transport: transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, ctx: ctx, cancel: cancel, conn: conn, exchanges: map[uint32]*exchange{}}, nil
}
func (g *Gateway) Close() {
	g.cancel()
	_ = g.conn.Close()
	g.mu.Lock()
	for _, e := range g.exchanges {
		e.cancel()
		_ = e.pipe.CloseWithError(context.Canceled)
	}
	g.mu.Unlock()
	g.transport.CloseIdleConnections()
}
func (g *Gateway) send(kind byte, id uint32, data []byte) error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	_ = g.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	err := WriteFrame(g.conn, Frame{kind, id, data})
	if err != nil {
		g.cancel()
		_ = g.conn.Close()
	}
	return err
}
func (g *Gateway) Serve() {
	defer func() { g.Close(); g.wg.Wait() }()
	for {
		_ = g.conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		f, err := ReadFrame(g.conn)
		if err != nil {
			return
		}
		if err = g.accept(f); err != nil {
			return
		} // Invalid framing/flow control fails the entire capability closed.
	}
}
func (g *Gateway) mapURL(value string) (*url.URL, error) {
	if len(value) > 8192 || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return nil, errors.New("invalid request path")
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || u.IsAbs() || u.Host != "" {
		return nil, errors.New("relative path required")
	}
	prefix := g.config.PublicPrefix
	if u.Path != prefix && !strings.HasPrefix(u.Path, prefix+"/") {
		return nil, errors.New("path outside publication")
	}
	// Reject recursive traversal before mapping, while preserving escaped filenames.
	decoded := u.EscapedPath()
	for i := 0; i < 4; i++ {
		next, e := url.PathUnescape(decoded)
		if e != nil {
			return nil, e
		}
		for _, part := range strings.Split(next, "/") {
			if part == ".." || part == "." {
				return nil, errors.New("path traversal")
			}
		}
		if strings.ContainsAny(next, "\\\x00\r\n") {
			return nil, errors.New("invalid path")
		}
		if next == decoded {
			break
		}
		decoded = next
	}
	if strings.Contains(decoded, "%") {
		return nil, errors.New("ambiguous path")
	}
	suffix := strings.TrimPrefix(u.Path, prefix)
	escapedSuffix := strings.TrimPrefix(u.EscapedPath(), browserpath.Escape(prefix))
	if escapedSuffix == u.EscapedPath() && u.Path != prefix {
		return nil, errors.New("encoded public prefix")
	}
	out := *g.upstream
	out.Path = strings.TrimSuffix(g.config.UpstreamBasePath, "/") + suffix
	if out.Path == "" {
		out.Path = "/"
	}
	out.RawPath = browserpath.Escape(strings.TrimSuffix(g.config.UpstreamBasePath, "/")) + escapedSuffix
	out.RawQuery = u.RawQuery
	return &out, nil
}
func safeHeaders(in http.Header, request bool) (http.Header, error) {
	if len(in) > 64 {
		return nil, errors.New("too many headers")
	}
	out := make(http.Header)
	for k, values := range in {
		for _, v := range values {
			out.Add(k, v)
		}
	}
	size := 0
	for k, vs := range out {
		for _, c := range k {
			if !(c > 32 && c < 127 && !strings.ContainsRune("()<>@,;:\\\"/[]?={} ", c)) {
				return nil, errors.New("invalid header name")
			}
		}
		for _, v := range vs {
			size += len(k) + len(v)
			if strings.ContainsAny(v, "\r\n\x00") {
				return nil, errors.New("invalid header value")
			}
		}
	}
	if size > MaxMetadata {
		return nil, errors.New("headers too large")
	}
	for _, value := range out.Values("Connection") {
		for _, key := range strings.Split(value, ",") {
			out.Del(strings.TrimSpace(key))
		}
	}
	for key := range out {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "proxy-") {
			out.Del(key)
		}
	}
	for _, key := range []string{"Host", "Connection", "Keep-Alive", "Transfer-Encoding", "TE", "Trailer", "Upgrade", "Set-Cookie"} {
		out.Del(key)
	}
	if request {
		for _, key := range []string{"Cookie", "Content-Length", "Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-For", "Service-Worker"} {
			out.Del(key)
		}
	}
	return out, nil
}
func (g *Gateway) accept(f Frame) error {
	if f.Type == Start {
		if f.ID <= g.lastID {
			return errors.New("request IDs must increase")
		}
		g.lastID = f.ID
		var meta Request
		decoder := json.NewDecoder(strings.NewReader(string(f.Data)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&meta) != nil {
			return errors.New("invalid request metadata")
		}
		switch meta.Method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		default:
			return errors.New("unsupported method")
		}
		u, err := g.mapURL(meta.Path)
		if err != nil {
			return err
		}
		headers, err := safeHeaders(meta.Headers, true)
		if err != nil {
			return err
		}
		if headers.Get("Origin") != "" || meta.Method == "POST" || meta.Method == "PUT" || meta.Method == "PATCH" || meta.Method == "DELETE" {
			headers.Set("Origin", g.upstream.Scheme+"://"+g.upstream.Host)
		}
		if value := headers.Get("Referer"); value != "" {
			headers.Del("Referer")
			if ref, err := url.Parse(value); err == nil && (ref.Scheme == "http" || ref.Scheme == "https") {
				if mapped, err := g.mapURL(ref.RequestURI()); err == nil {
					headers.Set("Referer", mapped.String())
				}
			}
		}
		reader, writer := io.Pipe()
		ctx, cancel := context.WithCancel(g.ctx)
		e := &exchange{cancel: cancel, upload: make(chan []byte, 5), pipe: writer, wake: make(chan struct{}, 1), uploaded: Window}
		g.mu.Lock()
		if len(g.exchanges) >= MaxExchanges || g.exchanges[f.ID] != nil {
			g.mu.Unlock()
			cancel()
			_ = reader.Close()
			_ = writer.Close()
			return errors.New("exchange limit/duplicate ID")
		}
		g.exchanges[f.ID] = e
		g.mu.Unlock()
		req, err := http.NewRequestWithContext(ctx, meta.Method, u.String(), reader)
		if err != nil {
			return err
		}
		req.Header = headers
		g.wg.Add(2)
		go func() {
			defer g.wg.Done()
			defer writer.Close()
			for {
				select {
				case <-ctx.Done():
					return
				case data := <-e.upload:
					if data == nil {
						return
					}
					if _, err := writer.Write(data); err != nil {
						return
					}
					e.mu.Lock()
					e.uploaded += len(data)
					e.mu.Unlock()
					if g.send(UploadCredit, f.ID, Credit(len(data))) != nil {
						return
					}
				}
			}
		}()
		go func() { defer g.wg.Done(); g.execute(f.ID, e, req, reader) }()
		return g.send(UploadCredit, f.ID, Credit(Window))
	}
	g.mu.Lock()
	e := g.exchanges[f.ID]
	g.mu.Unlock()
	if e == nil {
		if f.ID <= g.lastID && (f.Type == Cancel || f.Type == DownloadCredit || f.Type == End || f.Type == Body) {
			return nil
		}
		return errors.New("unknown exchange")
	}
	switch f.Type {
	case Body:
		if len(f.Data) == 0 {
			return errors.New("empty body chunk")
		}
		e.mu.Lock()
		if e.ended || len(f.Data) > e.uploaded {
			e.mu.Unlock()
			return errors.New("upload window exceeded")
		}
		e.uploaded -= len(f.Data)
		e.mu.Unlock()
		select {
		case e.upload <- f.Data:
		default:
			return errors.New("upload buffer exceeded")
		}
	case End:
		if len(f.Data) != 0 {
			return errors.New("invalid end frame")
		}
		e.mu.Lock()
		if e.ended {
			e.mu.Unlock()
			return errors.New("duplicate end")
		}
		e.ended = true
		e.mu.Unlock()
		select {
		case e.upload <- nil:
		default:
			return errors.New("upload buffer exceeded")
		}
	case Cancel:
		if len(f.Data) != 0 {
			return errors.New("invalid cancel frame")
		}
		e.cancel()
		_ = e.pipe.CloseWithError(context.Canceled)
	case DownloadCredit:
		if len(f.Data) != 4 {
			return errors.New("invalid credit")
		}
		n := int(binary.BigEndian.Uint32(f.Data))
		e.mu.Lock()
		if n < 1 || n > Window || e.credit+n > Window {
			e.mu.Unlock()
			return errors.New("download window exceeded")
		}
		e.credit += n
		e.mu.Unlock()
		select {
		case e.wake <- struct{}{}:
		default:
		}
	default:
		return errors.New("unknown message type")
	}
	return nil
}
func (g *Gateway) execute(id uint32, e *exchange, req *http.Request, reader *io.PipeReader) {
	defer func() {
		e.cancel()
		_ = reader.Close()
		_ = e.pipe.Close()
		g.mu.Lock()
		delete(g.exchanges, id)
		g.mu.Unlock()
	}()
	resp, err := g.client.Do(req)
	if err != nil {
		_ = g.send(ResponseError, id, []byte("upstream request failed"))
		return
	}
	defer resp.Body.Close()
	if g.jar.exceeded() {
		_ = g.send(ResponseError, id, []byte("upstream session cookie limit exceeded"))
		return
	}
	headers, err := safeHeaders(resp.Header, false)
	if err != nil {
		_ = g.send(ResponseError, id, []byte("invalid upstream headers"))
		return
	}
	// The client consumes Set-Cookie into its ephemeral jar; synthetic responses never expose it.
	if location := headers.Get("Location"); location != "" {
		rewritten, err := g.redirect(req.URL, location)
		if err != nil {
			_ = g.send(ResponseError, id, []byte("unsafe upstream redirect"))
			return
		}
		headers.Set("Location", rewritten)
	}
	// Upstream scripts cannot claim a worker scope covering the RunPilot shell.
	headers.Del("Service-Worker-Allowed")
	meta, _ := json.Marshal(Response{resp.StatusCode, headers})
	if len(meta) > MaxMetadata {
		_ = g.send(ResponseError, id, []byte("response metadata too large"))
		return
	}
	if g.send(ResponseStart, id, meta) != nil {
		return
	}
	buffer := make([]byte, Chunk)
	for {
		// Wait before reading: both upstream reads and browser queues are bounded by explicit credit.
		allowed := 0
		for allowed == 0 {
			e.mu.Lock()
			allowed = e.credit
			if allowed > Chunk {
				allowed = Chunk
			}
			e.mu.Unlock()
			if allowed == 0 {
				select {
				case <-req.Context().Done():
					return
				case <-e.wake:
				}
			}
		}
		n, readErr := resp.Body.Read(buffer[:allowed])
		if n > 0 {
			e.mu.Lock()
			e.credit -= n
			e.mu.Unlock()
			if g.send(ResponseBody, id, buffer[:n]) != nil {
				return
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				_ = g.send(ResponseEnd, id, nil)
			} else {
				_ = g.send(ResponseError, id, []byte("upstream response interrupted"))
			}
			return
		}
	}
}
func (g *Gateway) redirect(current *url.URL, value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	u = current.ResolveReference(u)
	if u.Scheme != "http" && u.Scheme != "https" || u.User != nil {
		return "", errors.New("unsafe redirect")
	}
	if sameOrigin(u, g.upstream) {
		base := strings.TrimSuffix(g.config.UpstreamBasePath, "/")
		if u.Path != base && !strings.HasPrefix(u.Path, base+"/") {
			return "", errors.New("redirect outside configured upstream base path")
		}
		escaped := u.EscapedPath()
		u.Path = g.config.PublicPrefix + strings.TrimPrefix(u.Path, base)
		u.RawPath = browserpath.Escape(g.config.PublicPrefix) + strings.TrimPrefix(escaped, browserpath.Escape(base))
		u.Scheme = ""
		u.Host = ""
	}
	return u.String(), nil
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}
