package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/dockercompose"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/software"
	"github.com/szilab/RunPilot/internal/storage"
	"github.com/szilab/RunPilot/internal/terminal"
	"github.com/szilab/RunPilot/internal/version"
	"github.com/szilab/RunPilot/internal/websocketsecure"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	ctrl              *core.Controller
	basePath          string
	tickets           map[string]downloadTicket
	ticketMu          sync.Mutex
	terminal          *terminal.Manager
	terminalTickets   map[string]terminalTicket
	terminalTicketMu  sync.Mutex
	remoteTickets     map[string]remoteClientTicket
	remoteTicketMu    sync.Mutex
	transportTickets  map[string]remoteTransportTicket
	transportTicketMu sync.Mutex
	rdpCredentials    map[string]rdpCredentials
	rdpCredentialMu   sync.Mutex
	docker            *dockercompose.Manager
}
type terminalTicket struct {
	Shell        string
	DockerExecID string
	Cols, Rows   uint16
	Expires      time.Time
}
type downloadTicket struct {
	StorageID, Path string
	Expires         time.Time
}
type remoteClientTicket struct {
	SessionID    string
	ClientParams map[string]string
	Expires      time.Time
}
type remoteTransportTicket struct {
	SessionID string
	Expires   time.Time
}
type rdpCredentials struct{ Username, Domain, Password string }

func New(ctrl *core.Controller, basePaths ...string) (*Server, error) {
	basePath := "/"
	if len(basePaths) > 0 {
		basePath = basePaths[0]
	}
	basePath, err := normalizeBasePath(basePath)
	if err != nil {
		return nil, err
	}
	return &Server{ctrl: ctrl, basePath: basePath, tickets: map[string]downloadTicket{}, terminal: terminal.NewManager(ctrl.DataDir(), terminal.DefaultMaxSessions), terminalTickets: map[string]terminalTicket{}, remoteTickets: map[string]remoteClientTicket{}, transportTickets: map[string]remoteTransportTicket{}, rdpCredentials: map[string]rdpCredentials{}, docker: ctrl.Docker()}, nil
}

