package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/szilab/RunPilot/internal/browserpath"
	"golang.org/x/crypto/argon2"
)

const (
	adminSessionCookie = "runpilot_session"
	csrfCookie         = "runpilot_csrf"
	adminSessionTTL    = 24 * time.Hour
	argonTime          = 3
	argonMemory        = 64 * 1024
	argonThreads       = 2
	argonKeyLength     = 32
)

type authSession struct {
	csrfHash string
	expires  time.Time
	// proxy marks sessions created from a trusted proxy's user header. The
	// proxy recreates them on demand, so they are evicted first when the
	// session table is full.
	proxy bool
}

func hashPassword(password string) (string, error) {
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt[:], argonTime, argonMemory, argonThreads, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt[:]), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 || !strings.HasPrefix(parameters[0], "m=") || !strings.HasPrefix(parameters[1], "t=") || !strings.HasPrefix(parameters[2], "p=") {
		return false
	}
	memory, memoryErr := strconv.ParseUint(strings.TrimPrefix(parameters[0], "m="), 10, 32)
	iterations, iterationsErr := strconv.ParseUint(strings.TrimPrefix(parameters[1], "t="), 10, 32)
	threads, threadsErr := strconv.ParseUint(strings.TrimPrefix(parameters[2], "p="), 10, 8)
	if memoryErr != nil || iterationsErr != nil || threadsErr != nil ||
		memory < 8*1024 || memory > 256*1024 || iterations == 0 || iterations > 10 || threads == 0 || threads > 8 {
		return false
	}
	saltText, hashText := parts[4], parts[5]
	salt, err := base64.RawStdEncoding.DecodeString(saltText)
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(hashText)
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, uint32(iterations), uint32(memory), uint8(threads), uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func validCredentials(username, password string) error {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 64 || strings.ContainsAny(username, "\r\n\x00") {
		return errors.New("username must be 3–64 characters")
	}
	if len(password) < 12 || len(password) > 1024 {
		return errors.New("password must be 12–1024 characters")
	}
	return nil
}

func randomSecret(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func authSessionKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request) string {
	sessionKey, _, _ := s.createSession(w, r, false)
	return sessionKey
}

// createSession stores a new browser session and sets its cookies. It
// returns the session key and the raw cookie and CSRF values, or empty
// strings when no session could be created.
func (s *Server) createSession(w http.ResponseWriter, r *http.Request, proxy bool) (string, string, string) {
	id, err := randomSecret(32)
	if err != nil {
		return "", "", ""
	}
	csrf, err := randomSecret(24)
	if err != nil {
		return "", "", ""
	}
	sessionKey := authSessionKey(id)
	s.sessionMu.Lock()
	now := time.Now()
	var expiredConnections []func()
	expired := false
	for key, value := range s.sessions {
		if now.After(value.expires) {
			delete(s.sessions, key)
			expired = true
			for _, closeConnection := range s.sessionConnections[key] {
				expiredConnections = append(expiredConnections, closeConnection)
			}
			delete(s.sessionConnections, key)
		}
	}
	var evicted string
	if len(s.sessions) >= maxPersistedAuthSessions {
		evicted = s.oldestProxySessionLocked()
		if evicted != "" {
			delete(s.sessions, evicted)
			for _, closeConnection := range s.sessionConnections[evicted] {
				expiredConnections = append(expiredConnections, closeConnection)
			}
			delete(s.sessionConnections, evicted)
		}
	}
	if len(s.sessions) >= maxPersistedAuthSessions {
		if expired {
			_ = s.saveSessionsLocked()
		}
		s.sessionMu.Unlock()
		for _, closeConnection := range expiredConnections {
			closeConnection()
		}
		return "", "", ""
	}
	s.sessions[sessionKey] = authSession{csrfHash: authSessionKey(csrf), expires: now.Add(adminSessionTTL), proxy: proxy}
	if err := s.saveSessionsLocked(); err != nil {
		delete(s.sessions, sessionKey)
		s.sessionMu.Unlock()
		for _, closeConnection := range expiredConnections {
			closeConnection()
		}
		return "", "", ""
	}
	s.sessionMu.Unlock()
	for _, closeConnection := range expiredConnections {
		closeConnection()
	}
	if evicted != "" {
		s.revokeSessionTickets(evicted)
	}
	secure := requestScheme(r) == "https"
	path := browserpath.Escape(s.basePath)
	if path == "/" {
		path = "/"
	}
	w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Value: id, Path: path, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: now.Add(adminSessionTTL), MaxAge: int(adminSessionTTL.Seconds())})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: csrf, Path: path, HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: now.Add(adminSessionTTL), MaxAge: int(adminSessionTTL.Seconds())})
	return sessionKey, id, csrf
}

// oldestProxySessionLocked returns the proxy-created session that expires
// first, or "" when there is none. sessionMu must be held.
func (s *Server) oldestProxySessionLocked() string {
	oldest := ""
	var oldestExpiry time.Time
	for key, session := range s.sessions {
		if session.proxy && (oldest == "" || session.expires.Before(oldestExpiry)) {
			oldest, oldestExpiry = key, session.expires
		}
	}
	return oldest
}

func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) error {
	var persistErr error
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		sessionID := authSessionKey(cookie.Value)
		s.sessionMu.Lock()
		delete(s.sessions, sessionID)
		connections := s.sessionConnections[sessionID]
		delete(s.sessionConnections, sessionID)
		persistErr = s.revokePersistedSessionsLocked()
		s.sessionMu.Unlock()
		for _, closeConnection := range connections {
			closeConnection()
		}
		s.revokeSessionTickets(sessionID)
	}
	for _, name := range []string{adminSessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: browserpath.Escape(s.basePath), HttpOnly: name == adminSessionCookie, Secure: requestScheme(r) == "https", SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
	return persistErr
}

