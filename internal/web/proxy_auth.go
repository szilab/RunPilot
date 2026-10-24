package web

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"strings"
)

// Environment variables that enable reverse-proxy header authentication.
// Both must be set; neither has a default.
const (
	TrustedProxiesEnv  = "RUNPILOT_TRUSTED_PROXIES"
	ProxyAuthHeaderEnv = "RUNPILOT_PROXY_AUTH_HEADER"
)

// ProxyAuth accepts a username asserted by an authenticating reverse proxy
// (single sign-on). The header is honored only on connections whose direct
// peer address is a trusted proxy; forwarding headers such as
// X-Forwarded-For are never consulted, because any client can send them.
type ProxyAuth struct {
	header  string
	trusted []netip.Prefix
}

// Header returns the canonical name of the username header.
func (p *ProxyAuth) Header() string { return p.header }

// TrustedProxies returns the trusted proxy ranges in configuration order.
func (p *ProxyAuth) TrustedProxies() []string {
	out := make([]string, len(p.trusted))
	for i, prefix := range p.trusted {
		out[i] = prefix.String()
	}
	return out
}

// Headers that carry other credentials or request routing must not double
// as the asserted identity.
var reservedProxyAuthHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"Cookie":              true,
	"Host":                true,
	"X-Runpilot-Csrf":     true,
}

// ParseProxyAuth validates the two environment values. Both empty disables
// proxy authentication (nil, nil); a partial or invalid configuration is an
// error so that a mistyped security setting cannot silently do nothing.
func ParseProxyAuth(trustedProxies, header string) (*ProxyAuth, error) {
	trustedProxies, header = strings.TrimSpace(trustedProxies), strings.TrimSpace(header)
	if trustedProxies == "" && header == "" {
		return nil, nil
	}
	if trustedProxies == "" || header == "" {
		return nil, fmt.Errorf("proxy authentication needs both %s and %s", TrustedProxiesEnv, ProxyAuthHeaderEnv)
	}
	if !validHeaderName(header) {
		return nil, fmt.Errorf("%s: %q is not a valid HTTP header name", ProxyAuthHeaderEnv, header)
	}
	canonical := textproto.CanonicalMIMEHeaderKey(header)
	if reservedProxyAuthHeaders[canonical] {
		return nil, fmt.Errorf("%s: %s cannot carry the proxy-authenticated user", ProxyAuthHeaderEnv, canonical)
	}
	auth := &ProxyAuth{header: canonical}
	for _, entry := range strings.FieldsFunc(trustedProxies, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		prefix, err := parseTrustedProxy(entry)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", TrustedProxiesEnv, err)
		}
		auth.trusted = append(auth.trusted, prefix)
	}
	if len(auth.trusted) == 0 {
		return nil, fmt.Errorf("%s lists no proxy addresses", TrustedProxiesEnv)
	}
	return auth, nil
}

func parseTrustedProxy(entry string) (netip.Prefix, error) {
	var prefix netip.Prefix
	if strings.Contains(entry, "/") {
		parsed, err := netip.ParsePrefix(entry)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid address range %q", entry)
		}
		prefix = parsed.Masked()
	} else {
		addr, err := netip.ParseAddr(entry)
		if err != nil || addr.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("invalid address %q", entry)
		}
		addr = addr.Unmap()
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	if prefix.Addr().Is4In6() {
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
	}
	if prefix.Bits() == 0 {
		return netip.Prefix{}, fmt.Errorf("%q would trust every address", entry)
	}
	return prefix, nil
}

func validHeaderName(name string) bool {
	for _, r := range name {
		if r > 0x7e || r <= ' ' || strings.ContainsRune(`"(),/:;<=>?@[\]{}`, r) {
			return false
		}
	}
	return name != ""
}

func (p *ProxyAuth) trustedPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.WithZone("").Unmap()
	for _, prefix := range p.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

var errAmbiguousProxyUser = errors.New("ambiguous proxy user header")

// asserted returns the username a trusted proxy sent. ok is false when the
// peer is not trusted or sent no (or an empty) username; such requests use
// the normal password-session and bearer-token authentication.
func (p *ProxyAuth) asserted(r *http.Request) (username string, ok bool, err error) {
	if !p.trustedPeer(r) {
		return "", false, nil
	}
	values := r.Header.Values(p.header)
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) > 1 {
		return "", true, errAmbiguousProxyUser
	}
	username = strings.TrimSpace(values[0])
	if username == "" {
		return "", false, nil
	}
	return username, true, nil
}

// SetProxyAuth enables reverse-proxy header authentication; nil disables it.
func (s *Server) SetProxyAuth(auth *ProxyAuth) { s.proxyAuth.Store(auth) }

type proxyAuthSettings struct {
	trustedProxies string
	header         string
}