func (s *Server) BasePath() string { return s.basePath }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/system", s.handleSystem)
	api.HandleFunc("GET /api/v1/plugins", s.handlePlugins)
	api.HandleFunc("GET /api/v1/plugins/runtime", s.handlePluginRuntime)
	api.HandleFunc("PUT /api/v1/plugins/{id}", s.handlePluginUpdate)
	api.HandleFunc("POST /api/v1/plugins/rescan", s.handlePluginRescan)
	api.HandleFunc("GET /api/v1/docker", s.handleDockerRuntime)
	api.HandleFunc("GET /api/v1/docker/projects", s.handleDockerProjects)
	api.HandleFunc("GET /api/v1/docker/volumes", s.handleDockerVolumes)
	api.HandleFunc("POST /api/v1/docker/volumes", s.handleDockerCreateVolume)
	api.HandleFunc("DELETE /api/v1/docker/volumes/{name}", s.handleDockerDeleteVolume)
	api.HandleFunc("GET /api/v1/docker/networks", s.handleDockerNetworks)
	api.HandleFunc("POST /api/v1/docker/networks", s.handleDockerCreateNetwork)
	api.HandleFunc("DELETE /api/v1/docker/networks/{name}", s.handleDockerDeleteNetwork)
	api.HandleFunc("POST /api/v1/docker/containers/{id}/actions/{action}", s.handleDockerContainerAction)
	api.HandleFunc("GET /api/v1/docker/containers/{id}/logs", s.handleDockerContainerLogs)
	api.HandleFunc("POST /api/v1/docker/containers/{id}/attach-ticket", s.handleDockerContainerAttachTicket)
	api.HandleFunc("POST /api/v1/docker/projects", s.handleDockerCreateProject)
	api.HandleFunc("DELETE /api/v1/docker/projects/{name}", s.handleDockerDeleteProject)
	api.HandleFunc("POST /api/v1/docker/projects/{name}/actions/{action}", s.handleDockerAction)
	api.HandleFunc("GET /api/v1/docker/projects/{name}/files/{kind}", s.handleDockerReadFile)
	api.HandleFunc("PUT /api/v1/docker/projects/{name}/files/{kind}", s.handleDockerWriteFile)
	api.HandleFunc("GET /api/v1/terminal", s.handleTerminalInfo)
	api.HandleFunc("GET /api/v1/remote/providers", s.handleRemoteProviders)
	api.HandleFunc("GET /api/v1/remote/guacd", s.handleGuacdConfig)
	api.HandleFunc("PUT /api/v1/remote/guacd", s.handleUpdateGuacdConfig)
	api.HandleFunc("POST /api/v1/remote/guacd/test", s.handleTestGuacd)
	api.HandleFunc("GET /api/v1/remote/targets", s.handleRemoteTargets)
	api.HandleFunc("POST /api/v1/remote/targets", s.handleCreateRemoteTarget)
	api.HandleFunc("PUT /api/v1/remote/targets/{id}", s.handleUpdateRemoteTarget)
	api.HandleFunc("DELETE /api/v1/remote/targets/{id}", s.handleDeleteRemoteTarget)
	api.HandleFunc("GET /api/v1/remote/sessions", s.handleRemoteSessions)
	api.HandleFunc("GET /api/v1/remote/sessions/{id}/diagnostics", s.handleRemoteSessionDiagnostics)
	api.HandleFunc("POST /api/v1/remote/targets/{id}/sessions", s.handleStartRemoteSession)
	api.HandleFunc("DELETE /api/v1/remote/sessions/{id}", s.handleStopRemoteSession)
	api.HandleFunc("POST /api/v1/remote/sessions/{id}/client-ticket", s.handleRemoteClientTicket)
	api.HandleFunc("POST /api/v1/remote/sessions/{id}/transport-ticket", s.handleRemoteTransportTicket)
	api.HandleFunc("POST /api/v1/terminal/ticket", s.handleTerminalTicket)
	api.HandleFunc("GET /api/v1/overview", s.handleOverview)
	api.HandleFunc("GET /api/v1/processes", s.handleListProcesses)
	api.HandleFunc("POST /api/v1/processes", s.handleCreateProcess)
	api.HandleFunc("PUT /api/v1/processes/{id}", s.handleUpdateProcess)
	api.HandleFunc("DELETE /api/v1/processes/{id}", s.handleDeleteProcess)
	api.HandleFunc("POST /api/v1/processes/{id}/start", s.handleStartProcess)
	api.HandleFunc("POST /api/v1/processes/{id}/stop", s.handleStopProcess)
	api.HandleFunc("POST /api/v1/processes/{id}/restart", s.handleRestartProcess)
	api.HandleFunc("GET /api/v1/processes/{id}/log", s.handleProcessLog)

	api.HandleFunc("GET /api/v1/jobs", s.handleListJobs)
	api.HandleFunc("POST /api/v1/jobs", s.handleCreateJob)
	api.HandleFunc("PUT /api/v1/jobs/{id}", s.handleUpdateJob)
	api.HandleFunc("DELETE /api/v1/jobs/{id}", s.handleDeleteJob)
	api.HandleFunc("POST /api/v1/jobs/{id}/run", s.handleRunJob)

	api.HandleFunc("GET /api/v1/runs", s.handleRuns)
	api.HandleFunc("GET /api/v1/runs/{id}/log", s.handleRunLog)
	api.HandleFunc("GET /api/v1/storage", s.handleListStorage)
	api.HandleFunc("GET /api/v1/storage/{id}/entries", s.handleStorageEntries)
	api.HandleFunc("POST /api/v1/storage/{id}/download-ticket", s.handleDownloadTicket)
	api.HandleFunc("POST /api/v1/storage/{id}/upload", s.handleUpload)
	api.HandleFunc("POST /api/v1/storage/{id}/directories", s.handleCreateDirectory)
	api.HandleFunc("POST /api/v1/storage/{id}/rename", s.handleRename)
	api.HandleFunc("POST /api/v1/storage/{id}/move", s.handleMove)
	api.HandleFunc("POST /api/v1/storage/{id}/copy", s.handleCopy)
	api.HandleFunc("GET /api/v1/storage/{id}/text", s.handleReadText)
	api.HandleFunc("PUT /api/v1/storage/{id}/text", s.handleWriteText)
	api.HandleFunc("POST /api/v1/storage/{id}/delete", s.handleStorageDelete)
	mux.HandleFunc("GET /api/v1/storage/download/{ticket}", s.handleTicketDownload)
	api.HandleFunc("GET /api/v1/software/providers", s.handleSoftwareProviders)
	api.HandleFunc("GET /api/v1/software/providers/{id}", s.handleSoftwareProvider)
	api.HandleFunc("PUT /api/v1/software/providers/{id}", s.handleUpdateSoftwareProvider)
	api.HandleFunc("GET /api/v1/software/providers/{id}/installed", s.handleSoftwareInstalled)
	api.HandleFunc("GET /api/v1/software/providers/{id}/search", s.handleSoftwareSearch)
	api.HandleFunc("GET /api/v1/software/providers/{id}/updates", s.handleSoftwareUpdates)
	api.HandleFunc("GET /api/v1/software/providers/{id}/buckets", s.handleSoftwareBuckets)
	api.HandleFunc("POST /api/v1/software/providers/{id}/buckets", s.handleSoftwareAddBucket)
	api.HandleFunc("DELETE /api/v1/software/providers/{id}/buckets/{bucket}", s.handleSoftwareRemoveBucket)
	api.HandleFunc("POST /api/v1/software/providers/{id}/install", s.handleSoftwareInstall)
	api.HandleFunc("POST /api/v1/software/providers/{id}/packages/{package}/upgrade", s.handleSoftwareUpgrade)
	api.HandleFunc("POST /api/v1/software/providers/{id}/packages/{package}/uninstall", s.handleSoftwareUninstall)
	api.HandleFunc("POST /api/v1/software/providers/{id}/upgrade-all", s.handleSoftwareUpgradeAll)
	api.HandleFunc("POST /api/v1/software/providers/{id}/refresh", s.handleSoftwareRefresh)

	mux.Handle("/api/", s.auth(api))
	// The embedded upstream HTML5 client cannot attach a Bearer header to every
	// asset and WebSocket request. It receives only a scoped, HttpOnly,
	// path-scoped cookie after an authenticated client-ticket request.
	mux.HandleFunc("GET /api/v1/remote/sessions/{id}/client/{path...}", s.handleRemoteClient)
	mux.HandleFunc("GET /api/v1/remote/sessions/{id}/transport", s.handleRemoteTransport)
	mux.HandleFunc("GET /remote/session/{id}", s.handleRemoteSessionPage)
	// A terminal connection is authenticated by its short-lived, single-use
	// ticket. It intentionally does not accept the permanent API token in a URL.
	mux.HandleFunc("GET /api/v1/terminal/connect", s.handleTerminalConnect)
	mux.HandleFunc("GET /plugins/{id}/{path...}", s.handlePluginAsset)

	sub, _ := fs.Sub(staticFS, "static")
	static := http.FileServer(http.FS(sub))
	mux.Handle("/", static)
	if s.basePath == "/" {
		return logging(mux)
	}
	return logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		mux.ServeHTTP(w, clone)
	}))
}

