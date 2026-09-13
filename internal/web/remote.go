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
func (s *Server) handleRemoteSessionDiagnostics(w http.ResponseWriter, r *http.Request) {
	session, log, err := s.ctrl.Remote().Diagnostics(r.PathValue("id"))
	if err != nil {
		remoteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": session, "log": formatRemoteDiagnostics(session, log)})
}

func formatRemoteDiagnostics(session model.RemoteSession, log string) string {
	if session.Provider != "xpra" || session.Xpra == nil {
		return log
	}
	options := session.Xpra
	yesNo := func(value *bool) string {
		if value != nil && *value {
			return "enabled"
		}
		return "disabled"
	}
	dpi := "automatic"
	if options.DPIMode != model.XpraDPIAuto {
		dpi = fmt.Sprintf("%d (%s)", options.DPI, options.DPIMode)
	}
	header := fmt.Sprintf("Provider: Xpra\n\nRendering:\n  profile: %s\n  encoding: %s\n  video: %s\n\nDisplay:\n  DPI: %s\n  dynamic resize: %s\n\nInput:\n  clipboard: %s\n\nLaunch:\n  after client connect: %s\n\nXpra menu:\n  mode: %s\n  toolbar position: %s", options.Profile, options.Encoding, yesNo(options.Video), dpi, yesNo(options.DynamicResize), yesNo(options.Clipboard), yesNo(options.LaunchAfterConnect), options.Menu, options.ToolbarPosition)
	if strings.TrimSpace(log) == "" {
		return header
	}
	return header + "\n\nXpra server output:\n" + log
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
	params, err := s.ctrl.Remote().ClientParams(id)
	if err != nil {
		remoteError(w, err)
		return
	}
	ticket, err := secureTicket()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.remoteTicketMu.Lock()
	s.remoteTickets[ticket] = remoteClientTicket{SessionID: id, ClientParams: params, Expires: time.Now().Add(4 * time.Hour)}
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
	clientTicket, ok := s.remoteTicket(ticket, id)
	if !ok {
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
		redirect := prefix + clientPath + clean
		if len(clientTicket.ClientParams) > 0 {
			redirect += "?" + encodeClientParams(clientTicket.ClientParams)
		}
		http.Redirect(w, r, redirect, http.StatusFound)
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
		if r.PathValue("path") == "" {
			// The root HTML page is the only place html5 reads these settings.
			// Do not let a later user-edited iframe URL replace the session snapshot.
			request.URL.RawQuery = encodeClientParams(clientTicket.ClientParams)
		}
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
	}
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, proxyErr error) {
		writeError(response, http.StatusBadGateway, fmt.Errorf("remote session transport failed: %w", proxyErr))
	}
	proxy.ServeHTTP(w, r)
}

func encodeClientParams(params map[string]string) string {
	query := url.Values{}
	for key, value := range params {
		query.Set(key, value)
	}
	return query.Encode()
}
func (s *Server) remoteTicket(ticket, id string) (remoteClientTicket, bool) {
	if ticket == "" {
		return remoteClientTicket{}, false
	}
	s.remoteTicketMu.Lock()
	defer s.remoteTicketMu.Unlock()
	item, ok := s.remoteTickets[ticket]
	if !ok || item.SessionID != id || time.Now().After(item.Expires) {
		delete(s.remoteTickets, ticket)
		return remoteClientTicket{}, false
	}
	return item, true
}
func (s *Server) handleRemoteSessionPage(w http.ResponseWriter, r *http.Request) {
	base := s.basePath
	if base == "/" {
		base = ""
	}
	http.Redirect(w, r, base+"/?remoteSession="+url.QueryEscape(r.PathValue("id")), http.StatusFound)
}