func (s *Server) revokeSessionTickets(sessionID string) {
	// Keep short-lived records until expiry so revoked tickets cannot become anonymous.
	s.applicationTicketMu.Lock()
	for id, ticket := range s.applicationTickets {
		if ticket.sessionID == sessionID {
			delete(s.applicationTickets, id)
		}
	}
	s.applicationTicketMu.Unlock()
	s.ticketMu.Lock()
	for id, ticket := range s.tickets {
		if ticket.SessionID == sessionID {
			delete(s.tickets, id)
		}
	}
	s.ticketMu.Unlock()
	s.dockerAttachMu.Lock()
	for id, ticket := range s.dockerAttachTickets {
		if ticket.SessionID == sessionID {
			delete(s.dockerAttachTickets, id)
		}
	}
	s.dockerAttachMu.Unlock()
	s.remoteTicketMu.Lock()
	for id, ticket := range s.remoteTickets {
		if ticket.AuthSessionID == sessionID {
			delete(s.remoteTickets, id)
		}
	}
	s.remoteTicketMu.Unlock()
}

func (s *Server) rememberSessionTickets(sessionID string, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return
	}
	now := time.Now()
	s.browserTicketMu.Lock()
	defer s.browserTicketMu.Unlock()
	for key, item := range s.browserTickets {
		if now.After(item.expires) {
			delete(s.browserTickets, key)
		}
	}
	var visit func(any)
	visit = func(v any) {
		switch item := v.(type) {
		case map[string]any:
			for key, value := range item {
				if key == "ticket" {
					if ticket, ok := value.(string); ok && ticket != "" {
						s.browserTickets[ticket] = sessionTicket{sessionID: sessionID, expires: now.Add(time.Minute)}
					}
				}
				visit(value)
			}
		case []any:
			for _, value := range item {
				visit(value)
			}
		}
	}
	visit(decoded)
}

func (s *Server) browserTicketAllowed(ticket, sessionID string, hasSession bool) bool {
	s.browserTicketMu.Lock()
	defer s.browserTicketMu.Unlock()
	item, ok := s.browserTickets[ticket]
	if !ok {
		return true
	}
	if time.Now().After(item.expires) {
		delete(s.browserTickets, ticket)
		return false
	}
	return hasSession && item.sessionID == sessionID
}

func (s *Server) registerSessionConnection(sessionID string, closeConnection func()) func() {
	if sessionID == "" {
		return func() {}
	}
	s.sessionMu.Lock()
	id := s.nextSessionConnection
	s.nextSessionConnection++
	if s.sessionConnections[sessionID] == nil {
		s.sessionConnections[sessionID] = map[uint64]func(){}
	}
	s.sessionConnections[sessionID][id] = closeConnection
	s.sessionMu.Unlock()
	return func() {
		s.sessionMu.Lock()
		delete(s.sessionConnections[sessionID], id)
		if len(s.sessionConnections[sessionID]) == 0 {
			delete(s.sessionConnections, sessionID)
		}
		s.sessionMu.Unlock()
	}
}

func (s *Server) csrfValid(r *http.Request) bool {
	_, session, ok := s.session(r)
	if !ok {
		return false
	}
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := r.Header.Get("X-RunPilot-CSRF")
	csrfHash := authSessionKey(cookie.Value)
	headerHash := authSessionKey(header)
	return subtle.ConstantTimeCompare([]byte(csrfHash), []byte(session.csrfHash)) == 1 && subtle.ConstantTimeCompare([]byte(headerHash), []byte(session.csrfHash)) == 1
}

func (s *Server) session(r *http.Request) (string, authSession, bool) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return "", authSession{}, false
	}
	s.sessionMu.Lock()
	sessionID := authSessionKey(cookie.Value)
	value, ok := s.sessions[sessionID]
	if !ok {
		s.sessionMu.Unlock()
		return "", authSession{}, false
	}
	if time.Now().After(value.expires) {
		delete(s.sessions, sessionID)
		connections := s.sessionConnections[sessionID]
		delete(s.sessionConnections, sessionID)
		_ = s.saveSessionsLocked()
		s.sessionMu.Unlock()
		for _, closeConnection := range connections {
			closeConnection()
		}
		s.revokeSessionTickets(sessionID)
		return "", authSession{}, false
	}
	s.sessionMu.Unlock()
	return sessionID, value, true
}

func (s *Server) loginAllowed(key string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := time.Now()
	entry := s.loginFailures[key]
	if now.Sub(entry.start) > 15*time.Minute {
		entry = loginFailure{start: now}
	}
	return entry.count < 10
}

func (s *Server) recordLoginFailure(key string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := time.Now()
	if len(s.loginFailures) > 4096 {
		for client, failure := range s.loginFailures {
			if now.Sub(failure.start) > 15*time.Minute {
				delete(s.loginFailures, client)
			}
		}
	}
	if len(s.loginFailures) >= 4096 {
		if _, exists := s.loginFailures[key]; !exists {
			return
		}
	}
	entry := s.loginFailures[key]
	if now.Sub(entry.start) > 15*time.Minute {
		entry = loginFailure{start: now}
	}
	entry.count++
	s.loginFailures[key] = entry
}

func (s *Server) clearLoginFailures(key string) {
	s.loginMu.Lock()
	delete(s.loginFailures, key)
	s.loginMu.Unlock()
}

type loginFailure struct {
	start time.Time
	count int
}
