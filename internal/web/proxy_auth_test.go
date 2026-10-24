package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseProxyAuth(t *testing.T) {
	if auth, err := ParseProxyAuth("", ""); auth != nil || err != nil {
		t.Fatalf("unset configuration = %v, %v", auth, err)
	}
	auth, err := ParseProxyAuth(" 10.0.0.5, 172.18.0.0/16 ::ffff:192.168.1.10,fd00::/8 ", "remote-user")
	if err != nil {
		t.Fatal(err)
	}
	if auth.Header() != "Remote-User" {
		t.Fatalf("header = %q", auth.Header())
	}
	if got, want := strings.Join(auth.TrustedProxies(), " "), "10.0.0.5/32 172.18.0.0/16 192.168.1.10/32 fd00::/8"; got != want {
		t.Fatalf("trusted proxies = %q, want %q", got, want)
	}
	for _, tc := range []struct{ proxies, header, want string }{
		{"10.0.0.5", "", "needs both"},
		{"", "Remote-User", "needs both"},
		{"10.0.0.5", "Remote User", "not a valid HTTP header name"},
		{"10.0.0.5", "authorization", "cannot carry"},
		{"10.0.0.5", "Cookie", "cannot carry"},
		{"proxy.local", "Remote-User", "invalid address"},
		{"10.0.0.0/33", "Remote-User", "invalid address range"},
		{"0.0.0.0/0", "Remote-User", "every address"},
		{"::/0", "Remote-User", "every address"},
		{",", "Remote-User", "lists no proxy"},
	} {
		if _, err := ParseProxyAuth(tc.proxies, tc.header); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseProxyAuth(%q, %q) error = %v, want %q", tc.proxies, tc.header, err, tc.want)
		}
	}
}

type proxyAuthFixture struct {
	t      *testing.T
	server *Server
	token  string
}

func newProxyAuthFixture(t *testing.T, basePath string) proxyAuthFixture {
	t.Helper()
	ctrl, err := openReadyController(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctrl.Close)
	server, err := New(ctrl, basePath)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := ParseProxyAuth("10.0.0.5, 172.18.0.0/16", "Remote-User")
	if err != nil {
		t.Fatal(err)
	}
	server.SetProxyAuth(auth)
	return proxyAuthFixture{t: t, server: server, token: ctrl.Snapshot().Server.Token}
}

func (f proxyAuthFixture) do(method, path, peer string, configure func(*http.Request)) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = peer
	if configure != nil {
		configure(request)
	}
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	return response
}

func responseCookies(response *httptest.ResponseRecorder) map[string]*http.Cookie {
	cookies := map[string]*http.Cookie{}
	for _, cookie := range response.Result().Cookies() {
		cookies[cookie.Name] = cookie
	}
	return cookies
}

