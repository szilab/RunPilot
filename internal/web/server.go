package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/dockercompose"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/terminal"
	"github.com/szilab/RunPilot/internal/version"
	"github.com/szilab/RunPilot/internal/websocketsecure"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	ctrl                  *core.Controller
	basePath              string
	restartHandler        func()
	setupWindowMu         sync.Mutex
	setupWindowDeadline   time.Time
	tickets               map[string]downloadTicket
	ticketMu              sync.Mutex
	dockerTerminal        *terminal.Manager
	dockerAttachTickets   map[string]dockerAttachTicket
	dockerAttachMu        sync.Mutex
	remoteTickets         map[string]remoteClientTicket
	remoteTicketMu        sync.Mutex
	legacyRoutes          bool // mounts unmigrated feature APIs; only tests set it
	proxyAuth             atomic.Pointer[ProxyAuth]
	proxyAuthEnv          *proxyAuthSettings // set when the environment owns the settings
	applicationTickets    map[string]applicationTicket
	applicationTicketMu   sync.Mutex
	docker                *dockercompose.Manager
	sessionMu             sync.Mutex
	sessions              map[string]authSession
	sessionConnections    map[string]map[uint64]func()
	nextSessionConnection uint64
	browserTicketMu       sync.Mutex
	browserTickets        map[string]sessionTicket
	loginMu               sync.Mutex
	loginFailures         map[string]loginFailure
	loginVerify           chan struct{}
	dataRootSwitchAllowed bool
}
type dockerAttachTicket struct {
	DockerExecID string
	SessionID    string
	Cols, Rows   uint16
	Expires      time.Time
}
type downloadTicket struct {
	StorageID, Path string
	SessionID       string
	Expires         time.Time
}
type remoteClientTicket struct {
	SessionID     string
	AuthSessionID string
	ClientParams  map[string]string
	Expires       time.Time
}
type applicationTicket struct {
	expires   time.Time
	sessionID string
}
type sessionTicket struct {
	sessionID string
	expires   time.Time
}

func New(ctrl *core.Controller, basePaths ...string) (*Server, error) {
	basePath := "/"
	if len(basePaths) > 0 {
		basePath = basePaths[0]
	}
	basePath, err := normalizeBasePath(basePath)
	if err != nil {
		return nil, err
	}
	if err := ctrl.SetBrowserBasePath(basePath); err != nil {
		return nil, err
	}
	server := &Server{ctrl: ctrl, basePath: basePath, tickets: map[string]downloadTicket{}, dockerTerminal: terminal.NewManager(ctrl.DataDir(), terminal.DefaultMaxSessions), dockerAttachTickets: map[string]dockerAttachTicket{}, remoteTickets: map[string]remoteClientTicket{}, applicationTickets: map[string]applicationTicket{}, docker: ctrl.Docker(), sessions: map[string]authSession{}, sessionConnections: map[string]map[uint64]func(){}, browserTickets: map[string]sessionTicket{}, loginFailures: map[string]loginFailure{}, loginVerify: make(chan struct{}, 2), dataRootSwitchAllowed: true}
	if err := server.loadSessions(); err != nil {
		return nil, err
	}
	// Settings-backed proxy sign-in; the daemon re-applies it with any
	// environment override.
	_ = server.ConfigureProxyAuth("", "")
	return server, nil
}

func (s *Server) BasePath() string { return s.basePath }

// SetRestartHandler registers the daemon callback used by restart requests and
// the incomplete first-run setup window.
func (s *Server) SetRestartHandler(handler func()) {
	s.restartHandler = handler
	if handler != nil && s.setupRequired() {
		s.startSetupWindow(setupWindowDuration)
	}
}

const setupWindowDuration = 5 * time.Minute

func (s *Server) setupRequired() bool {
	server := s.ctrl.Snapshot().Server
	return server.AdminUsername == "" || server.AdminPasswordHash == ""
}

func (s *Server) startSetupWindow(duration time.Duration) {
	s.setupWindowMu.Lock()
	s.setupWindowDeadline = time.Now().Add(duration)
	s.setupWindowMu.Unlock()
}

