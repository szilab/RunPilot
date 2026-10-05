package web

import (
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"github.com/szilab/RunPilot/internal/browserpath"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebAppsRealBrowser(t *testing.T) {
	module := os.Getenv("RUNPILOT_PLAYWRIGHT_MODULE")
	if module == "" {
		t.Skip("optional browser integration needs RUNPILOT_PLAYWRIGHT_MODULE (playwright-core)")
	}
	for _, route := range []struct{ base, upstream string }{{"/", "/app"}, {"/p", "/p/app"}, {"/p", "/app"}, {"/tenant/pilot", "/app"}, {"/tenant space/应用", "/app"}} {
		t.Run(route.base+route.upstream, func(t *testing.T) {
			base := route.base
			upstreamBase := route.upstream
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, upstreamBase+"/") {
					http.NotFound(w, r)
					return
				}
				switch strings.TrimPrefix(r.URL.Path, upstreamBase+"/") {
				case "":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, `<!doctype html><link rel="stylesheet" href="style.css"><img src="pixel.png"><script src="code.js"></script>`)
				case "code.js":
					w.Header().Set("Content-Type", "application/javascript")
					fmt.Fprint(w, `const ready=document.createElement("h1");ready.id="app-ready";ready.textContent="Browser rendered application";document.body.append(ready);`)
				case "style.css":
					w.Header().Set("Content-Type", "text/css")
					fmt.Fprint(w, `#app-ready { color: rgb(10, 20, 30); }`)
				case "pixel.png":
					w.Header().Set("Content-Type", "image/png")
					data, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j4N8AAAAASUVORK5CYII=")
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
			server := httptest.NewServer(s.Handler())
			defer server.Close()
			cmd := exec.Command("node", "plugins/web-apps/web/browser.test.cjs")
			cmd.Dir = filepath.Join("..", "..")
			cmd.Env = append(os.Environ(), "RUNPILOT_TEST_URL="+server.URL, "RUNPILOT_TEST_UPSTREAM="+upstream.URL, "RUNPILOT_TEST_UPSTREAM_BASE="+upstreamBase, "RUNPILOT_TEST_BASE="+browserpath.Escape(strings.TrimSuffix(base, "/")), "RUNPILOT_TEST_TOKEN="+c.Snapshot().Server.Token)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("browser integration: %v\n%s", err, out)
			} else {
				t.Log(string(out))
			}
		})
	}
}
