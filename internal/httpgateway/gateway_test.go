package httpgateway

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type testTunnel struct {
	t       *testing.T
	conn    net.Conn
	frames  chan Frame
	pending map[uint32][]Frame
	write   sync.Mutex
	next    uint32
}

func openTunnel(t *testing.T, server, public, upstream string) *testTunnel {
	return openTunnelConfig(t, Config{UpstreamURL: server, UpstreamBasePath: upstream, PublicPrefix: public})
}

func openTunnelConfig(t *testing.T, config Config) *testTunnel {
	t.Helper()
	browser, host := net.Pipe()
	g, err := New(config, host)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { g.Serve(); close(done) }()
	c := &testTunnel{t: t, conn: browser, frames: make(chan Frame, 256)}
	go func() {
		defer close(c.frames)
		for {
			f, e := ReadFrame(browser)
			if e != nil {
				return
			}
			c.frames <- f
		}
	}()
	t.Cleanup(func() {
		_ = browser.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("gateway failed to close")
		}
	})
	return c
}

func TestProxyCompatibilityHeadersAtRootAndNestedBasePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"host": r.Header.Get("X-Forwarded-Host"), "scheme": r.Header.Get("X-Forwarded-Proto"), "prefix": r.Header.Get("X-Forwarded-Prefix"), "script": r.Header.Get("X-Script-Name")})
	}))
	defer server.Close()
	for _, test := range []struct{ prefix, header, wantPrefix, wantScript string }{
		{"/app", "X-Forwarded-Prefix", "/app", ""},
		{"/p/app", "X-Forwarded-Prefix", "/p/app", ""},
		{"/app", "X-Script-Name", "", "/app"},
		{"/p/app", "X-Script-Name", "", "/p/app"},
	} {
		t.Run(test.prefix+"/"+test.header, func(t *testing.T) {
			config := Config{UpstreamURL: server.URL, UpstreamBasePath: "/", PublicPrefix: test.prefix, BasePathHeader: test.header, ForwardPublicHost: true, ForwardPublicScheme: true, PublicHost: "public.example:8443", PublicScheme: "https"}
			response, body := openTunnelConfig(t, config).request("GET", test.prefix+"/headers", nil, nil)
			var got map[string]string
			if response.Status != 200 || json.Unmarshal(body, &got) != nil || got["host"] != "public.example:8443" || got["scheme"] != "https" || got["prefix"] != test.wantPrefix || got["script"] != test.wantScript {
				t.Fatalf("headers = %s (%+v)", body, response)
			}
		})
	}
}