func (s *Server) setupWindowOpen() bool {
	s.setupWindowMu.Lock()
	deadline := s.setupWindowDeadline
	s.setupWindowMu.Unlock()
	return deadline.IsZero() || time.Now().Before(deadline)
}

func (s *Server) SetDataRootSwitchAllowed(allowed bool) { s.dataRootSwitchAllowed = allowed }

func (s *Server) handleRestartApplication(w http.ResponseWriter, r *http.Request) {
	if s.restartHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "application restart is unavailable"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go s.restartHandler()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/system", s.handleSystem)
	mux.HandleFunc("GET /api/v1/setup", s.handleSetupStatus)
	mux.HandleFunc("POST /api/v1/setup", s.handleInitialSetup)
	mux.HandleFunc("GET /setup", s.handleSetupPage)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /api/v1/auth/password", s.handlePasswordChange)
	api.HandleFunc("GET /api/v1/dashboard/status", s.handleDashboardStatus)
	api.HandleFunc("GET /api/v1/launcher", s.handleLauncherGet)
	api.HandleFunc("PUT /api/v1/launcher", s.handleLauncherPut)
	api.HandleFunc("POST /api/v1/ws/ticket", s.handleApplicationTicket)
	api.HandleFunc("GET /api/v1/plugins", s.handlePlugins)
	api.HandleFunc("POST /api/v1/restart", s.handleRestartApplication)
	api.HandleFunc("GET /api/v1/admin/data-root", s.handleDataRootGet)
	api.HandleFunc("POST /api/v1/admin/data-root", s.handleDataRootChange)
	api.HandleFunc("PUT /api/v1/admin/bearer-auth", s.handleBearerAuthChange)
	api.HandleFunc("PUT /api/v1/admin/proxy-auth", s.handleProxyAuthChange)
	api.HandleFunc("PUT /api/v1/admin/websocket-payload-mode", s.handleWebSocketPayloadModeChange)
	api.HandleFunc("GET /api/v1/plugins/catalog", s.handlePluginCatalog)
	api.HandleFunc("POST /api/v1/plugins/{id}/install", s.handlePluginInstall)
	api.HandleFunc("DELETE /api/v1/plugins/{id}", s.handlePluginUninstall)
	api.HandleFunc("GET /api/v1/plugins/runtime", s.handlePluginRuntime)
	api.HandleFunc("PUT /api/v1/plugins/{id}", s.handlePluginUpdate)
	api.HandleFunc("POST /api/v1/plugins/rescan", s.handlePluginRescan)
	api.HandleFunc("GET /api/v1/overview", s.handleOverview)
	if s.legacyRoutes {
		s.registerLegacyRoutes(mux, api)
	}

	mux.Handle("/api/", s.auth(api))
	mux.HandleFunc("GET /api/v1/ws", s.handleApplicationWS)
	mux.HandleFunc("GET /plugins/{id}/{path...}", s.handlePluginAsset)
	mux.HandleFunc("GET /__runpilot__/browser/{runtime}/{asset}", s.serveBrowserRuntime)

	sub, _ := fs.Sub(staticFS, "static")
	static := http.FileServer(http.FS(sub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.serveBrowserPublication(w, r) {
			return
		}
		static.ServeHTTP(w, r)
	}))
	root := s.proxyAuthenticate(mux)
	if s.basePath == "/" {
		return logging(s.setupGate(root))
	}
	return logging(s.setupGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == s.basePath {
			http.Redirect(w, r, s.basePath+"/", http.StatusPermanentRedirect)
			return
		}
		prefix := s.basePath + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = strings.TrimPrefix(r.URL.Path, s.basePath)
		if r.URL.RawPath != "" {
			clone.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, s.basePath)
		}
		root.ServeHTTP(w, clone)
	})))
}

// Close releases all interactive shell sessions during RunPilot shutdown.
func (s *Server) Close() error { return s.dockerTerminal.Close() }

func normalizeBasePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return "/", nil
	}
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "?#") {
		return "", fmt.Errorf("server base path must start with / and contain no query or fragment")
	}
	clean := path.Clean(value)
	if clean == "." || clean == "/" || strings.HasPrefix(clean, "/..") {
		return "", fmt.Errorf("invalid server base path %q", value)
	}
	return clean, nil
}

const initialSetupHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Set up RunPilot</title><script>document.documentElement.dataset.scheme="system";document.documentElement.dataset.theme=matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light"</script><link rel="stylesheet" href="__BASE__/styles.css"><link rel="stylesheet" href="__BASE__/workspace.css"></head><body><main class="setup-screen"><section class="dialog small setup-card"><div class="dialog-head"><div><h1>Set up RunPilot</h1><p>Create the local administrator account and choose where RunPilot stores its data.</p></div></div><p>Complete setup within 5 minutes of starting RunPilot. When the window expires, setup is locked until you restart the RunPilot service.</p><form id="setup" class="settings-form setup-form"><label>Administrator username<input name="username" value="admin" minlength="3" maxlength="64" autocomplete="username" required></label><label>Password<input name="password" type="password" minlength="12" autocomplete="new-password" required></label><label>Confirm password<input name="confirm" type="password" minlength="12" autocomplete="new-password" required></label>__DATA_DIR_CONTROL__<div id="error" class="form-error hidden" role="alert"></div><div class="dialog-actions"><button class="button primary">Create administrator</button></div></form></section></main><script>try{localStorage.removeItem('runpilot.token');sessionStorage.removeItem('runpilot.token')}catch{}const form=document.querySelector('#setup'),error=document.querySelector('#error');form.onsubmit=async e=>{e.preventDefault();error.textContent='';error.classList.add('hidden');if(form.password.value!==form.confirm.value){error.textContent='Passwords do not match.';error.classList.remove('hidden');return}try{const response=await fetch('__BASE__/api/v1/setup',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'same-origin',body:JSON.stringify(Object.fromEntries(new FormData(form)))});if(!response.ok){const result=await response.json();throw Error(result.error||'Setup failed')}location.href='__BASE__/'}catch(x){error.textContent=x.message;error.classList.remove('hidden')}}</script></body></html>`

func (s *Server) writeInitialSetup(w http.ResponseWriter) {
	staticPrefix := s.basePath
	if staticPrefix == "/" {
		staticPrefix = ""
	}
	page := strings.ReplaceAll(initialSetupHTML, "__BASE__", staticPrefix)
	dataControl := `<label>RunPilot data directory<input name="dataDir" type="text" value="` + html.EscapeString(s.ctrl.DataDir()) + `" required></label>`
	if !s.dataRootSwitchAllowed {
		dataControl = `<p class="muted">RunPilot data directory: <code>` + html.EscapeString(s.ctrl.DataDir()) + `</code> (fixed by startup configuration)</p>`
	}
	page = strings.ReplaceAll(page, "__DATA_DIR_CONTROL__", dataControl)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, page)
}

func (s *Server) writeExpiredSetup(w http.ResponseWriter) {
	staticPrefix := s.basePath
	if staticPrefix == "/" {
		staticPrefix = ""
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Setup locked · RunPilot</title><script>document.documentElement.dataset.scheme="system";document.documentElement.dataset.theme=matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light"</script><link rel="stylesheet" href="`+html.EscapeString(staticPrefix)+`/styles.css"><link rel="stylesheet" href="`+html.EscapeString(staticPrefix)+`/workspace.css"></head><body><main class="setup-screen"><section class="dialog small setup-card"><h1>Setup window expired</h1><p>The five-minute setup window has ended. Restart the RunPilot service to open a new setup window.</p></section></main></body></html>`)
}

func (s *Server) writeSetupPage(w http.ResponseWriter) {
	if !s.setupWindowOpen() {
		s.writeExpiredSetup(w)
		return
	}
	s.writeInitialSetup(w)
}

func (s *Server) setupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localPath := r.URL.Path
		if s.basePath != "/" {
			if localPath == s.basePath || !strings.HasPrefix(localPath, s.basePath+"/") {
				next.ServeHTTP(w, r)
				return
			}
			localPath = strings.TrimPrefix(localPath, s.basePath)
		}
		serverConfig := s.ctrl.Snapshot().Server
		setupComplete := serverConfig.AdminUsername != "" && serverConfig.AdminPasswordHash != ""
		if setupComplete || localPath == "/healthz" || localPath == "/api/v1/setup" || localPath == "/api/v1/auth/login" || localPath == "/setup" || setupAppearanceAsset(localPath) {
			next.ServeHTTP(w, r)
			return
		}
		if localPath == "/" {
			s.writeSetupPage(w)
			return
		}
		writeJSON(w, http.StatusPreconditionRequired, map[string]string{"error": "initial setup required", "setup": strings.TrimSuffix(s.basePath, "/") + "/setup"})
	})
}

