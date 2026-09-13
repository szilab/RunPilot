package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/remote"
)

func (s *Server) handleRemoteProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.Remote().ProviderStatuses(r.Context()))
}
func (s *Server) handleRemoteTargets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.RemoteTargets())
}
func (s *Server) handleCreateRemoteTarget(w http.ResponseWriter, r *http.Request) {
	var target model.RemoteTarget
	if !decodeJSON(w, r, &target) {
		return
	}
	target.ID = ""
	result, err := s.ctrl.UpsertRemoteTarget(target)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
func (s *Server) handleUpdateRemoteTarget(w http.ResponseWriter, r *http.Request) {
	var target model.RemoteTarget
	if !decodeJSON(w, r, &target) {
		return
	}
	target.ID = r.PathValue("id")
	if _, err := s.ctrl.RemoteTarget(target.ID); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	result, err := s.ctrl.UpsertRemoteTarget(target)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) handleDeleteRemoteTarget(w http.ResponseWriter, r *http.Request) {
	if err := s.ctrl.DeleteRemoteTarget(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleRemoteSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.Remote().Sessions())
}
func (s *Server) handleStartRemoteSession(w http.ResponseWriter, r *http.Request) {
	target, err := s.ctrl.RemoteTarget(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !target.Enabled {
		writeError(w, http.StatusConflict, fmt.Errorf("remote target is disabled"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	session, err := s.ctrl.Remote().Start(ctx, target)
	if err != nil {
		remoteError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}
func (s *Server) handleStopRemoteSession(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := s.ctrl.Remote().Stop(ctx, r.PathValue("id")); err != nil {
		remoteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func remoteError(w http.ResponseWriter, err error) {
	if errors.Is(err, remote.ErrUnknownSession) || errors.Is(err, remote.ErrUnknownProvider) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusConflict, err)
}

func (s *Server) handleRemoteClientTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.ctrl.Remote().Endpoint(id); err != nil {
		remoteError(w, err)
		return
	}
	ticket, err := secureTicket()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.remoteTicketMu.Lock()
	s.remoteTickets[ticket] = remoteClientTicket{SessionID: id, Expires: time.Now().Add(4 * time.Hour)}
	s.remoteTicketMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"ticket": ticket})
}
func (s *Server) handleRemoteClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		if cookie, err := r.Cookie("runpilot_remote"); err == nil {
			ticket = cookie.Value
		}
	}
	if !s.validRemoteTicket(ticket, id) {
		http.Error(w, "remote client authorization required", http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("ticket") != "" {
		path := s.basePath + "/api/v1/remote/sessions/" + id + "/client/"
		if s.basePath == "/" {
			path = "/api/v1/remote/sessions/" + id + "/client/"
		}
		http.SetCookie(w, &http.Cookie{Name: "runpilot_remote", Value: ticket, Path: path, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 4 * 60 * 60})
		clean := strings.TrimPrefix(r.URL.Path, "/api/v1/remote/sessions/"+id+"/client")
		if clean == "" {
			clean = "/"
		}
		prefix := s.basePath
		if prefix == "/" {
			prefix = ""
		}
		// Keep the browser inside the per-session proxy. Redirecting only to
		// clean ("/") would load the RunPilot SPA inside the iframe instead.
		clientPath := "/api/v1/remote/sessions/" + id + "/client"
		http.Redirect(w, r, prefix+clientPath+clean, http.StatusFound)
		return
	}
	endpoint, err := s.ctrl.Remote().Endpoint(id)
	if err != nil {
		remoteError(w, err)
		return
	}
	target := &url.URL{Scheme: "http", Host: endpoint}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(request *http.Request) {
		original(request)
		request.URL.Path = "/" + r.PathValue("path")
		request.URL.RawPath = ""
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
	}
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, proxyErr error) {
		writeError(response, http.StatusBadGateway, fmt.Errorf("remote session transport failed: %w", proxyErr))
	}
	proxy.ServeHTTP(w, r)
}
func (s *Server) validRemoteTicket(ticket, id string) bool {
	if ticket == "" {
		return false
	}
	s.remoteTicketMu.Lock()
	defer s.remoteTicketMu.Unlock()
	item, ok := s.remoteTickets[ticket]
	if !ok || item.SessionID != id || time.Now().After(item.Expires) {
		delete(s.remoteTickets, ticket)
		return false
	}
	return true
}
func (s *Server) handleRemoteSessionPage(w http.ResponseWriter, r *http.Request) {
	base := s.basePath
	if base == "/" {
		base = ""
	}
	http.Redirect(w, r, base+"/?remoteSession="+url.QueryEscape(r.PathValue("id")), http.StatusFound)
}
