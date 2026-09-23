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
	indexPath := flag.String("index", "", "write a catalog index instead of a package")
	outPath := flag.String("out", "dist", "output file or directory")
	flag.Parse()
	if flag.NArg() == 0 {
		fail("plugin source directory is required")
	}
	if *indexPath != "" {
		buildIndex(flag.Args(), *indexPath)
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
	if filepath.Ext(output) != ".rpplugin" {
		output = filepath.Join(output, manifest.ID+"-"+manifest.Version+".rpplugin")
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
		if info.IsDir() || filepath.Base(path) == ".git" {
			return nil
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
	data, err := os.ReadFile(output)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	fmt.Printf("%s  %s\n", hex.EncodeToString(hash[:]), output)
	return nil
}

func buildIndex(sources []string, output string) {
	entries := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		manifest := readManifest(source)
		entries = append(entries, map[string]any{"id": manifest.ID, "name": manifest.Name, "version": manifest.Version, "description": manifest.Description, "runpilotApi": manifest.Requires.RunPilotAPI, "asset": manifest.ID + "-" + manifest.Version + ".rpplugin"})
	}
	data, err := json.MarshalIndent(map[string]any{"apiVersion": 1, "plugins": entries}, "", "  ")
	if err != nil {
		fail(err.Error())
	}
	if err := os.WriteFile(output, append(data, '\n'), 0o644); err != nil {
		fail(err.Error())
	}
}

func readManifest(source string) plugins.Manifest {
	data, err := os.ReadFile(filepath.Join(source, "plugin.yaml"))
	if err != nil {
		fail(err.Error())
	}
	var manifest plugins.Manifest
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&manifest); err != nil {
		fail(err.Error())
	}
	return manifest
}

func fail(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