func TestProxyAuthSignsInTheAdministratorFromATrustedProxy(t *testing.T) {
	f := newProxyAuthFixture(t, "")
	first := f.do(http.MethodGet, "/api/v1/system", "172.18.0.3:40000", func(r *http.Request) { r.Header.Set("Remote-User", "admin") })
	if first.Code != http.StatusOK {
		t.Fatalf("proxy-authenticated status = %d: %s", first.Code, first.Body.String())
	}
	cookies := responseCookies(first)
	session, csrf := cookies[adminSessionCookie], cookies[csrfCookie]
	if session == nil || csrf == nil || !session.HttpOnly {
		t.Fatalf("proxy sign-in did not issue session cookies: %v", cookies)
	}
	if !strings.Contains(first.Body.String(), `"header":"Remote-User"`) {
		t.Fatalf("system info does not report proxy authentication: %s", first.Body.String())
	}
	withSession := func(r *http.Request) {
		r.Header.Set("Remote-User", "admin")
		r.AddCookie(session)
		r.AddCookie(csrf)
	}
	// The session behaves like a password session: unsafe requests need CSRF.
	if response := f.do(http.MethodPost, "/api/v1/ws/ticket", "172.18.0.3:40000", withSession); response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF header status = %d", response.Code)
	}
	ticket := f.do(http.MethodPost, "/api/v1/ws/ticket", "172.18.0.3:40000", func(r *http.Request) {
		withSession(r)
		r.Header.Set("X-RunPilot-CSRF", csrf.Value)
	})
	if ticket.Code != http.StatusCreated {
		t.Fatalf("CSRF-protected request status = %d: %s", ticket.Code, ticket.Body.String())
	}
	if len(responseCookies(ticket)) != 0 {
		t.Fatal("an existing session was replaced")
	}
	// The session stays valid behind the proxy without repeating the header.
	if response := f.do(http.MethodGet, "/api/v1/plugins", "172.18.0.3:40000", func(r *http.Request) { r.AddCookie(session) }); response.Code != http.StatusOK {
		t.Fatalf("session without header status = %d", response.Code)
	}
	f.server.sessionMu.Lock()
	proxySessions := 0
	for _, value := range f.server.sessions {
		if value.proxy {
			proxySessions++
		}
	}
	f.server.sessionMu.Unlock()
	if proxySessions != 1 {
		t.Fatalf("proxy sessions = %d, want 1", proxySessions)
	}
}

func TestProxyAuthIgnoresUntrustedPeersAndForwardingHeaders(t *testing.T) {
	f := newProxyAuthFixture(t, "")
	for _, peer := range []string{"10.0.0.6:5000", "192.0.2.1:5000", "[2001:db8::1]:5000"} {
		response := f.do(http.MethodGet, "/api/v1/system", peer, func(r *http.Request) {
			r.Header.Set("Remote-User", "admin")
			r.Header.Set("X-Forwarded-For", "10.0.0.5")
			r.Header.Set("X-Real-IP", "10.0.0.5")
		})
		if response.Code != http.StatusUnauthorized || len(responseCookies(response)) != 0 {
			t.Fatalf("untrusted peer %s status = %d cookies = %v", peer, response.Code, responseCookies(response))
		}
	}
	// An IPv4-mapped peer address matches the IPv4 entry.
	if response := f.do(http.MethodGet, "/api/v1/system", "[::ffff:10.0.0.5]:5000", func(r *http.Request) { r.Header.Set("Remote-User", "admin") }); response.Code != http.StatusOK {
		t.Fatalf("IPv4-mapped trusted peer status = %d", response.Code)
	}
	// Without the header, a trusted proxy's requests use the normal methods.
	bearer := f.do(http.MethodGet, "/api/v1/system", "10.0.0.5:5000", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+f.token) })
	if bearer.Code != http.StatusOK || len(responseCookies(bearer)) != 0 {
		t.Fatalf("bearer through trusted proxy status = %d", bearer.Code)
	}
	if response := f.do(http.MethodGet, "/api/v1/system", "10.0.0.5:5000", func(r *http.Request) { r.Header.Set("Remote-User", " ") }); response.Code != http.StatusUnauthorized {
		t.Fatalf("empty proxy user status = %d", response.Code)
	}
	// Static assets never create sessions.
	if response := f.do(http.MethodGet, "/", "10.0.0.5:5000", func(r *http.Request) { r.Header.Set("Remote-User", "admin") }); len(responseCookies(response)) != 0 {
		t.Fatal("static page request created a session")
	}
}

