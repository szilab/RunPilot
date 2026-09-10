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
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/software"
	"github.com/szilab/RunPilot/internal/storage"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	ctrl     *core.Controller
	basePath string
	tickets  map[string]downloadTicket
	ticketMu sync.Mutex
}
type downloadTicket struct {
	StorageID, Path string
	Expires         time.Time
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
	return &Server{ctrl: ctrl, basePath: basePath, tickets: map[string]downloadTicket{}}, nil
}

func (s *Server) BasePath() string { return s.basePath }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/system", s.handleSystem)
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
	api.HandleFunc("POST /api/v1/storage", s.handleCreateStorage)
	api.HandleFunc("PUT /api/v1/storage/{id}", s.handleUpdateStorage)
	api.HandleFunc("DELETE /api/v1/storage/{id}", s.handleDeleteStorage)
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

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctrl.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"name":       "RunPilot",
		"version":    "0.1.0-dev",
		"dataDir":    s.ctrl.DataDir(),
		"configPath": s.ctrl.ConfigPath(),
		"bind":       cfg.Server.Bind,
	})
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
	runs, err := s.ctrl.RecentRuns(queryLimit(r, 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
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
	type view struct {
		model.StorageDefinition
		Capabilities any  `json:"capabilities"`
		Available    bool `json:"available"`
	}
	out := make([]view, 0)
	for _, d := range s.ctrl.StorageDefinitions() {
		p, e := s.ctrl.StorageProvider(d.ID)
		v := view{StorageDefinition: d, Available: e == nil}
		if e == nil {
			v.Capabilities = p.Capabilities()
			if d.Local.Scope == model.LocalStorageScopeRoot {
				_, e = p.List("")
				v.Available = e == nil
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleCreateStorage(w http.ResponseWriter, r *http.Request) {
	var d model.StorageDefinition
	if !decodeJSON(w, r, &d) {
		return
	}
	d.ID = ""
	out, e := s.ctrl.UpsertStorage(d)
	if e != nil {
		writeError(w, http.StatusBadRequest, e)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
func (s *Server) handleUpdateStorage(w http.ResponseWriter, r *http.Request) {
	var d model.StorageDefinition
	if !decodeJSON(w, r, &d) {
		return
	}
	d.ID = r.PathValue("id")
	out, e := s.ctrl.UpsertStorage(d)
	if e != nil {
		writeError(w, http.StatusBadRequest, e)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleDeleteStorage(w http.ResponseWriter, r *http.Request) {
	if e := s.ctrl.DeleteStorage(r.PathValue("id")); e != nil {
		writeError(w, http.StatusNotFound, e)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleStorageEntries(w http.ResponseWriter, r *http.Request) {
	p, e := s.ctrl.StorageProvider(r.PathValue("id"))
	if e != nil {
		writeError(w, http.StatusNotFound, e)
		return
	}
	storagePath := r.URL.Query().Get("path")
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
	p, e := s.ctrl.StorageProvider(r.PathValue("id"))
	if e != nil {
		writeError(w, http.StatusNotFound, e)
		return
	}
	if !p.Capabilities().Download {
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
	p, e := s.ctrl.StorageProvider(t.StorageID)
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
func (s *Server) mutable(w http.ResponseWriter, id string) (interface {
	CreateDirectory(string, string) error
	Rename(string, string) error
	Move(string, string) error
	Delete(string) error
}, bool) {
	p, e := s.ctrl.StorageProvider(id)
	if e != nil {
		writeError(w, 404, e)
		return nil, false
	}
	m, ok := p.(interface {
		CreateDirectory(string, string) error
		Rename(string, string) error
		Move(string, string) error
		Delete(string) error
	})
	if !ok {
		writeError(w, 405, fmt.Errorf("operation is not supported"))
		return nil, false
	}
	return m, true
}
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	p, e := s.ctrl.StorageProvider(r.PathValue("id"))
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
	m, ok := s.mutable(w, r.PathValue("id"))
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
	m, ok := s.mutable(w, r.PathValue("id"))
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
	m, ok := s.mutable(w, r.PathValue("id"))
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
	p, err := s.ctrl.StorageProvider(r.PathValue("id"))
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
	p, err := s.ctrl.StorageProvider(id)
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
	m, ok := s.mutable(w, r.PathValue("id"))
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
	n, err := strconv.Atoi(r.URL.Query().Get("lines"))
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
