package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/plugins"
)

// Opt in with the same Playwright environment used by the gateway browser tests.
// This exercises real browser sessions, plugin assets, and settings RPCs.
func TestUIRealBrowser(t *testing.T) {
	if os.Getenv("RUNPILOT_PLAYWRIGHT_MODULE") == "" || runtime.GOOS != "linux" {
		t.Skip("needs Linux and RUNPILOT_PLAYWRIGHT_MODULE")
	}
	t.Setenv("RUNPILOT_DATA_ROOT_FILE", filepath.Join(t.TempDir(), "deployment", "data-root"))
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(plugins.Catalog{SchemaVersion: 1, Plugins: []plugins.CatalogEntry{}})
	}))
	defer registry.Close()
	for _, base := range []string{"/", "/tenant/pilot"} {
		t.Run(base, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(t.TempDir(), "terminal.rpplugin")
			if err := os.WriteFile(archive, terminalPluginPackage(t), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := plugins.InstallPackage(filepath.Join(dir, "plugins"), archive, ""); err != nil {
				t.Fatal(err)
			}
			store, err := config.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Update(func(cfg *model.Config) error {
				enabled := true
				cfg.Plugins = map[string]model.PluginSettings{"terminal": {Enabled: &enabled}}
				cfg.PluginRegistry.URL = registry.URL
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			ctrl, err := core.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer ctrl.Close()
			hash, err := hashPassword("browser review password")
			if err != nil {
				t.Fatal(err)
			}
			if err := ctrl.ConfigureAdmin("admin", hash); err != nil {
				t.Fatal(err)
			}
			s, err := New(ctrl, base)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(s.Handler())
			defer server.Close()
			cmd := exec.Command("node", "internal/web/static/ui.test.cjs")
			cmd.Dir = filepath.Join("..", "..")
			cmd.Env = append(os.Environ(), "RUNPILOT_TEST_URL="+server.URL, "RUNPILOT_TEST_BASE="+base)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("UI browser: %v\n%s", err, output)
			} else {
				t.Log(string(output))
			}
		})
	}
}