// ConfigureProxyAuth selects the proxy authentication settings at startup.
// Environment values win when either is set; an invalid environment
// configuration is an error so the service does not start with a mistyped
// security setting. Otherwise the Settings values from runpilot.yaml apply;
// if they are invalid (edited by hand) proxy sign-in stays off and password
// sign-in keeps working.
func (s *Server) ConfigureProxyAuth(envTrustedProxies, envHeader string) error {
	if strings.TrimSpace(envTrustedProxies) != "" || strings.TrimSpace(envHeader) != "" {
		auth, err := ParseProxyAuth(envTrustedProxies, envHeader)
		if err != nil {
			return err
		}
		s.proxyAuthEnv = &proxyAuthSettings{trustedProxies: strings.TrimSpace(envTrustedProxies), header: strings.TrimSpace(envHeader)}
		s.SetProxyAuth(auth)
		return nil
	}
	cfg := s.ctrl.Snapshot().Server
	auth, err := ParseProxyAuth(cfg.ProxyAuthTrustedProxies, cfg.ProxyAuthHeader)
	if err != nil {
		log.Printf("proxy sign-in settings in %s are invalid and stay disabled: %v", s.ctrl.ConfigPath(), err)
		auth = nil
	}
	s.SetProxyAuth(auth)
	return nil
}

func (s *Server) proxyAuthState() map[string]any {
	settings, source := proxyAuthSettings{}, "settings"
	if s.proxyAuthEnv != nil {
		settings, source = *s.proxyAuthEnv, "environment"
	} else {
		cfg := s.ctrl.Snapshot().Server
		settings = proxyAuthSettings{trustedProxies: cfg.ProxyAuthTrustedProxies, header: cfg.ProxyAuthHeader}
	}
	state := map[string]any{"source": source, "trustedProxies": settings.trustedProxies, "header": settings.header, "enabled": false}
	if auth := s.proxyAuth.Load(); auth != nil {
		state["enabled"] = true
		state["header"] = auth.Header()
		state["trustedProxyRanges"] = auth.TrustedProxies()
	}
	return state
}

func (s *Server) handleProxyAuthChange(w http.ResponseWriter, r *http.Request) {
	if !s.csrfValid(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf validation failed"})
		return
	}
	if s.proxyAuthEnv != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("proxy sign-in is set by %s and %s; change those environment variables instead", TrustedProxiesEnv, ProxyAuthHeaderEnv)})
		return
	}
	var input struct {
		TrustedProxies string `json:"trustedProxies"`
		Header         string `json:"header"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	auth, err := ParseProxyAuth(input.TrustedProxies, input.Header)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": settingsProxyAuthError(err)})
		return
	}
	// Refuse a configuration under which this very request would be denied:
	// saving it from behind the proxy would lock the administrator out.
	if auth != nil {
		if username, asserted, assertErr := auth.asserted(r); asserted {
			admin := s.ctrl.Snapshot().Server.AdminUsername
			if assertErr != nil || username != admin {
				writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("not saved: this browser reaches RunPilot through a trusted proxy that reports a different user than %s, so the settings would lock you out", admin)})
				return
			}
		}
	}
	if err := s.ctrl.SetProxyAuth(input.TrustedProxies, input.Header); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save proxy sign-in settings"})
		return
	}
	s.SetProxyAuth(auth)
	writeJSON(w, http.StatusOK, s.proxyAuthState())
}

// settingsProxyAuthError words validation errors for the Settings form,
// which edits fields rather than environment variables.
func settingsProxyAuthError(err error) string {
	return strings.NewReplacer(TrustedProxiesEnv, "Trusted proxies", ProxyAuthHeaderEnv, "User name header").Replace(err.Error())
}

// proxyAuthenticate turns a trusted proxy's username assertion into an
// ordinary server session, so CSRF checks, WebSocket tickets and revocation
// behave exactly as after a password login. A trusted proxy asserting any
// other user is authoritative: management API requests are refused even with
// an existing session or bearer token.
func (s *Server) proxyAuthenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := s.proxyAuth.Load()
		if auth == nil || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		username, asserted, err := auth.asserted(r)
		if !asserted {
			next.ServeHTTP(w, r)
			return
		}
		admin := s.ctrl.Snapshot().Server.AdminUsername
		if admin == "" {
			next.ServeHTTP(w, r) // setup is not complete; the setup gate decides.
			return
		}
		if err != nil || len(username) != len(admin) || subtle.ConstantTimeCompare([]byte(username), []byte(admin)) != 1 {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v1/setup" {
				next.ServeHTTP(w, r)
				return
			}
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "the user signed in at the proxy is not the RunPilot administrator"})
			return
		}
		if _, _, ok := s.session(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		sessionKey, cookie, csrf := s.createSession(w, r, true)
		if sessionKey == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "could not create session"})
			return
		}
		// Let this request continue with the session it just received.
		authenticated := r.Clone(r.Context())
		authenticated.Header.Del("Cookie")
		for _, existing := range r.Cookies() {
			if existing.Name != adminSessionCookie && existing.Name != csrfCookie {
				authenticated.AddCookie(existing)
			}
		}
		authenticated.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: cookie})
		authenticated.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
		next.ServeHTTP(w, authenticated)
	})
}

// ProxyAuthSummary reports the active proxy sign-in header and trusted ranges.
func (s *Server) ProxyAuthSummary() (string, []string, bool) {
	auth := s.proxyAuth.Load()
	if auth == nil {
		return "", nil, false
	}
	return auth.Header(), auth.TrustedProxies(), true
}