func TestCustomHeaderValidation(t *testing.T) {
	for _, name := range []string{"Host", "Content-Length", "Transfer-Encoding", "Connection", "Upgrade", "Cookie", "Set-Cookie", "Authorization", "Proxy-Authorization", "Proxy-Thing", "X-Forwarded-Host", "X-Forwarded-Prefix", "Origin", "Accept-Encoding", "Range", "Sec-WebSocket-Protocol", "Bad Header"} {
		browser, host := net.Pipe()
		_, err := New(Config{UpstreamURL: "http://localhost", UpstreamBasePath: "/", PublicPrefix: "/app", CustomHeaders: http.Header{name: {"x"}}}, host)
		_ = browser.Close()
		_ = host.Close()
		if err == nil {
			t.Fatalf("accepted forbidden custom header %q", name)
		}
	}
	if err := validateCustomHeaders(http.Header{"X-Site-Mode": {"compat"}}); err != nil {
		t.Fatal(err)
	}
}
func (c *testTunnel) send(kind byte, id uint32, data []byte) {
	c.t.Helper()
	c.write.Lock()
	defer c.write.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := WriteFrame(c.conn, Frame{kind, id, data}); err != nil {
		c.t.Error(err)
	}
}
func (c *testTunnel) nextFrame() Frame {
	c.t.Helper()
	select {
	case f, ok := <-c.frames:
		if !ok {
			c.t.Fatal("gateway closed")
		}
		return f
	case <-time.After(5 * time.Second):
		c.t.Fatal("gateway timed out")
	}
	return Frame{}
}
func (c *testTunnel) nextFrameFor(id uint32) Frame {
	c.t.Helper()
	if pending := c.pending[id]; len(pending) > 0 {
		frame := pending[0]
		c.pending[id] = pending[1:]
		return frame
	}
	for {
		frame := c.nextFrame()
		if frame.ID == id {
			return frame
		}
		if c.pending == nil {
			c.pending = map[uint32][]Frame{}
		}
		c.pending[frame.ID] = append(c.pending[frame.ID], frame)
	}
}
func (c *testTunnel) request(method, path string, headers http.Header, body []byte) (Response, []byte) {
	c.t.Helper()
	c.next++
	id := c.next
	meta, _ := json.Marshal(Request{method, path, headers})
	c.send(Start, id, meta)
	upload := make(chan int, 8)
	stop := make(chan struct{})
	uploaded := make(chan struct{})
	go func() {
		defer close(uploaded)
		offset, available := 0, 0
		for offset < len(body) {
			if available == 0 {
				select {
				case available = <-upload:
				case <-stop:
					return
				}
			}
			n := len(body) - offset
			if n > Chunk {
				n = Chunk
			}
			if n > available {
				n = available
			}
			c.send(Body, id, body[offset:offset+n])
			offset += n
			available -= n
		}
		c.send(End, id, nil)
	}()
	c.send(DownloadCredit, id, Credit(Window))
	var response Response
	var result bytes.Buffer
	defer func() { close(stop); <-uploaded }()
	for {
		f := c.nextFrameFor(id)
		switch f.Type {
		case UploadCredit:
			select {
			case upload <- int(binary.BigEndian.Uint32(f.Data)):
			default:
			}
		case ResponseStart:
			if json.Unmarshal(f.Data, &response) != nil {
				c.t.Fatal("bad metadata")
			}
		case ResponseBody:
			result.Write(f.Data)
			c.send(DownloadCredit, id, Credit(len(f.Data)))
		case ResponseEnd:
			return response, result.Bytes()
		case ResponseError:
			c.t.Fatalf("response failed: %s", f.Data)
		}
	}
}
func TestRoutingHTTPAndCookies(t *testing.T) {
	for _, route := range []struct{ base, mount, upstream string }{{"/", "/app", "/app"}, {"/p", "/app", "/p/app"}, {"/p", "/app", "/app"}, {"/tenant/control", "/app", "/different"}} {
		t.Run(route.base+route.upstream, func(t *testing.T) {
			public := strings.TrimSuffix(route.base, "/") + route.mount
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, route.upstream+"/") {
					http.NotFound(w, r)
					return
				}
				switch suffix := strings.TrimPrefix(r.URL.Path, route.upstream); suffix {
				case "/":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, `<script src="web/code.js"></script><link href="style.css"><img src="image.png">`)
				case "/web/code.js", "/style.css", "/image.png":
					fmt.Fprint(w, suffix)
				case "/escaped file":
					fmt.Fprintf(w, "%s?%s|%s", r.URL.EscapedPath(), r.URL.RawQuery, r.Host)
				case "/echo":
					_ = http.NewResponseController(w).EnableFullDuplex()
					_, _ = io.Copy(w, r.Body)
				case "/head":
					w.Header().Set("Content-Length", "99")
				case "/fail":
					w.WriteHeader(500)
				case "/login":
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "private", Path: route.upstream, HttpOnly: true})
					fmt.Fprint(w, "logged in")
				case "/cookie":
					cookie, _ := r.Cookie("session")
					if cookie != nil {
						fmt.Fprint(w, cookie.Value)
					}
				case "/redirect":
					w.Header().Set("Location", "http://"+r.Host+route.upstream+"/login?from=app")
					w.WriteHeader(302)
				case "/range":
					w.Header().Set("Accept-Ranges", "bytes")
					http.ServeContent(w, r, "media", time.Time{}, strings.NewReader("0123456789"))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			c := openTunnel(t, server.URL, public, route.upstream)
			response, body := c.request("GET", public+"/", nil, nil)
			if response.Status != 200 || !bytes.Contains(body, []byte(`src="web/code.js"`)) {
				t.Fatal(response, string(body))
			}
			for _, path := range []string{"/web/code.js", "/style.css", "/image.png"} {
				_, body = c.request("GET", public+path, nil, nil)
				if string(body) != path {
					t.Fatal(string(body))
				}
			}
			_, body = c.request("GET", public+"/escaped%20file?q=a%2Fb", http.Header{"host": {"evil:12"}}, nil)
			if !bytes.Contains(body, []byte(route.upstream+"/escaped%20file?q=a%2Fb")) || bytes.Contains(body, []byte("evil")) {
				t.Fatal(string(body))
			}
			response, body = c.request("HEAD", public+"/head", nil, nil)
			if response.Headers.Get("Content-Length") != "99" || len(body) != 0 {
				t.Fatal(response, string(body))
			}
			payload := bytes.Repeat([]byte("body"), 100000)
			response, body = c.request("POST", public+"/echo", nil, payload)
			if response.Status != 200 || !bytes.Equal(body, payload) {
				t.Fatal("POST", response.Status, len(body))
			}
			for _, method := range []string{"PUT", "PATCH", "DELETE"} {
				_, body = c.request(method, public+"/echo", nil, []byte("value"))
				if string(body) != "value" {
					t.Fatal(method, string(body))
				}
			}
			for _, code := range []int{404, 500} {
				path := "/missing"
				if code == 500 {
					path = "/fail"
				}
				response, _ = c.request("GET", public+path, nil, nil)
				if response.Status != code {
					t.Fatal(response)
				}
			}
			response, _ = c.request("GET", public+"/redirect", nil, nil)
			if response.Status != 302 || response.Headers.Get("Location") != public+"/login?from=app" {
				t.Fatal(response)
			}
			response, body = c.request("GET", public+"/range", http.Header{"range": {"bytes=2-5"}, "if-range": {""}}, nil)
			if response.Status != 206 || string(body) != "2345" || response.Headers.Get("Content-Range") != "bytes 2-5/10" || response.Headers.Get("Content-Length") != "4" {
				t.Fatal(response, string(body))
			}
			response, _ = c.request("POST", public+"/login", nil, nil)
			if response.Headers.Get("Set-Cookie") != "" {
				t.Fatal("cookie exposed")
			}
			_, body = c.request("GET", public+"/cookie", http.Header{"cookie": {"session=attacker"}}, nil)
			if string(body) != "private" {
				t.Fatal("jar missing", string(body))
			}
			second := openTunnel(t, server.URL, public, route.upstream)
			_, body = second.request("GET", public+"/cookie", nil, nil)
			if len(body) != 0 {
				t.Fatal("cookie leaked")
			}
		})
	}
}
func TestMappingAndRedirectValidation(t *testing.T) {
	browser, host := net.Pipe()
	defer browser.Close()
	g, err := New(Config{UpstreamURL: "http://localhost:1234", UpstreamBasePath: "/app", PublicPrefix: "/p/app"}, host)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, value := range []string{"http://evil/x", "//evil/x", "/other/x", "/p/app/../escape", "/p/app/%2e%2e/x", "/p/app/%252e%252e/x", "/p/app/%2e%2e%2fx", "/p/app/a%5cb"} {
		if _, e := g.mapURL(value); e == nil {
			t.Fatal("accepted", value)
		}
	}
	for _, value := range []string{"/p/app", "/p/app/", "/p/app/space%20name?x=%2F"} {
		u, e := g.mapURL(value)
		if e != nil || u.Host != "localhost:1234" {
			t.Fatal(value, u, e)
		}
	}
	current, _ := g.mapURL("/p/app/page")
	for input, want := range map[string]string{"login": "/p/app/login", "/app/login": "/p/app/login", "http://localhost:1234/app/login": "/p/app/login", "https://external.example/login": "https://external.example/login"} {
		got, e := g.redirect(current, input)
		if e != nil || got != want {
			t.Fatal(input, got, e)
		}
	}
	for _, value := range []string{"javascript:alert(1)", "http://a:b@evil/", "/outside"} {
		if _, e := g.redirect(current, value); e == nil {
			t.Fatal("unsafe redirect", value)
		}
	}
	headers, e := safeHeaders(http.Header{"connection": {"X-Hidden"}, "x-hidden": {"secret"}, "proxy-authorization": {"secret"}, "upgrade": {"websocket"}, "authorization": {"Bearer app-token"}}, true)
	if e != nil || headers.Get("X-Hidden") != "" || headers.Get("Upgrade") != "" || headers.Get("Proxy-Authorization") != "" || headers.Get("Authorization") != "Bearer app-token" {
		t.Fatal(headers, e)
	}
}

