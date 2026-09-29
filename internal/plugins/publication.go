package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// PublicationPolicy is repository-only release policy, never package/runtime data.
type PublicationPolicy struct {
	Plugins []PublicationRule `json:"plugins"`
}
type PublicationRule struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Publish bool   `json:"publish"`
}

func (p PublicationPolicy) Rule(id string) (PublicationRule, error) {
	var found *PublicationRule
	seen := map[string]bool{}
	for _, r := range p.Plugins {
		if !validID(r.ID) || !safePackagePath(r.Source) || seen[r.ID] {
			return PublicationRule{}, fmt.Errorf("invalid or duplicate publication rule %q", r.ID)
		}
		seen[r.ID] = true
		if r.ID == id {
			copy := r
			found = &copy
		}
	}
	if found == nil || !found.Publish {
		return PublicationRule{}, fmt.Errorf("plugin %q is not publishable", id)
	}
	return *found, nil
}
func ReleaseTag(id, version string) string  { return "plugin-" + id + "-v" + version }
func PackageName(id, version string) string { return id + "-" + version + ".rpplugin" }

// PublicationRecord is attached to each immutable release so catalog regeneration
// never needs current source manifests or mutable previous catalog state.
type PublicationRecord struct {
	SchemaVersion int          `json:"schemaVersion"`
	Plugin        CatalogEntry `json:"plugin"`
}

func RecordFromPackage(path, repository, tag string, policy PublicationPolicy) (PublicationRecord, error) {
	m, err := InspectPackage(path)
	if err != nil {
		return PublicationRecord{}, err
	}
	if _, err := policy.Rule(m.ID); err != nil {
		return PublicationRecord{}, err
	}
	if tag != ReleaseTag(m.ID, m.Version) {
		return PublicationRecord{}, fmt.Errorf("tag %q does not match manifest %s/%s", tag, m.ID, m.Version)
	}
	if strings.Count(repository, "/") != 1 || strings.ContainsAny(repository, "?# :\\") {
		return PublicationRecord{}, fmt.Errorf("invalid GitHub repository")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return PublicationRecord{}, err
	}
	sum := sha256.Sum256(data)
	v := CatalogVersion{Version: m.Version, Platforms: m.Platforms, Requires: m.Requires, URL: "https://github.com/" + repository + "/releases/download/" + tag + "/" + PackageName(m.ID, m.Version), SHA256: hex.EncodeToString(sum[:])}
	record := PublicationRecord{SchemaVersion: 1, Plugin: CatalogEntry{ID: m.ID, Name: m.Name, Description: m.Description, Latest: m.Version, Versions: []CatalogVersion{v}}}
	_, err = GenerateCatalog([]PublicationRecord{record})
	return record, err
}
func GenerateCatalog(records []PublicationRecord) (Catalog, error) {
	c := Catalog{SchemaVersion: 1, Plugins: []CatalogEntry{}}
	indexes := map[string]int{}
	for _, r := range records {
		if r.SchemaVersion != 1 || len(r.Plugin.Versions) != 1 {
			return c, fmt.Errorf("invalid publication record")
		}
		if err := (Catalog{SchemaVersion: 1, Plugins: []CatalogEntry{r.Plugin}}).Validate(); err != nil {
			return c, err
		}
		if i, ok := indexes[r.Plugin.ID]; ok {
			p := &c.Plugins[i]
			if versionBefore(p.Latest, r.Plugin.Latest) {
				p.Name = r.Plugin.Name
				p.Description = r.Plugin.Description
				p.Latest = r.Plugin.Latest
			}
			p.Versions = append(p.Versions, r.Plugin.Versions...)
		} else {
			indexes[r.Plugin.ID] = len(c.Plugins)
			copy := r.Plugin
			copy.Versions = append([]CatalogVersion(nil), r.Plugin.Versions...)
			c.Plugins = append(c.Plugins, copy)
		}
	}
	c.Sort()
	return c, c.Validate()
}
func CatalogJSON(c Catalog) ([]byte, error) {
	c.Sort()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if len(data)+1 > MaxCatalogSize {
		return nil, fmt.Errorf("generated catalog exceeds %d bytes", MaxCatalogSize)
	}
	return append(data, '\n'), err
}

// GeneratePublicCatalog applies repository publication policy before generation.
// Technical fixtures cannot enter the public catalog through a supplied record.
func GeneratePublicCatalog(records []PublicationRecord, policy PublicationPolicy) (Catalog, error) {
	// Validate the complete policy, including rules that are not in these records.
	seen := map[string]bool{}
	for _, r := range policy.Plugins {
		if !validID(r.ID) || !safePackagePath(r.Source) || seen[r.ID] {
			return Catalog{}, fmt.Errorf("invalid or duplicate publication rule %q", r.ID)
		}
		seen[r.ID] = true
	}
	public := make([]PublicationRecord, 0, len(records))
	for _, r := range records {
		if _, err := policy.Rule(r.Plugin.ID); err == nil {
			public = append(public, r)
		}
	}
	return GenerateCatalog(public)
}