func TestProxyAuthRefusesOtherUsersEvenWithCredentials(t *testing.T) {
	f := newProxyAuthFixture(t, "")
	login := f.do(http.MethodGet, "/api/v1/system", "10.0.0.5:5000", func(r *http.Request) { r.Header.Set("Remote-User", "admin") })
	session := responseCookies(login)[adminSessionCookie]
	for name, configure := range map[string]func(*http.Request){
		"other user":       func(r *http.Request) { r.Header.Set("Remote-User", "mallory") },
		"case differs":     func(r *http.Request) { r.Header.Set("Remote-User", "Admin") },
		"duplicate header": func(r *http.Request) { r.Header.Add("Remote-User", "admin"); r.Header.Add("Remote-User", "mallory") },
		"existing session": func(r *http.Request) { r.Header.Set("Remote-User", "mallory"); r.AddCookie(session) },
		"bearer token": func(r *http.Request) {
			r.Header.Set("Remote-User", "mallory")
			r.Header.Set("Authorization", "Bearer "+f.token)
		},
		"password sign-in": func(r *http.Request) {
			r.Header.Set("Remote-User", "mallory")
			r.Method = http.MethodPost
			r.URL.Path = "/api/v1/auth/login"
		},
		"websocket attempt": func(r *http.Request) {
			r.Header.Set("Remote-User", "mallory")
			r.AddCookie(session)
			r.URL.Path = "/api/v1/ws"
		},
	} {
		response := f.do(http.MethodGet, "/api/v1/system", "10.0.0.5:5000", configure)
		if response.Code != http.StatusForbidden || len(responseCookies(response)) != 0 {
			t.Errorf("%s: status = %d, cookies = %v", name, response.Code, responseCookies(response))
		}
	}
	if response := f.do(http.MethodGet, "/api/v1/setup", "10.0.0.5:5000", func(r *http.Request) { r.Header.Set("Remote-User", "mallory") }); response.Code != http.StatusOK {
		t.Fatalf("setup status endpoint = %d", response.Code)
	}
}

func TestProxyAuthHonorsBasePath(t *testing.T) {
	f := newProxyAuthFixture(t, "/runpilot")
	response := f.do(http.MethodGet, "/runpilot/api/v1/system", "10.0.0.5:5000", func(r *http.Request) { r.Header.Set("Remote-User", "admin") })
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if cookie := responseCookies(response)[adminSessionCookie]; cookie == nil || cookie.Path != "/runpilot" {
		t.Fatalf("session cookie = %+v", cookie)
	}
}

func TestProxyAuthDisabledIgnoresHeader(t *testing.T) {
	f := newProxyAuthFixture(t, "")
	f.server.SetProxyAuth(nil)
	if response := f.do(http.MethodGet, "/api/v1/system", "10.0.0.5:5000", func(r *http.Request) { r.Header.Set("Remote-User", "admin") }); response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled proxy auth status = %d", response.Code)
	}
}

func TestProxySessionsAreEvictedBeforePasswordSessions(t *testing.T) {
	f := newProxyAuthFixture(t, "")
	s := f.server
	now := time.Now()
	s.sessionMu.Lock()
	for i := 0; i < maxPersistedAuthSessions; i++ {
		s.sessions[authSessionKey(strings.Repeat("p", i+1))] = authSession{csrfHash: authSessionKey("csrf"), expires: now.Add(time.Duration(i+1) * time.Minute), proxy: true}
	}
	oldest := authSessionKey("p")
	s.sessionMu.Unlock()
	if key := s.issueSession(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)); key == "" {
		t.Fatal("a full table of proxy sessions blocked a password sign-in")
	}
	s.sessionMu.Lock()
	_, oldestKept := s.sessions[oldest]
	count := len(s.sessions)
	s.sessionMu.Unlock()
	if oldestKept || count != maxPersistedAuthSessions {
		t.Fatalf("eviction kept oldest=%v count=%d", oldestKept, count)
	}

	// Password sessions are never evicted to make room for proxy sessions.
	s.sessionMu.Lock()
	s.sessions = map[string]authSession{}
	for i := 0; i < maxPersistedAuthSessions; i++ {
		s.sessions[authSessionKey(strings.Repeat("w", i+1))] = authSession{csrfHash: authSessionKey("csrf"), expires: now.Add(time.Hour)}
	}
	s.sessionMu.Unlock()
	response := f.do(http.MethodGet, "/api/v1/system", "10.0.0.5:5000", func(r *http.Request) { r.Header.Set("Remote-User", "admin") })
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("full password session table status = %d", response.Code)
	}
}

