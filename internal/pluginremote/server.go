// Package pluginremote hosts the Remote capability inside a first-party plugin
// process. It is intentionally not a generic RPC system.
package pluginremote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/pluginapi"
	"github.com/szilab/RunPilot/internal/remote"
)

type Configurer interface{ Configure(model.GuacdConfig) }
type Server struct {
	provider     remote.Provider
	id, version  string
	capabilities []string
	configure    Configurer
	mu           sync.Mutex
	sessions     map[string]*session
}
type session struct {
	runtime remote.Runtime
	bridge  net.Listener
	state   string
	failure string
}

func New(provider remote.Provider, id, version string, capabilities []string, configure Configurer) *Server {
	return &Server{provider: provider, id: id, version: version, capabilities: capabilities, configure: configure, sessions: map[string]*session{}}
}

func (s *Server) Run(ctx context.Context) error {
	address, secret := os.Getenv("RUNPILOT_PLUGIN_LISTEN"), os.Getenv("RUNPILOT_PLUGIN_SECRET")
	if address == "" || secret == "" {
		return fmt.Errorf("plugin control listener configuration is missing")
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/info", s.info)
	mux.HandleFunc("/v1/health", s.health)
	mux.HandleFunc("/v1/capabilities", s.capabilitiesHandler)
	mux.HandleFunc("/v1/shutdown", s.shutdown)
	mux.HandleFunc("/v1/remote/status", s.status)
	mux.HandleFunc("/v1/remote/sessions", s.start)
	mux.HandleFunc("/v1/remote/sessions/", s.sessionHandler)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
	httpServer := &http.Server{Handler: h}
	go func() { <-ctx.Done(); _ = httpServer.Close() }()
	return httpServer.Serve(listener)
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		http.Error(w, "invalid JSON", 400)
		return false
	}
	return true
}
func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.NotFound(w, r)
		return
	}
	write(w, pluginapi.Info{ID: s.id, Version: s.version, ProtocolVersion: pluginapi.Version, Capabilities: s.capabilities})
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	write(w, pluginapi.Health{Healthy: true})
}
func (s *Server) capabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	write(w, map[string]any{"capabilities": s.capabilities})
}
func (s *Server) shutdown(w http.ResponseWriter, r *http.Request) {
	go s.close()
	write(w, map[string]bool{"ok": true})
}
func (s *Server) configureFor(config model.GuacdConfig) {
	if s.configure != nil {
		s.configure.Configure(config)
	}
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	var body pluginapi.RemoteStatusRequest
	if !decode(w, r, &body) {
		return
	}
	s.configureFor(body.Guacd)
	write(w, s.provider.Status(r.Context()))
}
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	var body pluginapi.RemoteStartRequest
	if !decode(w, r, &body) {
		return
	}
	s.configureFor(body.Guacd)
	runtime, err := s.provider.Start(r.Context(), remote.StartRequest{ID: body.ID, Target: body.Target, DataDir: body.DataDir})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	item := &session{runtime: runtime, state: "running"}
	if runtime.Dial != nil {
		bridge, err := s.bridge(runtime.Dial)
		if err != nil {
			_ = runtime.Stop(context.Background())
			http.Error(w, err.Error(), 500)
			return
		}
		item.bridge = bridge
	}
	s.mu.Lock()
	if _, exists := s.sessions[body.ID]; exists {
		s.mu.Unlock()
		if item.bridge != nil {
			_ = item.bridge.Close()
		}
		http.Error(w, "session exists", 409)
		return
	}
	s.sessions[body.ID] = item
	s.mu.Unlock()
	go s.watch(body.ID, runtime.Done)
	response := pluginapi.RemoteStartResponse{ClientKind: string(runtime.Client.Kind), ClientParams: merge(runtime.Client.Params, runtime.ClientParams), Endpoint: runtime.Endpoint, Message: runtime.Message}
	if item.bridge != nil {
		response.TransportEndpoint = item.bridge.Addr().String()
	}
	write(w, response)
}
func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
func (s *Server) bridge(dial func(context.Context) (net.Conn, error)) (net.Listener, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(in net.Conn) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				out, err := dial(ctx)
				if err != nil {
					_ = in.Close()
					return
				}
				go func() { _, _ = io.Copy(out, in); _ = out.Close() }()
				_, _ = io.Copy(in, out)
				_ = in.Close()
			}(conn)
		}
	}()
	return listener, nil
}
func (s *Server) sessionHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/remote/sessions/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, action := parts[0], parts[1]
	s.mu.Lock()
	item := s.sessions[id]
	s.mu.Unlock()
	if item == nil {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "stop":
		err := s.stop(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		write(w, map[string]bool{"ok": true})
	case "state":
		s.mu.Lock()
		state := pluginapi.SessionState{State: item.state, Failure: item.failure}
		s.mu.Unlock()
		write(w, state)
	case "probe":
		if item.runtime.Probe == nil {
			write(w, pluginapi.SessionProbe{})
			return
		}
		result, err := item.runtime.Probe(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		write(w, pluginapi.SessionProbe{WindowCount: result.WindowCount, Message: result.Message})
	case "log":
		value := ""
		if item.runtime.Log != nil {
			value = item.runtime.Log()
		}
		write(w, map[string]string{"log": value})
	default:
		http.NotFound(w, r)
	}
}
func (s *Server) stop(id string) error {
	s.mu.Lock()
	item := s.sessions[id]
	if item == nil {
		s.mu.Unlock()
		return nil
	}
	item.state = "stopping"
	s.mu.Unlock()
	if item.bridge != nil {
		_ = item.bridge.Close()
	}
	if item.runtime.Stop != nil {
		if err := item.runtime.Stop(context.Background()); err != nil {
			return err
		}
	}
	s.mu.Lock()
	item.state = "stopped"
	s.mu.Unlock()
	return nil
}
func (s *Server) watch(id string, done <-chan error) {
	if done == nil {
		return
	}
	err, ok := <-done
	if !ok {
		err = nil
	}
	s.mu.Lock()
	if item := s.sessions[id]; item != nil && item.state != "stopped" {
		if err != nil {
			item.state = "failed"
			item.failure = err.Error()
		} else {
			item.state = "stopped"
		}
		if item.bridge != nil {
			_ = item.bridge.Close()
		}
	}
	s.mu.Unlock()
}
func (s *Server) close() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.stop(id)
	}
}
