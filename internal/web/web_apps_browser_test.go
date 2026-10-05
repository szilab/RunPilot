package web

import (
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/browserpath"
)

func TestWebAppsRealBrowser(t *testing.T) {
	module := os.Getenv("RUNPILOT_PLAYWRIGHT_MODULE")
	if module == "" {
		t.Skip("optional browser integration needs RUNPILOT_PLAYWRIGHT_MODULE (playwright-core)")
	}
	for _, route := range []struct {
		base, upstream string
		tls            bool
	}{{"/", "/app", false}, {"/p", "/p/app", false}, {"/p", "/app", false}, {"/tenant/pilot", "/app", false}, {"/tenant space/应用", "/app", false}, {"/", "/app", true}} {
		t.Run(route.base+route.upstream, func(t *testing.T) {
			base := route.base
			upstreamBase := route.upstream
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, upstreamBase+"/") {
					http.NotFound(w, r)
					return
				}
				path := strings.TrimPrefix(r.URL.Path, upstreamBase+"/")
				// Jellyfin-style entry redirects must retain the initiating tab's tunnel.
				if path == "" || path == "entry" {
					destination := "entry"
					if path == "entry" {
						destination = "web/"
					}
					http.Redirect(w, r, upstreamBase+"/"+destination, http.StatusFound)
					return
				}
				switch strings.TrimPrefix(path, "web/") {
				case "":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, `<!doctype html><link rel="stylesheet" href="style.css"><img src="pixel.png"><script src="code.js"></script>`)
				case "code.js":
					w.Header().Set("Content-Type", "application/javascript")
					fmt.Fprint(w, `const ready=document.createElement("h1");ready.id="app-ready";ready.dataset.instance=crypto.randomUUID();ready.textContent="Browser rendered application";document.body.append(ready);`)
				case "style.css":
					w.Header().Set("Content-Type", "text/css")
					fmt.Fprint(w, `#app-ready { color: rgb(10, 20, 30); }`)
				case "pixel.png":
					w.Header().Set("Content-Type", "image/png")
					data, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==")
					_, _ = w.Write(data)
				case "login":
					_, _ = io.Copy(io.Discard, r.Body)
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "private-cookie", Path: upstreamBase, HttpOnly: true})
					fmt.Fprint(w, "ok")
				case "cookie":
					cookie, _ := r.Cookie("session")
					if cookie != nil {
						fmt.Fprint(w, cookie.Value)
					}
				case "range":
					w.Header().Set("Accept-Ranges", "bytes")
					http.ServeContent(w, r, "media", time.Time{}, strings.NewReader("0123456789"))
				case "gzip":
					w.Header().Set("Content-Encoding", "gzip")
					w.Header().Set("Content-Type", "text/plain")
					writer := gzip.NewWriter(w)
					_, _ = io.WriteString(writer, "compressed response")
					_ = writer.Close()
				case "redirect":
					w.Header().Set("Location", upstreamBase+"/cookie")
					w.WriteHeader(302)
				case "echo":
					_ = http.NewResponseController(w).EnableFullDuplex()
					_, _ = io.Copy(w, r.Body)
				case "large":
					w.Header().Set("Content-Type", "application/octet-stream")
					for range 1280 {
						if _, err := w.Write(make([]byte, 16384)); err != nil {
							return
						}
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			c := webAppsController(t, base)
			openWebApp(t, c, upstream.URL, upstreamBase)
			s, _ := New(c, base)
			var targetHTTP atomic.Int64
			var rootDocuments atomic.Int64
			handler := s.Handler()
			basePrefix := strings.TrimSuffix(base, "/")
			legacy := base == "/" && !route.tls
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if legacy && r.URL.Path == "/app/__runpilot__/sw.js" && r.URL.Query().Get("publication") == "legacy" {
					w.Header().Set("Content-Type", "application/javascript")
					w.Header().Set("Service-Worker-Allowed", "/app/")
					fmt.Fprint(w, `self.addEventListener("install",e=>e.waitUntil(self.skipWaiting()));self.addEventListener("activate",e=>e.waitUntil(self.clients.claim()));`)
					return
				}
				if strings.HasPrefix(r.URL.Path, basePrefix+"/app/") || strings.HasPrefix(r.URL.Path, basePrefix+"/router/") || strings.HasPrefix(r.URL.Path, basePrefix+"/crud/") {
					targetHTTP.Add(1)
					http.Error(w, "target path must never reach the HTTP listener", http.StatusForbidden)
					return
				}
				if r.Header.Get("Sec-Fetch-Dest") == "document" && r.URL.Path == basePrefix+"/" {
					rootDocuments.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			if route.tls {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			cmd := exec.Command("node", "plugins/web-apps/web/browser.test.cjs")
			cmd.Dir = filepath.Join("..", "..")
			cmd.Env = append(os.Environ(), "RUNPILOT_TEST_URL="+server.URL, "RUNPILOT_TEST_UPSTREAM="+upstream.URL, "RUNPILOT_TEST_UPSTREAM_BASE="+upstreamBase, "RUNPILOT_TEST_TOKEN="+c.Snapshot().Server.Token, "RUNPILOT_TEST_BASE="+browserpath.Escape(strings.TrimSuffix(base, "/")), fmt.Sprintf("RUNPILOT_TEST_TLS=%t", route.tls), fmt.Sprintf("RUNPILOT_TEST_LEGACY=%t", legacy))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("browser integration: %v\n%s", err, out)
			} else {
				t.Log(string(out))
				if targetHTTP.Load() != 0 {
					t.Fatalf("%d application HTTP requests bypassed the worker", targetHTTP.Load())
				}
				if rootDocuments.Load() < 2 {
					t.Fatalf("root document launches missing: %d", rootDocuments.Load())
				}
			}
		})
	}
}
