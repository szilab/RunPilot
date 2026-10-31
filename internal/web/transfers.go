package web

import (
	"errors"
	"net/http"

	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/plugins"
)

// Workspace transfers stream plugin-approved files over plain authenticated
// HTTP. A plugin issues the single-purpose ticket through the workspace
// capability; this route only serves or stores the bytes.

func transferError(w http.ResponseWriter, err error) {
	var failure *plugins.HostFailure
	switch {
	case errors.Is(err, core.ErrWorkspaceTransferUnknown):
		writeError(w, http.StatusNotFound, err)
	case errors.As(err, &failure) && failure.Code == "already_exists":
		writeError(w, http.StatusConflict, errors.New(failure.Message))
	case errors.As(err, &failure) && failure.Code == "too_large":
		writeError(w, http.StatusRequestEntityTooLarge, errors.New(failure.Message))
	case errors.As(err, &failure):
		writeError(w, http.StatusBadRequest, errors.New(failure.Message))
	default:
		writeError(w, http.StatusInternalServerError, errors.New("transfer failed"))
	}
}

func (s *Server) handleTransferDownload(w http.ResponseWriter, r *http.Request) {
	download, err := s.ctrl.OpenWorkspaceDownload(r.PathValue("ticket"))
	if err != nil {
		transferError(w, err)
		return
	}
	defer download.File.Close()
	header := w.Header()
	header.Set("Content-Type", download.ContentType)
	header.Set("Content-Disposition", download.ContentDisposition())
	header.Set("X-Content-Type-Options", "nosniff")
	// User files never run as RunPilot pages, even when opened directly.
	header.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	header.Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", download.Info.ModTime(), download.File)
}

func (s *Server) handleTransferUpload(w http.ResponseWriter, r *http.Request) {
	size, err := s.ctrl.AcceptWorkspaceUpload(r.Context(), r.PathValue("ticket"), r.Body)
	if err != nil {
		transferError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"size": size})
}
