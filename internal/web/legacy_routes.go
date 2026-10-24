package web

import "net/http"

// registerLegacyRoutes holds the REST routes of features that have no normal
// UI and no finished plugin yet: legacy Docker, Storage, Software, Backup jobs
// and the Xpra client proxy. The handlers stay in the codebase until their
// plugin migration reaches parity, but Handler does not expose them.
func (s *Server) registerLegacyRoutes(mux, api *http.ServeMux) {
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
	api.HandleFunc("POST /api/v1/remote/sessions/{id}/client-ticket", s.handleRemoteClientTicket)
	api.HandleFunc("GET /api/v1/jobs", s.handleListJobs)
	api.HandleFunc("POST /api/v1/jobs", s.handleCreateJob)
	api.HandleFunc("PUT /api/v1/jobs/{id}", s.handleUpdateJob)
	api.HandleFunc("DELETE /api/v1/jobs/{id}", s.handleDeleteJob)
	api.HandleFunc("POST /api/v1/jobs/{id}/run", s.handleRunJob)
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

	mux.HandleFunc("GET /api/v1/storage/download/{ticket}", s.handleTicketDownload)
	// The embedded upstream HTML5 client cannot attach a Bearer header to every
	// asset and WebSocket request. It receives only a scoped, HttpOnly,
	// path-scoped cookie after an authenticated client-ticket request.
	mux.HandleFunc("GET /api/v1/remote/sessions/{id}/client/{path...}", s.handleRemoteClient)
	// Docker attach uses a scoped, short-lived ticket rather than the API token.
	mux.HandleFunc("GET /api/v1/docker/attach", s.handleDockerAttach)
}