func setupAppearanceAsset(path string) bool {
	switch path {
	case "/styles.css", "/workspace.css", "/runpilot-logo.png", "/runpilot-mark.svg", "/workspace-alpine.webp", "/fonts/InterVariable.woff2":
		return true
	default:
		return false
	}
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctrl.Snapshot().Server
	writeJSON(w, http.StatusOK, map[string]any{"required": cfg.AdminUsername == "" || cfg.AdminPasswordHash == "", "fresh": !cfg.SetupComplete})
}

func (s *Server) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctrl.Snapshot().Server
	if cfg.AdminUsername != "" && cfg.AdminPasswordHash != "" {
		http.Redirect(w, r, s.basePath+"/", http.StatusSeeOther)
		return
	}
	s.writeSetupPage(w)
}

func (s *Server) handleInitialSetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.setupWindowOpen() {
		writeJSON(w, http.StatusGone, map[string]string{"error": "setup window expired; restart the RunPilot service to try again"})
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
		DataDir  string `json:"dataDir"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if input.Password != input.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password confirmation does not match"})
		return
	}
	if err := validCredentials(input.Username, input.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var err error
	selectedDir := s.ctrl.DataDir()
	if s.dataRootSwitchAllowed && strings.TrimSpace(input.DataDir) != "" {
		selectedDir, err = config.ValidateDataRoot(input.DataDir)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	var accountStore interface{ ConfigureAdmin(string, string) error }
	newRoot := false
	if filepath.Clean(selectedDir) == filepath.Clean(s.ctrl.DataDir()) {
		accountStore = s.ctrl
	} else if filepath.Clean(s.ctrl.ConfigPath()) != filepath.Clean(filepath.Join(s.ctrl.DataDir(), "runpilot.yaml")) {
		// With an explicitly separate config directory, the active Store remains
		// authoritative while the data directory is selected. Relocate only the
		// mutable data tree; configuration stays at its configured location.
		accountStore = s.ctrl
		selectedDir, err = config.ValidateNewDataRoot(selectedDir)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		newRoot = true
	} else if _, statErr := os.Stat(filepath.Join(selectedDir, "runpilot.yaml")); statErr == nil {
		accountStore, err = config.Open(selectedDir)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "existing RunPilot data directory cannot be opened"})
			return
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if _, rootErr := os.Lstat(selectedDir); rootErr == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an existing destination must contain a recognized RunPilot configuration"})
			return
		} else if !errors.Is(rootErr, os.ErrNotExist) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "selected data directory cannot be inspected"})
			return
		}
		selectedDir, err = config.ValidateNewDataRoot(selectedDir)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		newRoot = true
	} else {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "selected data directory cannot be inspected"})
		return
	}

	if newRoot {
		if s.hasComposeProjects(s.ctrl.DataDir()) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "the current root contains Docker Compose projects; choose the current directory or migrate those projects manually first"})
			return
		}
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not secure password"})
		return
	}
	if newRoot {
		// Claim the administrator account in the active store first. Concurrent
		// requests selecting different roots cannot each claim the installation.
		if err := s.ctrl.ConfigureAdmin(strings.TrimSpace(input.Username), passwordHash); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "administrator account is already configured"})
			return
		}
		if err = config.QueueDataRootMigration(s.ctrl.DataDir(), selectedDir); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "administrator account was created in the current directory, but selected data-root migration could not be queued: " + err.Error()})
			return
		}
	} else {
		if err := accountStore.ConfigureAdmin(strings.TrimSpace(input.Username), passwordHash); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "administrator account is already configured"})
			return
		}
	}
	if !newRoot {
		if err := config.WriteDataRoot(selectedDir); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "administrator created but data-root locator could not be saved"})
			return
		}
	}
	if s.issueSession(w, r) == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create session"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"complete": true})
	if filepath.Clean(selectedDir) != filepath.Clean(s.ctrl.DataDir()) && s.restartHandler != nil {
		go s.restartHandler()
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "login origin rejected"})
			return
		}
	}
	key := clientAddress(r)
	if !s.loginAllowed(key) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many login attempts"})
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	cfg := s.ctrl.Snapshot().Server
	select {
	case s.loginVerify <- struct{}{}:
		defer func() { <-s.loginVerify }()
	default:
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "login service is busy"})
		return
	}
	passwordOK := verifyPassword(cfg.AdminPasswordHash, input.Password)
	usernameOK := len(input.Username) == len(cfg.AdminUsername) && subtle.ConstantTimeCompare([]byte(input.Username), []byte(cfg.AdminUsername)) == 1
	if !usernameOK || !passwordOK {
		s.recordLoginFailure(key)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
		return
	}
	s.clearLoginFailures(key)
	if s.issueSession(w, r) == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create session"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.csrfValid(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf validation failed"})
		return
	}
	if err := s.clearSession(w, r); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session could not be revoked durably"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) revokeAllSessions() error {
	s.sessionMu.Lock()
	connections := s.sessionConnections
	s.sessions = map[string]authSession{}
	s.sessionConnections = map[string]map[uint64]func(){}
	err := s.revokePersistedSessionsLocked()
	s.sessionMu.Unlock()
	for _, set := range connections {
		for _, closeConnection := range set {
			closeConnection()
		}
	}
	return err
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.csrfValid(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf validation failed"})
		return
	}
	var input struct {
		Current string `json:"current"`
		Next    string `json:"next"`
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if input.Next != input.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password confirmation does not match"})
		return
	}
	if err := validCredentials(s.ctrl.Snapshot().Server.AdminUsername, input.Next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg := s.ctrl.Snapshot().Server
	if !verifyPassword(cfg.AdminPasswordHash, input.Current) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password rejected"})
		return
	}
	hash, err := hashPassword(input.Next)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not secure password"})
		return
	}
	if err = s.ctrl.ChangeAdminPassword(cfg.AdminUsername, cfg.AdminPasswordHash, hash); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "account changed during request"})
		return
	}
	if err := s.revokeAllSessions(); err != nil {
		_ = s.clearSession(w, r)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "password changed but sessions could not be revoked durably"})
		return
	}
	if err := s.clearSession(w, r); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "password changed but session store could not be updated"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) handleBearerAuthChange(w http.ResponseWriter, r *http.Request) {
	if !s.csrfValid(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf validation failed"})
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := s.ctrl.SetBearerAuth(input.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save API token setting"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": input.Enabled})
}

func (s *Server) handleWebSocketPayloadModeChange(w http.ResponseWriter, r *http.Request) {
	if !s.csrfValid(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf validation failed"})
		return
	}
	var input struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil || (input.Mode != "disabled" && input.Mode != "optional" && input.Mode != "required") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be disabled, optional, or required"})
		return
	}
	if err := s.ctrl.SetWebSocketPayloadMode(input.Mode); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save WebSocket payload encryption setting"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"mode": input.Mode})
}

func (s *Server) handleDataRootGet(w http.ResponseWriter, r *http.Request) {
	status := ""
	if path := config.LocatorPath(); path != "" {
		if value, err := os.ReadFile(path + ".status"); err == nil {
			status = strings.TrimSpace(string(value))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dataDir": s.ctrl.DataDir(), "migrationStatus": status, "changeAllowed": s.dataRootSwitchAllowed && s.restartHandler != nil})
}

func (s *Server) handleDataRootChange(w http.ResponseWriter, r *http.Request) {
	if !s.dataRootSwitchAllowed {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this RunPilot service uses an explicit --data-dir or RUNPILOT_DATA_DIR override; change its deployment setting"})
		return
	}
	if s.restartHandler == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "graceful restart is unavailable"})
		return
	}
	var input struct {
		Destination string `json:"destination"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if _, err := config.ValidateDataRoot(input.Destination); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := config.QueueDataRootSwitch(s.ctrl.DataDir(), input.Destination); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, `{"status":"restarting"}`)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go s.restartHandler()
}

