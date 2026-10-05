package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/szilab/RunPilot/internal/remote"
)

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