// Close releases all interactive shell sessions during RunPilot shutdown.
func (s *Server) Close() error { return s.terminal.Close() }

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

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.ctrl.Snapshot().Server.Token
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if token == "" || auth != "Bearer "+token {
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
	})
}

func (s *Server) handlePluginRuntime(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.Plugins().FrontendExtensions())
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
	writeJSON(w, http.StatusOK, map[string]any{"plugins": s.ctrl.Plugins().Statuses()})
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
		"name":                 "RunPilot",
		"version":              version.Version,
		"dataDir":              s.ctrl.DataDir(),
		"configPath":           s.ctrl.ConfigPath(),
		"bind":                 cfg.Server.Bind,
		"capabilities":         platform.CurrentCapabilities(),
		"websocketPayloadMode": requestWebSocketPayloadMode(r, cfg.Server.WebSocketPayloadMode),
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

func (s *Server) dockerSupported(w http.ResponseWriter) bool {
	if !platform.CurrentCapabilities().DockerCompose {
		writeError(w, http.StatusNotImplemented, dockercompose.ErrUnsupported)
		return false
	}
	return true
}

func (s *Server) handleDockerRuntime(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.docker.Runtime(r.Context()))
}
func (s *Server) handleDockerProjects(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	runtime, projects, err := s.docker.List(r.Context())
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime": runtime, "projects": projects})
}
func (s *Server) handleDockerVolumes(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	runtime, volumes, err := s.docker.ListVolumes(r.Context())
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime": runtime, "volumes": volumes})
}
func (s *Server) handleDockerCreateVolume(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	v, err := s.docker.CreateVolume(r.Context(), body.Name)
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}
func (s *Server) handleDockerDeleteVolume(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.DeleteVolume(r.Context(), r.PathValue("name")); err != nil {
		dockerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleDockerNetworks(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	runtime, networks, err := s.docker.ListNetworks(r.Context())
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime": runtime, "networks": networks})
}
func (s *Server) handleDockerCreateNetwork(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	network, err := s.docker.CreateNetwork(r.Context(), body.Name)
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, network)
}
func (s *Server) handleDockerDeleteNetwork(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.DeleteNetwork(r.Context(), r.PathValue("name")); err != nil {
		dockerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleDockerContainerAction(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.ContainerAction(r.Context(), r.PathValue("id"), r.PathValue("action")); err != nil {
		dockerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleDockerContainerLogs(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	logs, err := s.docker.ContainerLogs(r.Context(), r.PathValue("id"))
	if err != nil {
		dockerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(logs))
}
func (s *Server) handleDockerContainerAttachTicket(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.TerminalContainer(r.Context(), r.PathValue("id")); err != nil {
		dockerError(w, err)
		return
	}
	var request struct{ Cols, Rows uint16 }
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Cols == 0 {
		request.Cols = 80
	}
	if request.Rows == 0 {
		request.Rows = 24
	}
	if request.Cols > 500 || request.Rows > 300 {
		writeError(w, http.StatusBadRequest, errors.New("terminal dimensions are too large"))
		return
	}
	ticket, err := secureTicket()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.terminalTicketMu.Lock()
	s.terminalTickets[ticket] = terminalTicket{DockerExecID: r.PathValue("id"), Cols: request.Cols, Rows: request.Rows, Expires: time.Now().Add(time.Minute)}
	s.terminalTicketMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"ticket": ticket})
}
func (s *Server) handleDockerCreateProject(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	p, err := s.docker.Create(body.Name)
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}
func (s *Server) handleDockerAction(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.Action(r.Context(), r.PathValue("name"), r.PathValue("action")); err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}
func (s *Server) handleDockerDeleteProject(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.Delete(r.Context(), r.PathValue("name")); err != nil {
		dockerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleDockerReadFile(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	content, err := s.docker.ReadFile(r.PathValue("name"), r.PathValue("kind"))
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}
func (s *Server) handleDockerWriteFile(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.docker.WriteFile(r.PathValue("name"), r.PathValue("kind"), body.Content); err != nil {
		dockerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func dockerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dockercompose.ErrInvalidName), strings.Contains(err.Error(), "invalid Compose action"), strings.Contains(err.Error(), "invalid managed file"):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, dockercompose.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, dockercompose.ErrReadOnly):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, dockercompose.ErrBusy), errors.Is(err, dockercompose.ErrProjectNotDown):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, dockercompose.ErrRuntimeUnavailable):
		writeError(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, dockercompose.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, err)
	case errors.Is(err, dockercompose.ErrFileTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err)
	case errors.Is(err, dockercompose.ErrVolumeInUse), errors.Is(err, dockercompose.ErrNetworkInUse), errors.Is(err, dockercompose.ErrProtectedNetwork), errors.Is(err, dockercompose.ErrNetworkExists):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, dockercompose.ErrContainerRunning):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, dockercompose.ErrContainerNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, dockercompose.ErrContainerReadOnly):
		writeError(w, http.StatusForbidden, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func (s *Server) handleTerminalInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"available":    s.terminal.Available(),
		"shells":       s.terminal.Shells(),
		"defaultShell": s.terminal.DefaultShell(),
	})
}

