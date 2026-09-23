package plugins

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const MaxPackageSize = 64 << 20

// InstallPackage validates an immutable plugin archive and atomically installs
// it below root/id/version. Existing installations are never overwritten.
func InstallPackage(root, packagePath, expectedSHA256 string) (Manifest, error) {
	info, err := os.Stat(packagePath)
	if err != nil {
		return Manifest{}, err
	}
	if info.Size() > MaxPackageSize {
		return Manifest{}, fmt.Errorf("plugin package exceeds %d bytes", MaxPackageSize)
	}
	file, err := os.Open(packagePath)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, MaxPackageSize+1)); err != nil {
		return Manifest{}, err
	}
	if expectedSHA256 != "" && !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), strings.TrimSpace(expectedSHA256)) {
		return Manifest{}, fmt.Errorf("plugin package checksum mismatch")
	}
	archive, err := zip.OpenReader(packagePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open plugin package: %w", err)
	}
	defer archive.Close()
	manifest, err := manifestFromArchive(archive.File)
	if err != nil {
		return Manifest{}, err
	}
	tempRoot, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tempRoot)
	installRoot := filepath.Join(tempRoot, manifest.ID, manifest.Version)
	for _, entry := range archive.File {
		if strings.HasSuffix(entry.Name, "/") {
			if !safePackagePath(strings.TrimSuffix(entry.Name, "/")) {
				return Manifest{}, fmt.Errorf("unsafe plugin archive path %q", entry.Name)
			}
			continue
		}
		if !safePackagePath(entry.Name) {
			return Manifest{}, fmt.Errorf("unsafe plugin archive path %q", entry.Name)
		}
		destination := filepath.Join(installRoot, filepath.FromSlash(entry.Name))
		if err := ensureWithin(installRoot, destination); err != nil {
			return Manifest{}, err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return Manifest{}, err
		}
		input, err := entry.Open()
		if err != nil {
			return Manifest{}, err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, err = io.Copy(output, io.LimitReader(input, MaxPackageSize+1))
			closeErr := output.Close()
			if err == nil {
				err = closeErr
			}
		}
		_ = input.Close()
		if err != nil {
			return Manifest{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, manifest.ID)), 0o755); err != nil {
		return Manifest{}, err
	}
	finalRoot := filepath.Join(root, manifest.ID, manifest.Version)
	if _, err := os.Stat(finalRoot); err == nil {
		return manifest, nil
	} else if !os.IsNotExist(err) {
		return Manifest{}, err
	}
	if err := os.Rename(filepath.Join(tempRoot, manifest.ID), filepath.Join(root, manifest.ID)); err != nil {
		return Manifest{}, fmt.Errorf("activate plugin package: %w", err)
	}
	return manifest, nil
}

func manifestFromArchive(entries []*zip.File) (Manifest, error) {
	for _, entry := range entries {
		if entry.Name != "plugin.yaml" {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			return Manifest{}, err
		}
		var manifest Manifest
		decoder := yaml.NewDecoder(io.LimitReader(reader, 1<<20))
		decoder.KnownFields(true)
		err = decoder.Decode(&manifest)
		_ = reader.Close()
		if err != nil {
			return Manifest{}, fmt.Errorf("decode plugin manifest: %w", err)
		}
		if err := manifest.Validate(); err != nil {
			return Manifest{}, err
		}
		return manifest, nil
	}
	return Manifest{}, fmt.Errorf("plugin package does not contain plugin.yaml")
}

func ensureWithin(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("plugin archive path escapes installation root")
	}
	return nil
}
