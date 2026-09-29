package plugins

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestValidate(t *testing.T) {
	manifest := Manifest{
		APIVersion: PluginAPIVersion,
		ID:         "remote.xpra",
		Name:       "Xpra",
		Version:    "1.0.0",
		Requires:   Requires{RunPilotAPI: PluginABIVersion},
		Backend:    &BackendManifest{Module: "backend/plugin.wasm"},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestManifestRejectsUnsupportedAPI(t *testing.T) {
	manifest := Manifest{APIVersion: PluginAPIVersion, ID: "test", Name: "Test", Version: "1", Requires: Requires{RunPilotAPI: PluginABIVersion + 1}, Backend: &BackendManifest{Module: "backend/plugin.wasm"}}
	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate() accepted an unsupported API")
	}
}

func TestManifestPlatforms(t *testing.T) {
	manifest := Manifest{APIVersion: PluginAPIVersion, ID: "test", Name: "Test", Version: "1", Requires: Requires{RunPilotAPI: PluginABIVersion}, Platforms: []string{"linux"}}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("linux manifest rejected: %v", err)
	}
	if !manifest.Compatible("linux") || manifest.Compatible("windows") {
		t.Fatal("platform compatibility is incorrect")
	}
	manifest.Platforms = []string{"windows"}
	if err := manifest.Validate(); err != nil || !manifest.Compatible("windows") || manifest.Compatible("linux") {
		t.Fatalf("windows platform compatibility is incorrect: %v", err)
	}
	manifest.Platforms = []string{"darwin"}
	if err := manifest.Validate(); err == nil {
		t.Fatal("unsupported platform accepted")
	}
}

func TestInstallPackageRejectsMissingDeclaredAssets(t *testing.T) {
	packagePath := writePackage(t, map[string]string{"plugin.yaml": validManifest})
	if _, err := InstallPackage(t.TempDir(), packagePath, ""); err == nil {
		t.Fatal("package with missing backend was accepted")
	}
	frontend := "apiVersion: runpilot.plugin/v1\nid: test.frontend\nname: Test\nversion: 1\nrequires:\n  runpilotApi: 1\nfrontend:\n  module: web/plugin.js\n  stylesheet: web/plugin.css\n"
	packagePath = writePackage(t, map[string]string{"plugin.yaml": frontend, "web/plugin.js": "export function activate() {}"})
	if _, err := InstallPackage(t.TempDir(), packagePath, ""); err == nil {
		t.Fatal("package with missing frontend stylesheet was accepted")
	}
}

func TestInstallPackageRejectsTraversal(t *testing.T) {
	packagePath := writePackage(t, map[string]string{
		"plugin.yaml": validManifest,
		"../escape":   "no",
	})
	if _, err := InstallPackage(t.TempDir(), packagePath, ""); err == nil {
		t.Fatal("InstallPackage() accepted traversal")
	}
}

func TestInstallPackageVerifiesChecksumAndInstallsAtomically(t *testing.T) {
	packagePath := writePackage(t, map[string]string{"plugin.yaml": validManifest, "backend/plugin.wasm": "wasm"})
	data, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	root := t.TempDir()
	manifest, err := InstallPackage(root, packagePath, hex.EncodeToString(hash[:]))
	if err != nil {
		t.Fatalf("InstallPackage() error = %v", err)
	}
	installed := filepath.Join(root, manifest.ID, manifest.Version, "backend", "plugin.wasm")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("installed artifact missing: %v", err)
	}
	if _, err := InstallPackage(root, packagePath, "bad"); err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
}

const validManifest = "apiVersion: runpilot.plugin/v1\nid: remote.xpra\nname: Xpra\nversion: 1.0.0\nrequires:\n  runpilotApi: 1\nbackend:\n  module: backend/plugin.wasm\n"

func writePackage(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.rpplugin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, content := range entries {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
