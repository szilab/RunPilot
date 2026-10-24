package web

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/szilab/RunPilot/internal/storage"
)

// Legacy Storage REST handlers. They are not mounted by Handler (see
// legacy_routes.go) and remain until the Storage plugin reaches parity.

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
	sessionID, _, _ := s.session(r)
	s.ticketMu.Lock()
	s.tickets[t] = downloadTicket{StorageID: r.PathValue("id"), Path: v.Path, SessionID: sessionID, Expires: time.Now().Add(2 * time.Minute)}
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
	sessionID, _, _ := s.session(r)
	if !ok || t.SessionID != sessionID || time.Now().After(t.Expires) {
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