func TestWebSocketTunnelAndProxyHeaders(t *testing.T) {
	type observed struct{ host, proto, prefix, script, custom, origin, path, protocol, cookie string }
	seen := make(chan observed, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/login" {
			http.SetCookie(w, &http.Cookie{Name: "webapp-session", Value: "secret-cookie", Path: "/app", HttpOnly: true})
			_, _ = io.WriteString(w, "ok")
			return
		}
		if r.URL.Path != "/app/socket" {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"echo"}})
		if err != nil {
			return
		}
		seen <- observed{r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-Prefix"), r.Header.Get("X-Script-Name"), r.Header.Get("X-Feature-Mode"), r.Header.Get("Origin"), r.URL.RequestURI(), conn.Subprotocol(), r.Header.Get("Cookie")}
		ctx := context.Background()
		kind, data, err := conn.Read(ctx)
		if err == nil {
			_ = conn.Write(ctx, kind, data)
		}
		_ = conn.Close(websocket.StatusNormalClosure, "finished")
	}))
	defer server.Close()
	browser, host := net.Pipe()
	config := Config{UpstreamURL: server.URL, UpstreamBasePath: "/app", PublicPrefix: "/p/app", BasePathHeader: "X-Forwarded-Prefix", ForwardPublicHost: true, ForwardPublicScheme: true, PublicHost: "public.example", PublicScheme: "https", CustomHeaders: http.Header{"X-Feature-Mode": {"safe"}}}
	g, err := New(config, host)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { g.Serve(); close(done) }()
	c := &testTunnel{t: t, conn: browser, frames: make(chan Frame, 32)}
	go func() {
		defer close(c.frames)
		for {
			frame, err := ReadFrame(browser)
			if err != nil {
				return
			}
			c.frames <- frame
		}
	}()
	t.Cleanup(func() {
		_ = browser.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("WebSocket gateway did not close")
		}
	})
	login, _ := c.request("GET", "/p/app/login", nil, nil)
	if login.Headers.Get("Set-Cookie") != "" {
		t.Fatal("upstream cookie escaped into synthetic browser response")
	}
	id := WebSocketIDMask | 1
	open, _ := json.Marshal(webSocketOpen{Path: "/p/app/socket?x=%2F", Protocols: []string{"echo"}})
	c.send(WebSocketOpen, id, open)
	opened := c.nextFrame()
	if opened.Type != WebSocketOpened || opened.ID != id || !strings.Contains(string(opened.Data), `"protocol":"echo"`) {
		t.Fatal(opened)
	}
	c.send(WebSocketCredit, id, Credit(WebSocketWindow))
	c.send(WebSocketText, id, []byte{byte(websocket.MessageText), 1, 'h', 'e', 'l', 'l', 'o'})
	echo := c.nextFrame()
	if echo.Type != WebSocketText || echo.ID != id || string(echo.Data[2:]) != "hello" || echo.Data[1] != 1 {
		t.Fatal(echo)
	}
	concurrentHTTP, _ := c.request("GET", "/p/app/unrelated", nil, nil)
	if concurrentHTTP.Status != http.StatusNotFound {
		t.Fatalf("HTTP exchange failed while WebSocket was active: %+v", concurrentHTTP)
	}
	select {
	case got := <-seen:
		if got.host != "public.example" || got.proto != "https" || got.prefix != "/p/app" || got.script != "" || got.custom != "safe" || got.origin != server.URL || got.path != "/app/socket?x=%2F" || got.protocol != "echo" || got.cookie != "webapp-session=secret-cookie" {
			t.Fatalf("upstream handshake = %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not receive WebSocket handshake")
	}
	closeInfo, _ := json.Marshal(map[string]any{"code": 1000, "reason": "done"})
	c.send(WebSocketClose, id, closeInfo)
	got := c.nextFrameFor(id)
	if got.Type != WebSocketClosed || got.ID != id {
		t.Fatal("WebSocket close was not propagated", got)
	}
	var closeMetadata map[string]any
	if json.Unmarshal(got.Data, &closeMetadata) != nil || closeMetadata["code"] != float64(1000) || closeMetadata["reason"] != "finished" || closeMetadata["wasClean"] != true {
		t.Fatalf("upstream close metadata not propagated: %s", got.Data)
	}
	second := openTunnelConfig(t, Config{UpstreamURL: server.URL, UpstreamBasePath: "/app", PublicPrefix: "/p/app"})
	second.send(WebSocketOpen, WebSocketIDMask|1, open)
	if got := second.nextFrame(); got.Type != WebSocketOpened {
		t.Fatal("second gateway WebSocket did not open", got)
	}
	select {
	case got := <-seen:
		if got.cookie != "" {
			t.Fatalf("WebSocket cookie leaked across gateway sessions: %q", got.cookie)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second WebSocket handshake was not observed")
	}
	secondClose, _ := json.Marshal(map[string]any{"code": 1000, "reason": "done"})
	second.send(WebSocketClose, WebSocketIDMask|1, secondClose)
	badID := WebSocketIDMask | 2
	badOpen, _ := json.Marshal(webSocketOpen{Path: "//attacker.example/socket"})
	c.send(WebSocketOpen, badID, badOpen)
	if got := c.nextFrame(); got.Type != WebSocketError || got.ID != badID {
		t.Fatal("out-of-publication WebSocket path not rejected", got)
	}
	response, _ := c.request("GET", "/p/app/unrelated", nil, nil)
	if response.Status != http.StatusNotFound {
		t.Fatalf("unrelated HTTP exchange failed after WS rejection: %+v", response)
	}
	if _, err := g.mapURL("//attacker.example/socket"); err == nil {
		t.Fatal("arbitrary WebSocket host path accepted")
	}
}
func TestStreamingBackpressureCancellationAndClose(t *testing.T) {
	began := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(began)
		for {
			select {
			case <-r.Context().Done():
				close(canceled)
				return
			default:
				if _, err := w.Write(make([]byte, Chunk)); err != nil {
					<-r.Context().Done()
					close(canceled)
					return
				}
			}
		}
	}))
	defer server.Close()
	c := openTunnel(t, server.URL, "/app", "/")
	meta, _ := json.Marshal(Request{"GET", "/app/media", nil})
	c.send(Start, 1, meta)
	c.send(End, 1, nil)
	<-began
	for {
		if c.nextFrame().Type == ResponseStart {
			break
		}
	}
	select {
	case f := <-c.frames:
		t.Fatalf("uncredited body %d", f.Type)
	case <-time.After(50 * time.Millisecond):
	}
	c.send(DownloadCredit, 1, Credit(Chunk))
	f := c.nextFrame()
	if f.Type != ResponseBody || len(f.Data) != Chunk {
		t.Fatal(f.Type, len(f.Data))
	}
	select {
	case f := <-c.frames:
		t.Fatalf("credit exceeded %d", f.Type)
	case <-time.After(50 * time.Millisecond):
	}
	c.send(Cancel, 1, nil)
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not canceled")
	}
}
func TestCloseCancelsRequestAndRequestStreaming(t *testing.T) {
	first := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf [4]byte
		_, _ = io.ReadFull(r.Body, buf[:])
		close(first)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	c := openTunnel(t, server.URL, "/app", "/")
	meta, _ := json.Marshal(Request{"POST", "/app/upload", nil})
	c.send(Start, 1, meta)
	c.send(Body, 1, []byte("part"))
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("request buffered until end")
	}
	_ = c.conn.Close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("close failed to cancel")
	}
}
func TestLimitsAndProtocolViolations(t *testing.T) {
	for _, test := range []struct {
		name   string
		frames []Frame
	}{{"CONNECT", []Frame{{Start, 1, []byte(`{"method":"CONNECT","path":"/app/"}`)}}}, {"TRACE", []Frame{{Start, 1, []byte(`{"method":"TRACE","path":"/app/"}`)}}}, {"absolute URL", []Frame{{Start, 1, []byte(`{"method":"GET","path":"http://evil/"}`)}}}, {"credit overflow", []Frame{{Start, 1, []byte(`{"method":"POST","path":"/app/"}`)}, {DownloadCredit, 1, Credit(Window + 1)}}}} {
		t.Run(test.name, func(t *testing.T) {
			c := openTunnel(t, "http://127.0.0.1:1234", "/app", "/")
			for _, f := range test.frames {
				c.send(f.Type, f.ID, f.Data)
			}
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			for {
				select {
				case _, ok := <-c.frames:
					if !ok {
						return
					}
				case <-timer.C:
					t.Fatal("invalid protocol did not close")
				}
			}
		})
	}
	headers := http.Header{}
	for i := 0; i < 65; i++ {
		headers.Set(fmt.Sprintf("X-%d", i), "value")
	}
	if _, err := safeHeaders(headers, true); err == nil {
		t.Fatal("header count exceeded")
	}
	if _, err := safeHeaders(http.Header{"X-Big": {strings.Repeat("x", MaxMetadata)}}, true); err == nil {
		t.Fatal("header bytes exceeded")
	}
}

