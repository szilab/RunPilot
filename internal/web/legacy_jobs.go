package web

import (
	"net/http"

	"github.com/szilab/RunPilot/internal/model"
)

// Legacy Backup job REST handlers. They are not mounted by Handler (see
// legacy_routes.go); typed Backup jobs still run from configuration.

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
