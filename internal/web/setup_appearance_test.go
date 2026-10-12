package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/core"
)

func TestSetupAppearanceAssetsRespectBasePathAndSetupGate(t *testing.T) {
	t.Setenv("RUNPILOT_DATA_ROOT_FILE", filepath.Join(t.TempDir(), "deployment", "data-root"))
	for _, base := range []string{"/", "/tenant/pilot"} {
		t.Run(base, func(t *testing.T) {
			ctrl, err := core.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer ctrl.Close()
			s, err := New(ctrl, base)
			if err != nil {
				t.Fatal(err)
			}
			s.SetRestartHandler(func() {})
			prefix := strings.TrimSuffix(base, "/")
			for _, asset := range []string{"styles.css", "workspace.css", "runpilot-mark.svg", "workspace-alpine.png", "fonts/InterVariable.woff2"} {
				response := httptest.NewRecorder()
				s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, prefix+"/"+asset, nil))
				if response.Code != http.StatusOK || response.Body.Len() == 0 {
					t.Fatalf("setup asset %s: status %d", asset, response.Code)
				}
			}
			for _, page := range []string{"/", "/setup"} {
				response := httptest.NewRecorder()
				s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, prefix+page, nil))
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `href="`+prefix+`/workspace.css"`) {
					t.Fatalf("setup page %s: status %d", page, response.Code)
				}
			}
			for _, restricted := range []string{"/app.js", "/api/v1/system", "/plugins/system/web/plugin.js"} {
				response := httptest.NewRecorder()
				s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, prefix+restricted, nil))
				if response.Code != http.StatusPreconditionRequired {
					t.Fatalf("setup gate allowed %s: status %d", restricted, response.Code)
				}
			}
		})
	}
}
