package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const MaxCatalogSize = 1 << 20

type Catalog struct {
	APIVersion int            `json:"apiVersion"`
	Plugins    []CatalogEntry `json:"plugins"`
}

type CatalogEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	RunPilotAPI int    `json:"runpilotApi"`
	Asset       string `json:"asset"`
	SHA256      string `json:"sha256"`
}

func ParseCatalog(data []byte) (Catalog, error) {
	if len(data) > MaxCatalogSize {
		return Catalog{}, fmt.Errorf("plugin catalog exceeds %d bytes", MaxCatalogSize)
	}
	var catalog Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return Catalog{}, err
	}
	if catalog.APIVersion != 1 {
		return Catalog{}, fmt.Errorf("unsupported plugin catalog API %d", catalog.APIVersion)
	}
	for _, entry := range catalog.Plugins {
		if !validID(entry.ID) || strings.TrimSpace(entry.Version) == "" || !safePackagePath(entry.Asset) || len(entry.SHA256) != sha256.Size*2 {
			return Catalog{}, fmt.Errorf("invalid catalog entry %q", entry.ID)
		}
		if _, err := hex.DecodeString(entry.SHA256); err != nil {
			return Catalog{}, fmt.Errorf("invalid catalog checksum for %q", entry.ID)
		}
	}
	return catalog, nil
}

func DownloadAndInstall(ctx context.Context, client *http.Client, url, root string, entry CatalogEntry) (Manifest, error) {
	if client == nil {
		client = &http.Client{}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Manifest{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return Manifest{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("plugin download returned %s", response.Status)
	}
	if response.ContentLength > MaxPackageSize {
		return Manifest{}, fmt.Errorf("plugin download exceeds %d bytes", MaxPackageSize)
	}
	temporary, err := os.CreateTemp("", "runpilot-plugin-*.rpplugin")
	if err != nil {
		return Manifest{}, err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := io.Copy(temporary, io.LimitReader(response.Body, MaxPackageSize+1)); err != nil {
		_ = temporary.Close()
		return Manifest{}, err
	}
	if err := temporary.Close(); err != nil {
		return Manifest{}, err
	}
	return InstallPackage(root, name, entry.SHA256)
}