func TestProxySessionFlagIsPersisted(t *testing.T) {
	f := newProxyAuthFixture(t, "")
	response := f.do(http.MethodGet, "/api/v1/system", "10.0.0.5:5000", func(r *http.Request) { r.Header.Set("Remote-User", "admin") })
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	reloaded, err := New(f.server.ctrl)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.sessionMu.Lock()
	defer reloaded.sessionMu.Unlock()
	if len(reloaded.sessions) != 1 {
		t.Fatalf("reloaded sessions = %d", len(reloaded.sessions))
	}
	for _, session := range reloaded.sessions {
		if !session.proxy {
			t.Fatal("proxy marker was not persisted")
		}
	}
}

func (f proxyAuthFixture) passwordSession() func(*http.Request) {
	_, cookie, csrf := f.server.createSession(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil), false)
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: cookie})
		r.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
		r.Header.Set("X-RunPilot-CSRF", csrf)
	}
}

func (f proxyAuthFixture) saveSettings(peer, body string, configure ...func(*http.Request)) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/proxy-auth", strings.NewReader(body))
	request.RemoteAddr = peer
	request.Header.Set("Content-Type", "application/json")
	for _, apply := range configure {
		apply(request)
	}
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	return response
}

func TestProxyAuthSettingsApplyImmediatelyAndPersist(t *testing.T) {
	f := newProxyAuthFixture(t, "")
	f.server.SetProxyAuth(nil)
	session := f.passwordSession()
	if response := f.do(http.MethodGet, "/api/v1/system", "10.0.0.9:1", func(r *http.Request) { r.Header.Set("Remote-User", "admin") }); response.Code != http.StatusUnauthorized {
		t.Fatalf("before saving: %d", response.Code)
	}
	saved := f.saveSettings("192.0.2.50:1", `{"trustedProxies":" 10.0.0.9 ","header":"x-forwarded-user"}`, session)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), `"enabled":true`) || !strings.Contains(saved.Body.String(), `"source":"settings"`) {
		t.Fatalf("save = %d %s", saved.Code, saved.Body.String())
	}
	if response := f.do(http.MethodGet, "/api/v1/system", "10.0.0.9:1", func(r *http.Request) { r.Header.Set("X-Forwarded-User", "admin") }); response.Code != http.StatusOK {
		t.Fatalf("saved settings not applied: %d", response.Code)
	}
	cfg := f.server.ctrl.Snapshot().Server
	if cfg.ProxyAuthTrustedProxies != "10.0.0.9" || cfg.ProxyAuthHeader != "x-forwarded-user" {
		t.Fatalf("persisted = %q / %q", cfg.ProxyAuthTrustedProxies, cfg.ProxyAuthHeader)
	}
	restarted, err := New(f.server.ctrl)
	if err != nil {
		t.Fatal(err)
	}
	if header, _, ok := restarted.ProxyAuthSummary(); !ok || header != "X-Forwarded-User" {
		t.Fatalf("settings not loaded after restart: %q %v", header, ok)
	}
	if off := f.saveSettings("192.0.2.50:1", `{"trustedProxies":"","header":""}`, session); off.Code != http.StatusOK || !strings.Contains(off.Body.String(), `"enabled":false`) {
		t.Fatalf("disable = %d %s", off.Code, off.Body.String())
	}
	if response := f.do(http.MethodGet, "/api/v1/system", "10.0.0.9:1", func(r *http.Request) { r.Header.Set("X-Forwarded-User", "admin") }); response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled settings still applied: %d", response.Code)
	}
}