func (s *Server) hasComposeProjects(root string) bool {
	for _, path := range []string{filepath.Join(root, "compose"), filepath.Join(root, "plugins", "docker", "data", "workspace", "projects")} {
		entries, err := os.ReadDir(path)
		if err == nil && len(entries) > 0 {
			return true
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := s.session(r); ok {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				if !s.csrfValid(r) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf validation failed"})
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		cfg := s.ctrl.Snapshot().Server
		token := cfg.Token
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if !cfg.BearerAuthEnabled || token == "" || auth != "Bearer "+token {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"plugins":         s.ctrl.Plugins().Statuses(),
		"discoveryErrors": s.ctrl.Plugins().DiscoveryErrors(),
		"restartRequired": s.ctrl.Plugins().RestartRequired(),
	})
}

func (s *Server) handlePluginRuntime(w http.ResponseWriter, r *http.Request) {
	extensions := s.ctrl.Plugins().FrontendExtensions()
	if s.basePath != "/" {
		for i := range extensions {
			extensions[i].Module = s.basePath + extensions[i].Module
			if extensions[i].Stylesheet != "" {
				extensions[i].Stylesheet = s.basePath + extensions[i].Stylesheet
			}
		}
	}
	writeJSON(w, http.StatusOK, extensions)
}

func (s *Server) handlePluginAsset(w http.ResponseWriter, r *http.Request) {
	id, relative := r.PathValue("id"), r.PathValue("path")
	path, err := s.ctrl.Plugins().AssetPath(id, relative)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "plugin asset not found"})
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (s *Server) handlePluginUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, errors.New("enabled is required"))
		return
	}
	if err := s.ctrl.SetPluginEnabled(r.PathValue("id"), *body.Enabled); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plugins":         s.ctrl.Plugins().Statuses(),
		"restartRequired": s.ctrl.Plugins().RestartRequired(),
	})
}

