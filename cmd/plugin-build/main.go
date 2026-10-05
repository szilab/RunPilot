package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
	"gopkg.in/yaml.v3"
)

func main() {
	manifestOnly := flag.Bool("manifest", false, "validate and print manifest JSON")
	tag := flag.String("tag", "", "require exact release tag matching manifest")
	outPath := flag.String("out", "dist", "output file or directory")
	flag.Parse()
	if flag.NArg() == 0 {
		fail("plugin source directory is required")
	}
	manifest := readManifest(flag.Arg(0))
	if err := manifest.Validate(); err != nil {
		fail(err.Error())
	}
	if *tag != "" && *tag != plugins.ReleaseTag(manifest.ID, manifest.Version) {
		fail("tag version does not match plugin.yaml")
	}
	if *manifestOnly {
		if err := json.NewEncoder(os.Stdout).Encode(manifest); err != nil {
			fail(err.Error())
		}
		return
	}
	if err := buildPackage(flag.Arg(0), *outPath); err != nil {
		fail(err.Error())
	}
}

func buildPackage(source, output string) error {
	manifest := readManifest(source)
	if err := manifest.Validate(); err != nil {
		return err
	}
	if err := plugins.ValidatePackageAssets(manifest, func(asset string) bool {
		info, err := os.Stat(filepath.Join(source, filepath.FromSlash(asset)))
		return err == nil && !info.IsDir()
	}); err != nil {
		return err
	}
	if filepath.Ext(output) != ".rpplugin" {
		output = filepath.Join(output, manifest.ID+"-"+manifest.Version+".rpplugin")
	}
	sourceAbs, _ := filepath.Abs(source)
	outputAbs, _ := filepath.Abs(output)
	if relative, err := filepath.Rel(sourceAbs, outputAbs); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("output must be outside plugin source directory")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	file, err := os.Create(output)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(file)
	var paths []string
	err = filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if filepath.Base(path) == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular package asset %s", path)
		}
		if filepath.Base(path) == ".runpilot-source.json" {
			return fmt.Errorf("reserved package metadata path")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err == nil {
		sort.Strings(paths)
		for _, relative := range paths {
			input, openErr := os.Open(filepath.Join(source, filepath.FromSlash(relative)))
			if openErr != nil {
				err = openErr
				break
			}
			header := &zip.FileHeader{Name: relative, Method: zip.Deflate, Modified: time.Unix(0, 0).UTC()}
			writer, createErr := archive.CreateHeader(header)
			if createErr == nil {
				_, createErr = io.Copy(writer, input)
			}
			_ = input.Close()
			if createErr != nil {
				err = createErr
				break
			}
		}
	}
	closeErr := archive.Close()
	fileCloseErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if fileCloseErr != nil {
		return fileCloseErr
	}
	if _, err := plugins.InspectPackage(output); err != nil {
		return err
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	fmt.Printf("%s  %s\n", hex.EncodeToString(hash[:]), output)
	return nil
}

func readManifest(source string) plugins.Manifest {
	data, err := os.ReadFile(filepath.Join(source, "plugin.yaml"))
	if err != nil {
		fail(err.Error())
	}
	var manifest plugins.Manifest
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		fail(err.Error())
	}
	return manifest
}

func fail(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
