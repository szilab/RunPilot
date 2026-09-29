package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func catalogFixture() Catalog {
	return Catalog{SchemaVersion: 1, Plugins: []CatalogEntry{{ID: "test", Name: "Test", Latest: "2.0.0", Versions: []CatalogVersion{
		{Version: "1.9.0", Requires: Requires{Frontend: ">=1.0.0 <2.0.0"}, URL: "https://example.org/test-1.9.0.rpplugin", SHA256: strings.Repeat("a", 64)},
		{Version: "1.10.0", Requires: Requires{Frontend: ">=1.0.0 <2.0.0"}, URL: "https://example.org/test-1.10.0.rpplugin", SHA256: strings.Repeat("b", 64)},
		{Version: "2.0.0", Requires: Requires{Frontend: ">=2.0.0 <3.0.0"}, URL: "https://example.org/test-2.0.0.rpplugin", SHA256: strings.Repeat("c", 64)},
	}}}}
}
func TestContractConstraints(t *testing.T) {
	for _, tc := range []struct {
		constraint, host string
		ok               bool
	}{
		{"1.0.0", "1.0.0", true}, {">=1.0.0", "1.2.0", true}, {">=1.0.0 <2.0.0", "2.0.0", false},
		{">=1.0.0", "0.9.0", false}, {"nonsense", "1.0.0", false}, {"", "1.0.0", false},
		{">=1.0.0 <2.0.0", "1.1.0-beta.1", false}, {">=1.0.0-0 <2.0.0", "1.1.0-beta.1", true},
	} {
		t.Run(tc.constraint+tc.host, func(t *testing.T) {
			err := CheckConstraint(tc.constraint, tc.host)
			if (err == nil) != tc.ok {
				t.Fatalf("%v", err)
			}
		})
	}
	for _, v := range []string{"1", "v1.0.0", "01.0.0", "../1.0.0"} {
		if ValidateVersion(v) == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	if CompareVersions("1.10.0", "1.9.0") <= 0 || CompareVersions("1.0.0+a", "1.0.0+b") != 0 {
		t.Fatal("incorrect SemVer order")
	}
}
func TestParseCatalog(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Catalog)
		bad    bool
	}{
		{"valid", func(*Catalog) {}, false},
		{"schema", func(c *Catalog) { c.SchemaVersion = 2 }, true},
		{"duplicate plugin", func(c *Catalog) { c.Plugins = append(c.Plugins, c.Plugins[0]) }, true},
		{"duplicate version", func(c *Catalog) { c.Plugins[0].Versions = append(c.Plugins[0].Versions, c.Plugins[0].Versions[0]) }, true},
		{"URL", func(c *Catalog) { c.Plugins[0].Versions[0].URL = "file:///tmp/x" }, true},
		{"checksum", func(c *Catalog) { c.Plugins[0].Versions[0].SHA256 = strings.Repeat("z", 64) }, true},
		{"version", func(c *Catalog) { c.Plugins[0].Versions[0].Version = "1" }, true},
		{"constraint", func(c *Catalog) { c.Plugins[0].Versions[0].Requires.Backend = "bad" }, true},
		{"latest", func(c *Catalog) { c.Plugins[0].Latest = "1.9.0" }, true},
		{"platform", func(c *Catalog) { c.Plugins[0].Versions[0].Platforms = []string{"other"} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := catalogFixture()
			tc.mutate(&c)
			data, _ := json.Marshal(c)
			_, err := ParseCatalog(data)
			if (err != nil) != tc.bad {
				t.Fatalf("%v", err)
			}
		})
	}
	for _, data := range []string{`{}`, `{"schemaVersion":1,"plugins":[] ,"extra":1}`, `{"schemaVersion":1,"plugins":[]} {}`, `{`} {
		if _, err := ParseCatalog([]byte(data)); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}
