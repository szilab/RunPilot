package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/szilab/RunPilot/internal/browserpath"
	"github.com/szilab/RunPilot/internal/core"
)

func (s *Server) browserRuntimeConfig(runtime core.BrowserRuntime) map[string]string {
	base := browserpath.Escape(strings.TrimSuffix(s.basePath, "/"))
	root := base + "/__runpilot__/browser/" + runtime.ID + "/"
	return map[string]string{"owner": runtime.Owner, "runtimeId": runtime.ID, "basePath": base, "scope": base + "/", "assets": base + "/plugins/" + runtime.Owner + "/web/", "workerURL": root + "sw.js", "publicationsURL": root + "publications.json"}
}
func (s *Server) browserPublicationConfig(p core.BrowserPublication) map[string]string {
	base := browserpath.Escape(strings.TrimSuffix(s.basePath, "/"))
	config := map[string]string{"owner": p.Owner, "publicationId": p.ID, "publicPrefix": browserpath.Escape(browserpath.Join(s.basePath, p.MountPath)), "basePath": base, "assets": base + "/plugins/" + p.Owner + "/web/", "bootstrapHTML": base + "/plugins/" + p.Owner + "/" + p.Bootstrap}
	if runtime, _, ok := s.ctrl.BrowserRuntime(p.RuntimeID); ok {
		for key, value := range s.browserRuntimeConfig(runtime) {
			config[key] = value
		}
	}
	return config
}

// These framework routes serve only active package assets/public mount metadata.
// A runtime cannot choose its serving path, worker scope, or another owner's assets.
func (s *Server) serveBrowserRuntime(w http.ResponseWriter, r *http.Request) {
	runtime, publications, ok := s.ctrl.BrowserRuntime(r.PathValue("runtime"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !ok {
		http.Error(w, "browser runtime expired", http.StatusGone)
		return
	}
	asset := r.PathValue("asset")
	if asset == "publications.json" {
		configs := []map[string]string{}
		for _, p := range publications {
			configs = append(configs, s.browserPublicationConfig(p))
		}
		writeJSON(w, http.StatusOK, configs)
		return
	}
	var filename string
	var config map[string]string
	if asset == "sw.js" {
		filename = runtime.Worker
		config = s.browserRuntimeConfig(runtime)
	} else if asset == "bootstrap.js" {
		for _, p := range publications {
			if p.ID == r.URL.Query().Get("publication") {
				config = s.browserPublicationConfig(p)
				break
			}
		}
		if config == nil {
			http.Error(w, "publication expired", http.StatusGone)
			return
		}
		filename = runtime.Bootstrap
	} else {
		http.NotFound(w, r)
		return
	}
	path, err := s.ctrl.Plugins().AssetPath(runtime.Owner, filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 256<<10 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript")
	encoded, _ := json.Marshal(config)
	if asset == "sw.js" {
		w.Header().Set("Service-Worker-Allowed", config["scope"])
		_, _ = fmt.Fprintf(w, "self.RUNPILOT_BROWSER_RUNTIME=%s;\n", encoded)
	} else {
		_, _ = fmt.Fprintf(w, "globalThis.RUNPILOT_PUBLICATION=%s;\n", encoded)
	}
	_, _ = w.Write(data)
}

// serveBrowserPublication serves package-owned initialization only. It never
// performs an upstream HTTP request or serves application payloads.
func (s *Server) serveBrowserPublication(w http.ResponseWriter, r *http.Request) bool {
	p, ok := s.ctrl.BrowserPublication(r.URL.Path)
	if !ok {
		return false
	}
	prefix := browserpath.Escape(browserpath.Join(s.basePath, p.MountPath))
	if r.URL.Path == p.MountPath {
		target := prefix + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	asset := p.Bootstrap
	worker := r.URL.Path == p.MountPath+"/__runpilot__/sw.js"
	if worker {
		if p.RuntimeID != "" || r.URL.Query().Get("publication") != p.ID {
			http.Error(w, "publication expired", http.StatusGone)
			return true
		}
		asset = p.Worker
	} else if r.Method != "GET" || !strings.Contains(r.Header.Get("Accept"), "text/html") || strings.Contains(r.URL.Path, "/__runpilot__/") {
		http.Error(w, "application payload requires the encrypted browser tunnel", http.StatusServiceUnavailable)
		return true
	}
	filename, err := s.ctrl.Plugins().AssetPath(p.Owner, asset)
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	data, err := os.ReadFile(filename)
	if err != nil || len(data) > 256<<10 {
		http.NotFound(w, r)
		return true
	}
	config, _ := json.Marshal(s.browserPublicationConfig(p))
	if worker {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Service-Worker-Allowed", prefix+"/")
		_, _ = fmt.Fprintf(w, "self.RUNPILOT_PUBLICATION=%s;\n", config)
		_, _ = w.Write(data)
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'unsafe-inline'; connect-src 'self'; worker-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		_, _ = w.Write([]byte(strings.ReplaceAll(string(data), "__RUNPILOT_PUBLICATION__", string(config))))
	}
	return true
}
