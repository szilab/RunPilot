package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

const MaxCatalogSize = 1 << 20

type Catalog struct {
	SchemaVersion int            `json:"schemaVersion"`
	Plugins       []CatalogEntry `json:"plugins"`
}
type CatalogEntry struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Latest      string           `json:"latest"`
	Versions    []CatalogVersion `json:"versions"`
}
type CatalogVersion struct {
	Version   string   `json:"version"`
	Platforms []string `json:"platforms,omitempty"`
	Requires  Requires `json:"requires"`
	URL       string   `json:"url"`
	SHA256    string   `json:"sha256"`
}
type CatalogStatus struct {
	CatalogEntry
	LatestCompatible string `json:"latestCompatible,omitempty"`
	Incompatibility  string `json:"incompatibility,omitempty"`
	UpdateAvailable  bool   `json:"updateAvailable"`
}
type InstallSource struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Registry string `json:"registry"`
	SHA256   string `json:"sha256"`
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid HTTP(S) URL %q", raw)
	}
	return nil
}
func (v CatalogVersion) manifest(id, name string) Manifest {
	return Manifest{APIVersion: PluginAPIVersion, ID: id, Name: name, Version: v.Version, Platforms: v.Platforms, Requires: v.Requires}
}
func ParseCatalog(data []byte) (Catalog, error) {
	if len(data) > MaxCatalogSize {
		return Catalog{}, fmt.Errorf("plugin catalog exceeds %d bytes", MaxCatalogSize)
	}
	var c Catalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return c, fmt.Errorf("decode catalog: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("trailing catalog content")
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}
func (c Catalog) Validate() error {
	if c.SchemaVersion != 1 || c.Plugins == nil {
		return fmt.Errorf("invalid catalog schema (expected schemaVersion 1 and plugins array)")
	}
	ids := map[string]bool{}
	for _, p := range c.Plugins {
		if !validID(p.ID) || strings.TrimSpace(p.Name) == "" || len(p.Versions) == 0 {
			return fmt.Errorf("invalid catalog plugin %q", p.ID)
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate plugin %q", p.ID)
		}
		ids[p.ID] = true
		versions := map[string]bool{}
		latest := ""
		for _, v := range p.Versions {
			if err := v.manifest(p.ID, p.Name).Validate(); err != nil {
				return fmt.Errorf("%s: %w", p.ID, err)
			}
			if versions[v.Version] {
				return fmt.Errorf("duplicate version %s/%s", p.ID, v.Version)
			}
			versions[v.Version] = true
			if err := validateURL(v.URL); err != nil {
				return err
			}
			if digest, err := hex.DecodeString(v.SHA256); err != nil || len(digest) != sha256.Size {
				return fmt.Errorf("invalid checksum for %s/%s", p.ID, v.Version)
			}
			if latest == "" || versionBefore(latest, v.Version) {
				latest = v.Version
			}
		}
		if p.Latest != latest {
			return fmt.Errorf("incorrect latest version for %s: expected %s", p.ID, latest)
		}
	}
	return nil
}

// versionBefore provides deterministic ordering even for equal-precedence build metadata.
func versionBefore(a, b string) bool { n := CompareVersions(a, b); return n < 0 || (n == 0 && a < b) }
func (c *Catalog) Sort() {
	sort.Slice(c.Plugins, func(i, j int) bool { return c.Plugins[i].ID < c.Plugins[j].ID })
	for i := range c.Plugins {
		p := &c.Plugins[i]
		sort.Slice(p.Versions, func(i, j int) bool { return versionBefore(p.Versions[j].Version, p.Versions[i].Version) })
		if len(p.Versions) > 0 {
			p.Latest = p.Versions[0].Version
		}
	}
}
func (c Catalog) Statuses(goos string, installed []Status, registry string) []CatalogStatus {
	result := make([]CatalogStatus, 0, len(c.Plugins))
	for _, p := range c.Plugins {
		s := CatalogStatus{CatalogEntry: p}
		for _, v := range p.Versions {
			err := v.manifest(p.ID, p.Name).CompatibilityError(goos)
			if v.Version == p.Latest && err != nil {
				s.Incompatibility = err.Error()
			}
			if err == nil && (s.LatestCompatible == "" || versionBefore(s.LatestCompatible, v.Version)) {
				s.LatestCompatible = v.Version
			}
		}
		for _, local := range installed {
			if local.Manifest.ID == p.ID && local.Source != nil && local.Source.Registry == registry && s.LatestCompatible != "" {
				s.UpdateAvailable = CompareVersions(s.LatestCompatible, local.Manifest.Version) > 0
			}
		}
		result = append(result, s)
	}
	return result
}

func boundedGet(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
	if err := validateURL(rawURL); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch plugin resource: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plugin resource returned %s", resp.Status)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("plugin resource exceeds %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("plugin resource exceeds %d bytes", limit)
	}
	return data, nil
}
func FetchCatalog(ctx context.Context, client *http.Client, registry string) (Catalog, error) {
	data, err := boundedGet(ctx, client, registry, MaxCatalogSize)
	if err != nil {
		return Catalog{}, err
	}
	return ParseCatalog(data)
}
func DownloadAndInstall(ctx context.Context, client *http.Client, registry, root string, entry CatalogEntry, version CatalogVersion) (Manifest, error) {
	if err := (Catalog{SchemaVersion: 1, Plugins: []CatalogEntry{{ID: entry.ID, Name: entry.Name, Latest: version.Version, Versions: []CatalogVersion{version}}}}).Validate(); err != nil {
		return Manifest{}, err
	}
	expected := version.manifest(entry.ID, entry.Name)
	if err := expected.CompatibilityError(runtime.GOOS); err != nil {
		return Manifest{}, err
	}
	data, err := boundedGet(ctx, client, version.URL, MaxPackageSize)
	if err != nil {
		return Manifest{}, err
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), version.SHA256) {
		return Manifest{}, fmt.Errorf("plugin package checksum mismatch")
	}
	file, err := os.CreateTemp("", "runpilot-plugin-*.rpplugin")
	if err != nil {
		return Manifest{}, err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return Manifest{}, err
	}
	if closeErr != nil {
		return Manifest{}, closeErr
	}
	source := &InstallSource{ID: entry.ID, Version: version.Version, Registry: registry, SHA256: strings.ToLower(version.SHA256)}
	return installPackage(root, file.Name(), version.SHA256, &expected, source)
}