func TestCatalogCompatibilityAndUpdates(t *testing.T) {
	c := catalogFixture()
	for _, tc := range []struct {
		name, version string
		source        *InstallSource
		update        bool
	}{
		{"new compatible", "1.9.0", &InstallSource{Registry: "registry"}, true},
		{"new incompatible only", "1.10.0", &InstallSource{Registry: "registry"}, false},
		{"current", "2.0.0", &InstallSource{Registry: "registry"}, false},
		{"manual", "1.0.0", nil, false},
		{"different registry", "1.0.0", &InstallSource{Registry: "other"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := c.Statuses("linux", []Status{{Manifest: Manifest{ID: "test", Version: tc.version}, Source: tc.source}}, "registry")[0]
			if s.Latest != "2.0.0" || s.LatestCompatible != "1.10.0" || !strings.Contains(s.Incompatibility, "frontend") || s.UpdateAvailable != tc.update {
				t.Fatalf("%+v", s)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*CatalogVersion)
		reason string
	}{
		{"platform", func(v *CatalogVersion) { v.Platforms = []string{"windows"} }, "platform"},
		{"backend", func(v *CatalogVersion) { v.Requires.Backend = ">=2.0.0" }, "backend"},
		{"frontend", func(v *CatalogVersion) { v.Requires.Frontend = ">=2.0.0" }, "frontend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := catalogFixture()
			p := &c.Plugins[0]
			p.Versions = p.Versions[:1]
			p.Latest = p.Versions[0].Version
			tc.change(&p.Versions[0])
			s := c.Statuses("linux", nil, "")[0]
			if s.LatestCompatible != "" || !strings.Contains(s.Incompatibility, tc.reason) {
				t.Fatalf("%+v", s)
			}
		})
	}
}
func TestFetchCatalog(t *testing.T) {
	c := catalogFixture()
	data, _ := json.Marshal(c)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	if _, err := FetchCatalog(context.Background(), server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Repeat(" ", MaxCatalogSize+1))
	if _, err := FetchCatalog(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("accepted oversized catalog")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchCatalog(ctx, server.Client(), server.URL); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestDownloadAndInstall(t *testing.T) {
	for _, name := range []string{"success", "checksum", "invalid package", "missing asset", "incompatible manifest", "wrong identity", "wrong contract metadata", "preflight"} {
		t.Run(name, func(t *testing.T) {
			manifest := validManifest
			if name == "incompatible manifest" {
				manifest = strings.ReplaceAll(manifest, ">=1.0.0 <2.0.0", ">=2.0.0 <3.0.0")
			}
			if name == "wrong identity" {
				manifest = strings.ReplaceAll(manifest, "remote.xpra", "other")
			}
			if name == "wrong contract metadata" {
				manifest = strings.ReplaceAll(manifest, ">=1.0.0 <2.0.0", ">=1.0.0")
			}
			files := map[string]string{"plugin.yaml": manifest, "backend/plugin.wasm": "wasm"}
			if name == "missing asset" {
				delete(files, "backend/plugin.wasm")
			}
			path := writePackage(t, files)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if name == "invalid package" {
				data = []byte("not zip")
			}
			hash := sha256.Sum256(data)
			requested := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requested = true; _, _ = w.Write(data) }))
			defer server.Close()
			v := CatalogVersion{Version: "1.0.0", Requires: Requires{RunPilotAPI: 1, Backend: ">=1.0.0 <2.0.0", Frontend: ">=1.0.0 <2.0.0"}, URL: server.URL, SHA256: hex.EncodeToString(hash[:])}
			if name == "checksum" {
				v.SHA256 = strings.Repeat("0", 64)
			}
			if name == "preflight" {
				if runtime.GOOS == "linux" {
					v.Platforms = []string{"windows"}
				} else {
					v.Platforms = []string{"linux"}
				}
			}
			root := t.TempDir()
			entry := CatalogEntry{ID: "remote.xpra", Name: "Xpra"}
			_, err = DownloadAndInstall(context.Background(), server.Client(), "registry", root, entry, v)
			if name == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, "remote.xpra", "1.0.0", ".runpilot-source.json")); err != nil {
					t.Fatal(err)
				}
				manager := New(filepath.Dir(root))
				manager.root = root
				manager.Reload()
				if len(manager.Statuses()) != 1 || manager.Statuses()[0].Source.Registry != "registry" {
					t.Fatal("source not discovered")
				}
			} else {
				if err == nil {
					t.Fatal("invalid installation accepted")
				}
				entries, _ := os.ReadDir(root)
				if len(entries) != 0 {
					t.Fatalf("failed install changed root: %v", entries)
				}
				if name == "preflight" && requested {
					t.Fatal("downloaded incompatible catalog version")
				}
			}
		})
	}
}
func TestDownloadAndInstallRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "67108865")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	v := catalogFixture().Plugins[0].Versions[0]
	v.URL = server.URL
	_, err := DownloadAndInstall(context.Background(), server.Client(), "registry", t.TempDir(), CatalogEntry{ID: "test", Name: "Test"}, v)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("%v", err)
	}
}