func TestProxyAuthSettingsValidationAndLockoutGuard(t *testing.T) {
	f := newProxyAuthFixture(t, "")
	f.server.SetProxyAuth(nil)
	session := f.passwordSession()
	for body, want := range map[string]string{
		`{"trustedProxies":"10.0.0.9","header":""}`:             "needs both Trusted proxies and User name header",
		`{"trustedProxies":"0.0.0.0/0","header":"Remote-User"}`: "Trusted proxies",
		`{"trustedProxies":"10.0.0.9","header":"Cookie"}`:       "User name header: Cookie cannot carry",
	} {
		response := f.saveSettings("192.0.2.50:1", body, session)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), want) {
			t.Errorf("%s: %d %s", body, response.Code, response.Body.String())
		}
	}
	// This browser comes through 10.0.0.9, whose proxy reports another user.
	locked := f.saveSettings("10.0.0.9:1", `{"trustedProxies":"10.0.0.9","header":"Remote-User"}`, session, func(r *http.Request) { r.Header.Set("Remote-User", "mallory") })
	if locked.Code != http.StatusConflict || !strings.Contains(locked.Body.String(), "lock you out") {
		t.Fatalf("lockout guard = %d %s", locked.Code, locked.Body.String())
	}
	if f.server.ctrl.Snapshot().Server.ProxyAuthHeader != "" || f.server.proxyAuth.Load() != nil {
		t.Fatal("a refused configuration was saved")
	}
	if ok := f.saveSettings("10.0.0.9:1", `{"trustedProxies":"10.0.0.9","header":"Remote-User"}`, session, func(r *http.Request) { r.Header.Set("Remote-User", "admin") }); ok.Code != http.StatusOK {
		t.Fatalf("matching proxy user save = %d %s", ok.Code, ok.Body.String())
	}
	if csrf := f.saveSettings("192.0.2.50:1", `{"trustedProxies":"","header":""}`); csrf.Code == http.StatusOK {
		t.Fatal("settings saved without a CSRF-protected session")
	}
}

func TestProxyAuthEnvironmentOverridesSettings(t *testing.T) {
	f := newProxyAuthFixture(t, "")
	if err := f.server.ctrl.SetProxyAuth("10.0.0.9", "X-Forwarded-User"); err != nil {
		t.Fatal(err)
	}
	if err := f.server.ConfigureProxyAuth("172.18.0.1", "Remote-User"); err != nil {
		t.Fatal(err)
	}
	state := f.server.proxyAuthState()
	if state["source"] != "environment" || state["trustedProxies"] != "172.18.0.1" || state["header"] != "Remote-User" {
		t.Fatalf("state = %v", state)
	}
	session := f.passwordSession()
	if response := f.saveSettings("192.0.2.50:1", `{"trustedProxies":"","header":""}`, session); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "RUNPILOT_TRUSTED_PROXIES") {
		t.Fatalf("environment-managed save = %d %s", response.Code, response.Body.String())
	}
	if response := f.do(http.MethodGet, "/api/v1/system", "10.0.0.9:1", func(r *http.Request) { r.Header.Set("X-Forwarded-User", "admin") }); response.Code != http.StatusUnauthorized {
		t.Fatalf("settings applied despite environment override: %d", response.Code)
	}
	if err := f.server.ConfigureProxyAuth("172.18.0.1", ""); err == nil {
		t.Fatal("partial environment configuration accepted")
	}
}

func TestInvalidStoredProxyAuthSettingsStayDisabled(t *testing.T) {
	f := newProxyAuthFixture(t, "")
	if err := f.server.ctrl.SetProxyAuth("0.0.0.0/0", "Remote-User"); err != nil {
		t.Fatal(err)
	}
	if err := f.server.ConfigureProxyAuth("", ""); err != nil {
		t.Fatalf("invalid stored settings stopped startup: %v", err)
	}
	if _, _, ok := f.server.ProxyAuthSummary(); ok {
		t.Fatal("invalid stored settings were applied")
	}
}
