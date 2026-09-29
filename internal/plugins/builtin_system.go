package plugins

import (
	"archive/zip"
	_ "embed"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// EnsureReferenceSystem installs the nonpublic technical fixture for tests
// through the same archive validation and atomic installer as any plugin.
// Production startup does not call it.
func EnsureReferenceSystem(root string) error {
	var manifest Manifest
	if err := yaml.Unmarshal(referenceSystemManifest, &manifest); err != nil {
		return err
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, manifest.ID, manifest.Version, "plugin.yaml")); err == nil {
		return nil
	}
	file, err := os.CreateTemp(root, ".system-reference-*.rpplugin")
	if err != nil {
		return err
	}
	archivePath := file.Name()
	defer os.Remove(archivePath)
	writer := zip.NewWriter(file)
	assets := map[string]string{
		"plugin.yaml":    string(referenceSystemManifest),
		"web/plugin.js":  string(referenceSystemJavaScript),
		"web/plugin.css": string(referenceSystemStylesheet),
	}
	wasm := referenceSystemWASM
	for name, content := range assets {
		entry, err := writer.Create(name)
		if err != nil {
			return err
		}
		if _, err = entry.Write([]byte(content)); err != nil {
			return err
		}
	}
	entry, err := writer.Create("backend/plugin.wasm")
	if err != nil {
		return err
	}
	if _, err = entry.Write(wasm); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	_, err = InstallPackage(root, archivePath, "")
	return err
}

// referenceSystemWASM is built from plugins/system/backend, checked in so end-user
// installs never need TinyGo.  Build/release verifies this generated artifact.
//
//go:embed system_reference.wasm
var referenceSystemWASM []byte

//go:embed system_reference/plugin.yaml
var referenceSystemManifest []byte

//go:embed system_reference/web/plugin.js
var referenceSystemJavaScript []byte

//go:embed system_reference/web/plugin.css
var referenceSystemStylesheet []byte