func TestCookieJarBoundedAndExpiry(t *testing.T) {
	jar := newCookieJar()
	u, _ := url.Parse("http://host/app/page")
	for i := 0; i < maxSessionCookies; i++ {
		jar.SetCookies(u, []*http.Cookie{{Name: fmt.Sprintf("n%d", i), Value: "private", Path: "/app"}})
	}
	if len(jar.Cookies(u)) != maxSessionCookies {
		t.Fatal("cookies lost before limit")
	}
	jar.SetCookies(u, []*http.Cookie{{Name: "overflow", Value: "private", Path: "/app"}})
	if !jar.exceeded() || len(jar.Cookies(u)) != maxSessionCookies {
		t.Fatal("cookie count exceeded")
	}
	jar.SetCookies(u, []*http.Cookie{{Name: "n0", MaxAge: -1, Path: "/app"}})
	if len(jar.Cookies(u)) != maxSessionCookies-1 {
		t.Fatal("cookie removal failed")
	}
	next := newCookieJar()
	next.SetCookies(u, []*http.Cookie{{Name: "oversized", Value: strings.Repeat("x", maxSessionCookieBytes), Path: "/app"}})
	if !next.exceeded() || len(next.Cookies(u)) != 0 {
		t.Fatal("cookie bytes exceeded")
	}
}

