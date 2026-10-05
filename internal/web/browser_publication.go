package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/szilab/RunPilot/internal/browserpath"
)

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
		if r.URL.Query().Get("publication") != p.ID {
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
	base := browserpath.Escape(strings.TrimSuffix(s.basePath, "/"))
	config, _ := json.Marshal(map[string]string{"owner": p.Owner, "publicationId": p.ID, "publicPrefix": prefix, "basePath": base, "assets": base + "/plugins/" + p.Owner + "/web/"})
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
