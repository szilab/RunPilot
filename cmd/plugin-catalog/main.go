// plugin-catalog creates immutable publication records from built packages and
// regenerates the static catalog from those records, locally or from GitHub.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	packagePath := flag.String("package", "", "build publication record for package")
	policyPath := flag.String("policy", "plugins/publication.json", "repository publication policy")
	repository := flag.String("repository", "szilab/RunPilot", "GitHub owner/repository")
	tag := flag.String("tag", "", "exact plugin release tag")
	output := flag.String("out", "catalog.json", "output JSON")
	github := flag.Bool("github", false, "regenerate from all published GitHub release records")
	sourceID := flag.String("source", "", "print publication source for plugin ID")
	flag.Parse()
	var policy plugins.PublicationPolicy
	rawPolicy, err := os.ReadFile(*policyPath)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(rawPolicy, &policy); err != nil {
		return err
	}
	var data []byte
	if *sourceID != "" || *packagePath != "" {
		if *sourceID != "" {
			rule, err := policy.Rule(*sourceID)
			if err != nil {
				return err
			}
			fmt.Println(rule.Source)
			return nil
		}
		record, err := plugins.RecordFromPackage(*packagePath, *repository, *tag, policy)
		if err != nil {
			return err
		}
		data, err = json.MarshalIndent(record, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
	} else {
		var records []plugins.PublicationRecord
		if *github {
			var err error
			records, err = githubRecords(*repository)
			if err != nil {
				return err
			}
		}
		for _, path := range flag.Args() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var record plugins.PublicationRecord
			if err = json.Unmarshal(raw, &record); err != nil {
				return err
			}
			records = append(records, record)
		}
		catalog, err := plugins.GeneratePublicCatalog(records, policy)
		if err != nil {
			return err
		}
		data, err = plugins.CatalogJSON(catalog)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		return err
	}
	return os.WriteFile(*output, data, 0o644)
}
func githubGet(url string, accept string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := os.Getenv("GH_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub returned %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if len(raw) > 8<<20 {
		return nil, fmt.Errorf("GitHub response exceeds limit")
	}
	return raw, err
}
func githubRecords(repository string) ([]plugins.PublicationRecord, error) {
	return githubRecordsWith(repository, githubGet)
}

func githubRecordsWith(repository string, fetch func(string, string) ([]byte, error)) ([]plugins.PublicationRecord, error) {
	if strings.Count(repository, "/") != 1 || strings.ContainsAny(repository, "?# :\\") {
		return nil, fmt.Errorf("invalid repository")
	}
	records := []plugins.PublicationRecord{}
	for page := 1; page <= 100; page++ {
		raw, err := fetch(fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", repository, page), "application/vnd.github+json")
		if err != nil {
			return nil, err
		}
		var releases []struct {
			Tag    string `json:"tag_name"`
			Draft  bool   `json:"draft"`
			Assets []struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"assets"`
		}
		if err = json.Unmarshal(raw, &releases); err != nil {
			return nil, err
		}
		for _, release := range releases {
			if release.Draft || release.Tag == "plugin-catalog" || !strings.HasPrefix(release.Tag, "plugin-") {
				continue
			}
			found := false
			for _, asset := range release.Assets {
				if asset.Name == "publication.json" {
					// Only API asset URLs in the configured repository may receive the token.
					if !strings.HasPrefix(asset.URL, "https://api.github.com/repos/"+repository+"/releases/assets/") {
						return nil, fmt.Errorf("unexpected asset API URL")
					}
					raw, err := fetch(asset.URL, "application/octet-stream")
					if err != nil {
						return nil, err
					}
					var record plugins.PublicationRecord
					if err = json.Unmarshal(raw, &record); err != nil {
						return nil, err
					}
					p := record.Plugin
					if len(p.Versions) != 1 || release.Tag != plugins.ReleaseTag(p.ID, p.Latest) {
						return nil, fmt.Errorf("release record/tag mismatch")
					}
					expected := "https://github.com/" + repository + "/releases/download/" + release.Tag + "/" + plugins.PackageName(p.ID, p.Latest)
					if p.Versions[0].URL != expected {
						return nil, fmt.Errorf("release record asset URL mismatch")
					}
					names := map[string]bool{}
					for _, a := range release.Assets {
						names[a.Name] = true
					}
					packageName := plugins.PackageName(p.ID, p.Latest)
					if !names[packageName] || !names[packageName+".sha256"] {
						return nil, fmt.Errorf("release %s is missing package/checksum assets", release.Tag)
					}
					records = append(records, record)
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("release %s has no publication.json", release.Tag)
			}
		}
		if len(releases) < 100 {
			return records, nil
		}
	}
	return nil, fmt.Errorf("GitHub pagination limit exceeded")
}