func TestConcurrentExchangeLimitAndMultiplexing(t *testing.T) {
	started := make(chan struct{}, MaxExchanges)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	c := openTunnel(t, server.URL, "/app", "/")
	for id := uint32(1); id <= MaxExchanges; id++ {
		meta, _ := json.Marshal(Request{"GET", "/app/media", nil})
		c.send(Start, id, meta)
		c.send(End, id, nil)
	}
	for range MaxExchanges {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent requests did not start")
		}
	}
	seen := map[uint32]bool{}
	for len(seen) < MaxExchanges {
		f := c.nextFrame()
		if f.Type == ResponseStart {
			seen[f.ID] = true
		}
	}
	meta, _ := json.Marshal(Request{"GET", "/app/overflow", nil})
	c.send(Start, MaxExchanges+1, meta)
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-c.frames:
			if !ok {
				return
			}
		case <-timer.C:
			t.Fatal("exchange limit did not close gateway")
		}
	}
}
func TestFrameSizeAndVersionBounds(t *testing.T) {
	var b bytes.Buffer
	if WriteFrame(&b, Frame{Type: Body, ID: 1, Data: make([]byte, Chunk+1)}) == nil {
		t.Fatal("oversized frame written")
	}
	header := make([]byte, 10)
	header[0] = Version
	header[1] = Body
	binary.BigEndian.PutUint32(header[2:6], 1)
	binary.BigEndian.PutUint32(header[6:], Chunk+1)
	if _, err := ReadFrame(bytes.NewReader(header)); err == nil {
		t.Fatal("oversized frame accepted")
	}
	header[0] = Version + 1
	binary.BigEndian.PutUint32(header[6:], 0)
	if _, err := ReadFrame(bytes.NewReader(header)); err == nil {
		t.Fatal("unknown protocol version accepted")
	}
}

