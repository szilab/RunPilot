package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/dockercompose"
	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/websocketsecure"
)

// Legacy Docker REST handlers. They are not mounted by Handler (see
// legacy_routes.go) and remain until the Storage migration lets the legacy
// Docker manager be removed.

func (s *Server) dockerSupported(w http.ResponseWriter) bool {
	if !platform.CurrentCapabilities().DockerCompose {
		writeError(w, http.StatusNotImplemented, dockercompose.ErrUnsupported)
		return false
	}
	return true
}

func (s *Server) handleDockerRuntime(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.docker.Runtime(r.Context()))
}

func (s *Server) handleDockerProjects(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	runtime, projects, err := s.docker.List(r.Context())
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime": runtime, "projects": projects})
}

func (s *Server) handleDockerVolumes(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	runtime, volumes, err := s.docker.ListVolumes(r.Context())
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime": runtime, "volumes": volumes})
}

func (s *Server) handleDockerCreateVolume(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	v, err := s.docker.CreateVolume(r.Context(), body.Name)
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleDockerDeleteVolume(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.DeleteVolume(r.Context(), r.PathValue("name")); err != nil {
		dockerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDockerNetworks(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	runtime, networks, err := s.docker.ListNetworks(r.Context())
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime": runtime, "networks": networks})
}

func (s *Server) handleDockerCreateNetwork(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	network, err := s.docker.CreateNetwork(r.Context(), body.Name)
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, network)
}

func (s *Server) handleDockerDeleteNetwork(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.DeleteNetwork(r.Context(), r.PathValue("name")); err != nil {
		dockerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDockerContainerAction(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.ContainerAction(r.Context(), r.PathValue("id"), r.PathValue("action")); err != nil {
		dockerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDockerContainerLogs(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	logs, err := s.docker.ContainerLogs(r.Context(), r.PathValue("id"))
	if err != nil {
		dockerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(logs))
}

func (s *Server) handleDockerContainerAttachTicket(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.TerminalContainer(r.Context(), r.PathValue("id")); err != nil {
		dockerError(w, err)
		return
	}
	var request struct{ Cols, Rows uint16 }
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Cols == 0 {
		request.Cols = 80
	}
	if request.Rows == 0 {
		request.Rows = 24
	}
	if request.Cols > 500 || request.Rows > 300 {
		writeError(w, http.StatusBadRequest, errors.New("terminal dimensions are too large"))
		return
	}
	ticket, err := secureTicket()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.dockerAttachMu.Lock()
	sessionID, _, _ := s.session(r)
	s.dockerAttachTickets[ticket] = dockerAttachTicket{DockerExecID: r.PathValue("id"), SessionID: sessionID, Cols: request.Cols, Rows: request.Rows, Expires: time.Now().Add(time.Minute)}
	s.dockerAttachMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"ticket": ticket})
}

func (s *Server) handleDockerCreateProject(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	p, err := s.docker.Create(body.Name)
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleDockerAction(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.Action(r.Context(), r.PathValue("name"), r.PathValue("action")); err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) handleDockerDeleteProject(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	if err := s.docker.Delete(r.Context(), r.PathValue("name")); err != nil {
		dockerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDockerReadFile(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	content, err := s.docker.ReadFile(r.PathValue("name"), r.PathValue("kind"))
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

func (s *Server) handleDockerWriteFile(w http.ResponseWriter, r *http.Request) {
	if !s.dockerSupported(w) {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.docker.WriteFile(r.PathValue("name"), r.PathValue("kind"), body.Content); err != nil {
		dockerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func dockerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dockercompose.ErrInvalidName), strings.Contains(err.Error(), "invalid Compose action"), strings.Contains(err.Error(), "invalid managed file"):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, dockercompose.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, dockercompose.ErrReadOnly):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, dockercompose.ErrBusy), errors.Is(err, dockercompose.ErrProjectNotDown):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, dockercompose.ErrRuntimeUnavailable):
		writeError(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, dockercompose.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, err)
	case errors.Is(err, dockercompose.ErrFileTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err)
	case errors.Is(err, dockercompose.ErrVolumeInUse), errors.Is(err, dockercompose.ErrNetworkInUse), errors.Is(err, dockercompose.ErrProtectedNetwork), errors.Is(err, dockercompose.ErrNetworkExists):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, dockercompose.ErrContainerRunning):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, dockercompose.ErrContainerNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, dockercompose.ErrContainerReadOnly):
		writeError(w, http.StatusForbidden, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func (s *Server) handleDockerAttach(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("ticket") == "" {
		http.Error(w, "terminal ticket required", http.StatusUnauthorized)
		return
	}
	// coder/websocket validates Origin against the request Host by default.
	// Do not set InsecureSkipVerify or broad origin patterns here.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(websocketsecure.MaxEncryptedMessageSize)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sessionID, authSession, hasSession := s.session(r)
	if !hasSession {
		_ = conn.Close(websocket.StatusPolicyViolation, "session expired")
		return
	}
	ctx, cancel = context.WithDeadline(ctx, authSession.expires)
	defer cancel()
	unregister := s.registerSessionConnection(sessionID, func() { _ = conn.Close(websocket.StatusPolicyViolation, "session revoked") })
	defer unregister()
	ticket, ok := s.consumeDockerAttachTicket(r.URL.Query().Get("ticket"), sessionID)
	if !ok || ticket.DockerExecID == "" {
		_ = conn.Close(websocket.StatusPolicyViolation, "terminal connection expired or already used")
		return
	}
	secure, err := websocketsecure.ServerHandshake(ctx, conn, requestWebSocketPayloadMode(r, s.ctrl.Snapshot().Server.WebSocketPayloadMode))
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "secure WebSocket negotiation failed")
		return
	}
	session, err := s.dockerTerminal.StartCommand("Container terminal", "docker", []string{"exec", "-i", "-t", ticket.DockerExecID, "/bin/sh"}, ticket.Cols, ticket.Rows)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, err.Error())
		return
	}
	defer session.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 32*1024)
		for {
			n, readErr := session.Read(buffer)
			if n > 0 {
				if writeErr := secure.Write(ctx, websocket.MessageBinary, buffer[:n]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	for {
		kind, data, readErr := secure.Read(ctx)
		if readErr != nil {
			break
		}
		switch kind {
		case websocket.MessageBinary:
			if _, err := session.Write(data); err != nil {
				return
			}
		case websocket.MessageText:
			var control struct {
				Type       string `json:"type"`
				Cols, Rows uint16
			}
			if json.Unmarshal(data, &control) != nil || control.Type != "resize" || session.Resize(control.Cols, control.Rows) != nil {
				_ = conn.Close(websocket.StatusPolicyViolation, "invalid terminal control message")
				return
			}
		}
	}
	cancel()
	_ = session.Close()
	<-done
}

func (s *Server) consumeDockerAttachTicket(value, sessionID string) (dockerAttachTicket, bool) {
	s.dockerAttachMu.Lock()
	defer s.dockerAttachMu.Unlock()
	ticket, ok := s.dockerAttachTickets[value]
	if ok {
		delete(s.dockerAttachTickets, value)
	}
	if !ok || ticket.SessionID != sessionID || time.Now().After(ticket.Expires) {
		return dockerAttachTicket{}, false
	}
	return ticket, true
}
