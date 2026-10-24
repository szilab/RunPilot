package web

import (
	"context"
	"fmt"
	"net/http"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/software"
)

// Legacy Software (Scoop) REST handlers. They are not mounted by Handler (see
// legacy_routes.go) and remain until the Software plugin reaches parity.

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
