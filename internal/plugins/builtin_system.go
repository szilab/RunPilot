package plugins

import (
	"archive/zip"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// CleanupObsoleteSystemFixture removes the automatically installed ABI-v1
// System fixture used by older develop builds. The fixture predates backend
// and frontend contract requirements, so it cannot be discovered by current
// releases. Only its immutable package directory is removed; plugin data is
// stored elsewhere and remains untouched.
func CleanupObsoleteSystemFixture(root string) (bool, error) {
	dir := filepath.Join(root, "system", "1.0.0")
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	manifestPath := filepath.Join(dir, "plugin.yaml")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !manifestInfo.Mode().IsRegular() {
		return false, nil
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return false, err
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	decodeErr := decoder.Decode(&manifest)
	closeErr := file.Close()
	if decodeErr != nil {
		return false, nil
	}
	if closeErr != nil {
		return false, closeErr
	}
	if manifest.APIVersion != PluginAPIVersion || manifest.ID != "system" || manifest.Name != "System" || manifest.Description != "Host status and resource usage" || manifest.Version != "1.0.0" || manifest.Requires.RunPilotAPI != PluginABIVersion || manifest.Requires.Backend != "" || manifest.Requires.Frontend != "" || manifest.Backend == nil || manifest.Backend.Module != "backend/plugin.wasm" || manifest.Frontend == nil || manifest.Frontend.Module != "web/plugin.js" || manifest.Frontend.Stylesheet != "web/plugin.css" {
		return false, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, fmt.Errorf("remove obsolete System fixture: %w", err)
	}
	return true, nil
}

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