func TestEscapedFrameworkAndUpstreamPrefixes(t *testing.T) {
	browser, host := net.Pipe()
	defer browser.Close()
	g, err := New(Config{UpstreamURL: "http://localhost:1234", UpstreamBasePath: "/上游 base", PublicPrefix: "/tenant space/应用/app"}, host)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	u, err := g.mapURL("/tenant%20space/%E5%BA%94%E7%94%A8/app/file%20name?q=1")
	if err != nil || u.Path != "/上游 base/file name" || u.EscapedPath() != "/%E4%B8%8A%E6%B8%B8%20base/file%20name" {
		t.Fatal(u, err)
	}
	got, err := g.redirect(u, "http://localhost:1234/%E4%B8%8A%E6%B8%B8%20base/login")
	if err != nil || got != "/tenant%20space/%E5%BA%94%E7%94%A8/app/login" {
		t.Fatal(got, err)
	}
}

func TestRedirectOriginUsesDefaultPortSemantics(t *testing.T) {
	for _, upstream := range []string{"http://example.test:80", "https://example.test:443"} {
		browser, host := net.Pipe()
		g, err := New(Config{UpstreamURL: upstream, UpstreamBasePath: "/app", PublicPrefix: "/p/app"}, host)
		if err != nil {
			t.Fatal(err)
		}
		current, _ := g.mapURL("/p/app/")
		scheme := current.Scheme
		got, err := g.redirect(current, scheme+"://EXAMPLE.test/app/login")
		if err != nil || got != "/p/app/login" {
			t.Fatal(got, err)
		}
		external := scheme + "://example.test:1234/app/login"
		got, err = g.redirect(current, external)
		if err != nil || got != external {
			t.Fatal(got, err)
		}
		g.Close()
		_ = browser.Close()
	}
}