func (s *Server) handleTerminalTicket(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Shell string `json:"shell"`
		Cols  uint16 `json:"cols"`
		Rows  uint16 `json:"rows"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if !s.terminal.Available() {
		writeError(w, http.StatusServiceUnavailable, errors.New("terminal is not available on this host"))
		return
	}
	if request.Shell != "" {
		found := false
		for _, shell := range s.terminal.Shells() {
			if shell.ID == request.Shell {
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusBadRequest, terminal.ErrUnknownShell)
			return
		}
	}
	if request.Cols == 0 {
		request.Cols = 80
	}
	if request.Rows == 0 {
		request.Rows = 24
	}
	if request.Cols > 500 || request.Rows > 300 {
		writeError(w, http.StatusBadRequest, errors.New("terminal dimensions are too large"))
		return
	}
	ticket, err := secureTicket()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.terminalTicketMu.Lock()
	s.terminalTickets[ticket] = terminalTicket{Shell: request.Shell, Cols: request.Cols, Rows: request.Rows, Expires: time.Now().Add(time.Minute)}
	s.terminalTicketMu.Unlock()
	// The browser resolves the WebSocket endpoint from document.baseURI so it
	// retains the public scheme, host, port, and any configured base path.
	// Returning only the ticket prevents a backend address from leaking into
	// the browser-facing connection URL.
	writeJSON(w, http.StatusCreated, map[string]string{"ticket": ticket})
}

func (s *Server) handleTerminalConnect(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("ticket") == "" {
		http.Error(w, "terminal ticket required", http.StatusUnauthorized)
		return
	}
	// coder/websocket validates Origin against the request Host by default.
	// Do not set InsecureSkipVerify or broad origin patterns here.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(websocketsecure.MaxEncryptedMessageSize)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	ticket, ok := s.consumeTerminalTicket(r.URL.Query().Get("ticket"))
	if !ok {
		_ = conn.Close(websocket.StatusPolicyViolation, "terminal connection expired or already used")
		return
	}
	secure, err := websocketsecure.ServerHandshake(ctx, conn, requestWebSocketPayloadMode(r, s.ctrl.Snapshot().Server.WebSocketPayloadMode))
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "secure WebSocket negotiation failed")
		return
	}
	var session *terminal.Session
	if ticket.DockerExecID != "" {
		session, err = s.terminal.StartCommand("Container terminal", "docker", []string{"exec", "-i", "-t", ticket.DockerExecID, "/bin/sh"}, ticket.Cols, ticket.Rows)
	} else {
		session, err = s.terminal.Start(ticket.Shell, ticket.Cols, ticket.Rows)
	}
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, err.Error())
		return
	}
	defer session.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 32*1024)
		for {
			n, readErr := session.Read(buffer)
			if n > 0 {
				if writeErr := secure.Write(ctx, websocket.MessageBinary, buffer[:n]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	for {
		kind, data, readErr := secure.Read(ctx)
		if readErr != nil {
			break
		}
		switch kind {
		case websocket.MessageBinary:
			if _, err := session.Write(data); err != nil {
				return
			}
		case websocket.MessageText:
			var control struct {
				Type       string `json:"type"`
				Cols, Rows uint16
			}
			if json.Unmarshal(data, &control) != nil || control.Type != "resize" || session.Resize(control.Cols, control.Rows) != nil {
				_ = conn.Close(websocket.StatusPolicyViolation, "invalid terminal control message")
				return
			}
		}
	}
	cancel()
	_ = session.Close()
	<-done
}

func (s *Server) consumeTerminalTicket(value string) (terminalTicket, bool) {
	s.terminalTicketMu.Lock()
	defer s.terminalTicketMu.Unlock()
	ticket, ok := s.terminalTickets[value]
	if ok {
		delete(s.terminalTickets, value)
	}
	if !ok || time.Now().After(ticket.Expires) {
		return terminalTicket{}, false
	}
	return ticket, true
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

func (s *Server) handleListProcesses(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.ProcessViews())
}

func (s *Server) handleCreateProcess(w http.ResponseWriter, r *http.Request) {
	var p model.ProcessDefinition
	if !decodeJSON(w, r, &p) {
		return
	}
	p.ID = ""
	out, err := s.ctrl.UpsertProcess(p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleUpdateProcess(w http.ResponseWriter, r *http.Request) {
	var p model.ProcessDefinition
	if !decodeJSON(w, r, &p) {
		return
	}
	p.ID = r.PathValue("id")
	out, err := s.ctrl.UpsertProcess(p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteProcess(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.DeleteProcess(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStartProcess(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.StartProcess(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "starting"})
}

func (s *Server) handleStopProcess(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.StopProcess(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "stopping"})
}

func (s *Server) handleRestartProcess(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.RestartProcess(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "restarted"})
}

func (s *Server) handleProcessLog(w http.ResponseWriter, r *http.Request) {
	text, err := s.ctrl.ProcessLog(r.PathValue("id"), queryLimit(r, 200))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(text))
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.JobViews())
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var j model.JobDefinition
	if !decodeJSON(w, r, &j) {
		return
	}
	j.ID = ""
	out, err := s.ctrl.UpsertJob(j)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request) {
	var j model.JobDefinition
	if !decodeJSON(w, r, &j) {
		return
	}
	j.ID = r.PathValue("id")
	out, err := s.ctrl.UpsertJob(j)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.DeleteJob(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRunJob(w http.ResponseWriter, r *http.Request) {
	runID, err := s.ctrl.RunJob(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"runId": runID})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	targetID := r.URL.Query().Get("targetId")
	if targetID != "" && !validRunTargetID(targetID) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid target id"))
		return
	}
	runs, err := s.ctrl.RecentRuns(targetID, queryLimit(r, 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func validRunTargetID(id string) bool {
	if id == "" || id != path.Base(id) {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) handleRunLog(w http.ResponseWriter, r *http.Request) {
	text, err := s.ctrl.RunLog(r.PathValue("id"), queryLimit(r, 500))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(text))
}

func (s *Server) handleListStorage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.StorageLocations(r.Context()))
}
func storageCapabilities(p storage.Provider, path string) storage.Capabilities {
	if scoped, ok := p.(storage.PathCapabilitiesProvider); ok {
		return scoped.CapabilitiesFor(path)
	}
	return p.Capabilities()
}
func (s *Server) handleStorageEntries(w http.ResponseWriter, r *http.Request) {
	p, e := s.ctrl.StorageProvider(r.Context(), r.PathValue("id"))
	if e != nil {
		writeError(w, http.StatusNotFound, e)
		return
	}
	storagePath := r.URL.Query().Get("path")
	if !storageCapabilities(p, storagePath).Browse {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("browse is not supported"))
		return
	}
	showHidden := r.URL.Query().Get("showHidden") == "true"
	var out any
	if filtered, ok := p.(interface {
		ListWithOptions(string, storage.ListOptions) (storage.Listing, error)
	}); ok {
		out, e = filtered.ListWithOptions(storagePath, storage.ListOptions{ShowHidden: showHidden})
	} else {
		out, e = p.List(storagePath)
	}
	if e != nil {
		storageError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleDownloadTicket(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &v) {
		return
	}
	p, e := s.ctrl.StorageProvider(r.Context(), r.PathValue("id"))
	if e != nil {
		writeError(w, http.StatusNotFound, e)
		return
	}
	if !storageCapabilities(p, v.Path).Download {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("download is not supported"))
		return
	}
	o, e := p.Open(v.Path)
	if e != nil {
		storageError(w, e)
		return
	}
	o.Reader.Close()
	b := make([]byte, 24)
	if _, e = rand.Read(b); e != nil {
		writeError(w, 500, e)
		return
	}
	t := fmt.Sprintf("%x", b)
	s.ticketMu.Lock()
	s.tickets[t] = downloadTicket{r.PathValue("id"), v.Path, time.Now().Add(2 * time.Minute)}
	s.ticketMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"url": s.downloadTicketURL(t)})
}

func (s *Server) downloadTicketURL(ticket string) string {
	prefix := s.basePath
	if prefix == "/" {
		prefix = ""
	}
	return prefix + "/api/v1/storage/download/" + ticket
}
func (s *Server) handleTicketDownload(w http.ResponseWriter, r *http.Request) {
	s.ticketMu.Lock()
	t, ok := s.tickets[r.PathValue("ticket")]
	if ok {
		delete(s.tickets, r.PathValue("ticket"))
	}
	s.ticketMu.Unlock()
	if !ok || time.Now().After(t.Expires) {
		http.NotFound(w, r)
		return
	}
	p, e := s.ctrl.StorageProvider(r.Context(), t.StorageID)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	o, e := p.Open(t.Path)
	if e != nil {
		storageError(w, e)
		return
	}
	defer o.Reader.Close()
	ctype := mime.TypeByExtension(path.Ext(o.Name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": o.Name}))
	if o.Size != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(*o.Size, 10))
	}
	if o.ModifiedAt != nil {
		w.Header().Set("Last-Modified", o.ModifiedAt.UTC().Format(http.TimeFormat))
	}
	_, _ = io.Copy(w, o.Reader)
}
func (s *Server) mutable(w http.ResponseWriter, id, action string) (interface {
	CreateDirectory(string, string) error
	Rename(string, string) error
	Move(string, string) error
	Delete(string) error
}, bool) {
	p, e := s.ctrl.StorageProvider(context.Background(), id)
	if e != nil {
		writeError(w, 404, e)
		return nil, false
	}
	caps := p.Capabilities()
	if !map[string]bool{"mkdir": caps.CreateDirectory, "rename": caps.Rename, "move": caps.Move, "delete": caps.Delete}[action] {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("%s is not supported", action))
		return nil, false
	}
	m, ok := p.(interface {
		CreateDirectory(string, string) error
		Rename(string, string) error
		Move(string, string) error
		Delete(string) error
	})
	if !ok || !p.Capabilities().TextEdit {
		writeError(w, 405, fmt.Errorf("operation is not supported"))
		return nil, false
	}
	return m, true
}
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	p, e := s.ctrl.StorageProvider(r.Context(), r.PathValue("id"))
	if e != nil {
		writeError(w, 404, e)
		return
	}
	u, ok := p.(interface {
		Upload(string, string, io.Reader) error
	})
	if !ok || !p.Capabilities().Upload {
		writeError(w, 405, fmt.Errorf("upload is not supported"))
		return
	}
	if e = r.ParseMultipartForm(32 << 20); e != nil {
		writeError(w, 400, e)
		return
	}
	parent := r.FormValue("path")
	for _, h := range r.MultipartForm.File["files"] {
		f, e := h.Open()
		if e == nil {
			e = u.Upload(parent, h.Filename, f)
			f.Close()
		}
		if e != nil {
			storageError(w, e)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleCreateDirectory(w http.ResponseWriter, r *http.Request) {
	var v struct {
		ParentPath string `json:"parentPath"`
		Name       string `json:"name"`
	}
	if !decodeJSON(w, r, &v) {
		return
	}
	m, ok := s.mutable(w, r.PathValue("id"), "mkdir")
	if !ok {
		return
	}
	if e := m.CreateDirectory(v.ParentPath, v.Name); e != nil {
		storageError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Path    string `json:"path"`
		NewName string `json:"newName"`
	}
	if !decodeJSON(w, r, &v) {
		return
	}
	m, ok := s.mutable(w, r.PathValue("id"), "rename")
	if !ok {
		return
	}
	if e := m.Rename(v.Path, v.NewName); e != nil {
		storageError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	var v struct {
		SourcePath           string `json:"sourcePath"`
		DestinationDirectory string `json:"destinationDirectory"`
	}
	if !decodeJSON(w, r, &v) {
		return
	}
	m, ok := s.mutable(w, r.PathValue("id"), "move")
	if !ok {
		return
	}
	if e := m.Move(v.SourcePath, v.DestinationDirectory); e != nil {
		storageError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request) {
	var v struct {
		SourcePath           string `json:"sourcePath"`
		DestinationDirectory string `json:"destinationDirectory"`
	}
	if !decodeJSON(w, r, &v) {
		return
	}
	p, err := s.ctrl.StorageProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, err)
		return
	}
	c, ok := p.(interface{ Copy(string, string) error })
	if !ok || !p.Capabilities().Copy {
		writeError(w, 405, fmt.Errorf("copy is not supported"))
		return
	}
	if err := c.Copy(v.SourcePath, v.DestinationDirectory); err != nil {
		storageError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) textEditor(w http.ResponseWriter, id string) (interface {
	ReadText(string) (string, error)
	WriteText(string, string) error
}, bool) {
	p, err := s.ctrl.StorageProvider(context.Background(), id)
	if err != nil {
		writeError(w, 404, err)
		return nil, false
	}
	e, ok := p.(interface {
		ReadText(string) (string, error)
		WriteText(string, string) error
	})
	if !ok {
		writeError(w, 405, fmt.Errorf("editing is not supported"))
		return nil, false
	}
	return e, true
}
func (s *Server) handleReadText(w http.ResponseWriter, r *http.Request) {
	p, err := s.ctrl.StorageProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !storageCapabilities(p, r.URL.Query().Get("path")).Browse {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("reading is not supported"))
		return
	}
	e, ok := s.textEditor(w, r.PathValue("id"))
	if !ok {
		return
	}
	content, err := e.ReadText(r.URL.Query().Get("path"))
	if err != nil {
		storageError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"content": content})
}
func (s *Server) handleWriteText(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &v) {
		return
	}
	p, err := s.ctrl.StorageProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !storageCapabilities(p, v.Path).TextEdit {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("editing is not supported"))
		return
	}
	e, ok := s.textEditor(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := e.WriteText(v.Path, v.Content); err != nil {
		storageError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleStorageDelete(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &v) {
		return
	}
	m, ok := s.mutable(w, r.PathValue("id"), "delete")
	if !ok {
		return
	}
	if e := m.Delete(v.Path); e != nil {
		storageError(w, e)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) softwareProvider(w http.ResponseWriter, r *http.Request) (interface {
	Status(context.Context) software.Status
	Installed(context.Context) ([]software.Package, error)
	Search(context.Context, string) ([]software.Package, error)
	Updates(context.Context) ([]software.Package, error)
	Install(context.Context, string) error
	Upgrade(context.Context, string) error
	UpgradeAll(context.Context) error
	Uninstall(context.Context, string) error
	Refresh(context.Context) error
}, bool) {
	p, err := s.ctrl.SoftwareProvider(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return nil, false
	}
	return p, true
}

func softwareError(w http.ResponseWriter, err error) { writeError(w, http.StatusConflict, err) }
func (s *Server) handleSoftwareProviders(w http.ResponseWriter, r *http.Request) {
	out := make([]software.Status, 0, len(s.ctrl.SoftwareDefinitions()))
	for _, d := range s.ctrl.SoftwareDefinitions() {
		p, err := s.ctrl.SoftwareProvider(d.ID)
		if err != nil {
			out = append(out, software.Status{ID: d.ID, Name: d.Name, Type: string(d.Type), State: software.StateUnavailable, Message: err.Error()})
			continue
		}
		out = append(out, p.Status(r.Context()))
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleSoftwareProvider(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.softwareProvider(w, r); ok {
		writeJSON(w, http.StatusOK, p.Status(r.Context()))
	}
}
func (s *Server) handleUpdateSoftwareProvider(w http.ResponseWriter, r *http.Request) {
	var d model.SoftwareProviderDefinition
	if !decodeJSON(w, r, &d) {
		return
	}
	d.ID = r.PathValue("id")
	current, err := softwareDefinition(s.ctrl.SoftwareDefinitions(), d.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// Configuration UI edits the root only; retain stable identity/type/name.
	if d.Name == "" {
		d.Name = current.Name
	}
	if d.Type == "" {
		d.Type = current.Type
	}
	if d.Scoop == nil {
		d.Scoop = current.Scoop
	}
	out, err := s.ctrl.UpsertSoftwareProvider(d)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func softwareDefinition(all []model.SoftwareProviderDefinition, id string) (model.SoftwareProviderDefinition, error) {
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return model.SoftwareProviderDefinition{}, fmt.Errorf("unknown software provider %q", id)
}
func (s *Server) handleSoftwareInstalled(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.softwareProvider(w, r); ok {
		out, err := p.Installed(r.Context())
		if err != nil {
			softwareError(w, err)
			return
		}
		writeJSON(w, 200, out)
	}
}
func (s *Server) handleSoftwareSearch(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.softwareProvider(w, r); ok {
		out, err := p.Search(r.Context(), r.URL.Query().Get("q"))
		if err != nil {
			writeError(w, 400, err)
			return
		}
		writeJSON(w, 200, out)
	}
}
func (s *Server) handleSoftwareUpdates(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.softwareProvider(w, r); ok {
		out, err := p.Updates(r.Context())
		if err != nil {
			softwareError(w, err)
			return
		}
		writeJSON(w, 200, out)
	}
}
func (s *Server) softwareBuckets(w http.ResponseWriter, r *http.Request) (software.BucketManager, bool) {
	p, ok := s.softwareProvider(w, r)
	if !ok {
		return nil, false
	}
	manager, ok := p.(software.BucketManager)
	if !ok {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("software provider %q does not support bucket management", r.PathValue("id")))
		return nil, false
	}
	return manager, true
}
func (s *Server) handleSoftwareBuckets(w http.ResponseWriter, r *http.Request) {
	if manager, ok := s.softwareBuckets(w, r); ok {
		out, err := manager.Buckets(r.Context())
		if err != nil {
			softwareError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}
func (s *Server) handleSoftwareAddBucket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if manager, ok := s.softwareBuckets(w, r); ok {
		if err := manager.AddBucket(r.Context(), body.Name, body.Source); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "added"})
	}
}
func (s *Server) handleSoftwareRemoveBucket(w http.ResponseWriter, r *http.Request) {
	if manager, ok := s.softwareBuckets(w, r); ok {
		if err := manager.RemoveBucket(r.Context(), r.PathValue("bucket")); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
	}
}
func (s *Server) softwarePackage(w http.ResponseWriter, r *http.Request, op func(interface {
	Install(context.Context, string) error
	Upgrade(context.Context, string) error
	Uninstall(context.Context, string) error
}, string) error) {
	if !software.ValidPackageID(r.PathValue("package")) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid package ID %q", r.PathValue("package")))
		return
	}
	p, ok := s.softwareProvider(w, r)
	if !ok {
		return
	}
	if err := op(p, r.PathValue("package")); err != nil {
		softwareError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p.Status(r.Context()))
}
func (s *Server) handleSoftwareInstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Package string `json:"package"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !software.ValidPackageID(body.Package) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid package ID %q", body.Package))
		return
	}
	p, ok := s.softwareProvider(w, r)
	if !ok {
		return
	}
	if err := p.Install(r.Context(), body.Package); err != nil {
		softwareError(w, err)
		return
	}
	writeJSON(w, 200, p.Status(r.Context()))
}
func (s *Server) handleSoftwareUpgrade(w http.ResponseWriter, r *http.Request) {
	s.softwarePackage(w, r, func(p interface {
		Install(context.Context, string) error
		Upgrade(context.Context, string) error
		Uninstall(context.Context, string) error
	}, id string) error {
		return p.Upgrade(r.Context(), id)
	})
}
func (s *Server) handleSoftwareUninstall(w http.ResponseWriter, r *http.Request) {
	s.softwarePackage(w, r, func(p interface {
		Install(context.Context, string) error
		Upgrade(context.Context, string) error
		Uninstall(context.Context, string) error
	}, id string) error {
		return p.Uninstall(r.Context(), id)
	})
}
func (s *Server) handleSoftwareUpgradeAll(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.softwareProvider(w, r); ok {
		if err := p.UpgradeAll(r.Context()); err != nil {
			softwareError(w, err)
			return
		}
		writeJSON(w, 200, p.Status(r.Context()))
	}
}
func (s *Server) handleSoftwareRefresh(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.softwareProvider(w, r); ok {
		if err := p.Refresh(r.Context()); err != nil {
			softwareError(w, err)
			return
		}
		writeJSON(w, 200, p.Status(r.Context()))
	}
}
func storageError(w http.ResponseWriter, e error) {
	if errors.Is(e, fs.ErrNotExist) {
		writeError(w, 404, e)
		return
	}
	if errors.Is(e, fs.ErrPermission) {
		writeError(w, 403, e)
		return
	}
	writeError(w, 409, e)
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
