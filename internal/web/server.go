package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	ctrl     *core.Controller
	basePath string
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
	return &Server{ctrl: ctrl, basePath: basePath}, nil
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