func (s *Server) handlePluginRescan(w http.ResponseWriter, r *http.Request) {
	errs := s.ctrl.RescanPlugins()
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		messages = append(messages, err.Error())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plugins": s.ctrl.Plugins().Statuses(),
		"errors":  messages,
	})
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctrl.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"proxyAuth":                      s.proxyAuthState(),
		"name":                           "RunPilot",
		"version":                        version.Version,
		"dataDir":                        s.ctrl.DataDir(),
		"bearerAuthEnabled":              cfg.Server.BearerAuthEnabled,
		"username":                       cfg.Server.AdminUsername,
		"configPath":                     s.ctrl.ConfigPath(),
		"bind":                           cfg.Server.Bind,
		"capabilities":                   platform.CurrentCapabilities(),
		"websocketPayloadMode":           requestWebSocketPayloadMode(r, cfg.Server.WebSocketPayloadMode),
		"websocketPayloadModeConfigured": cfg.Server.WebSocketPayloadMode,
	})
}

// requestWebSocketPayloadMode disables the additional payload cipher for an
// HTTP browser session. HTTPS clients retain the configured transport mode.
func requestWebSocketPayloadMode(r *http.Request, configured string) string {
	if requestScheme(r) == "http" {
		return websocketsecure.ModeDisabled
	}
	return websocketsecure.NormalizeMode(configured)
}

func requestScheme(r *http.Request) string {
	if origin, err := url.Parse(r.Header.Get("Origin")); err == nil && (origin.Scheme == "http" || origin.Scheme == "https") {
		return origin.Scheme
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		return forwarded
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func secureTicket() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.Overview())
}

func queryLimit(r *http.Request, fallback int) int {
	value := r.URL.Query().Get("limit")
	if value == "" {
		value = r.URL.Query().Get("lines")
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 || n > 5000 {
		return fallback
	}
	return n
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		_ = start // hook for structured logging in the next milestone
	})
}

var _ = errors.Is
