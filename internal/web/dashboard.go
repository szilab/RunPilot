package web

import (
	"net/http"
	"time"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/platform"
)

func (s *Server) handleDashboardStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"host":              platform.HostStatus(),
		"hostUptimeSeconds": int64(platform.HostUptime() / time.Second),
	})
}

func (s *Server) handleLauncherGet(w http.ResponseWriter, r *http.Request) {
	entries := s.ctrl.Snapshot().Launcher
	if entries == nil {
		entries = []model.LauncherEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) handleLauncherPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Entries []model.LauncherEntry `json:"entries"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	entries, err := s.ctrl.SaveLauncher(body.Entries)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
