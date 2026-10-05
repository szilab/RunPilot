package web

import (
	"github.com/szilab/RunPilot/internal/plugins"
	"net/http"
)

func (s *Server) handlePluginCatalog(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.ctrl.PluginCatalog(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": catalog, "registry": s.ctrl.PluginRegistryURL(), "backendContract": plugins.BackendContractVersion, "frontendContract": plugins.FrontendContractVersion})
}
func (s *Server) handlePluginInstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.ctrl.InstallCatalogPlugin(r.Context(), r.PathValue("id"), body.Version); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	s.handlePlugins(w, r)
}
func (s *Server) handlePluginUninstall(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.UninstallPlugin(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	s.handlePlugins(w, r)
}
