package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/plugins"
)

func TestGitHubRecords(t *testing.T) {
	const repository = "example/RunPilot"
	const assetURL = "https://api.github.com/repos/example/RunPilot/releases/assets/1"
	for _, name := range []string{"valid", "missing record", "tag mismatch", "missing package", "untrusted asset URL", "HTTP failure"} {
		t.Run(name, func(t *testing.T) {
			record := plugins.PublicationRecord{SchemaVersion: 1, Plugin: plugins.CatalogEntry{ID: "hello", Name: "Hello", Latest: "1.0.0", Versions: []plugins.CatalogVersion{{Version: "1.0.0", URL: "https://github.com/example/RunPilot/releases/download/plugin-hello-v1.0.0/hello-1.0.0.rpplugin", SHA256: strings.Repeat("a", 64)}}}}
			tag := "plugin-hello-v1.0.0"
			if name == "tag mismatch" {
				tag = "plugin-hello-v2.0.0"
			}
			url := assetURL
			if name == "untrusted asset URL" {
				url = "https://untrusted.example/asset"
			}
			assets := []map[string]string{{"name": "publication.json", "url": url}, {"name": "hello-1.0.0.rpplugin"}, {"name": "hello-1.0.0.rpplugin.sha256"}}
			if name == "missing record" {
				assets = assets[1:]
			}
			if name == "missing package" {
				assets = assets[:1]
			}
			fetch := func(url, accept string) ([]byte, error) {
				if name == "HTTP failure" {
					return nil, fmt.Errorf("service unavailable")
				}
				if url == assetURL {
					return json.Marshal(record)
				}
				if !strings.HasPrefix(url, "https://api.github.com/repos/"+repository+"/releases?") {
					t.Fatalf("unexpected URL: %s", url)
				}
				return json.Marshal([]map[string]any{{"tag_name": "application-v1.0.0"}, {"tag_name": "plugin-catalog"}, {"tag_name": "plugin-draft-v1.0.0", "draft": true}, {"tag_name": tag, "assets": assets}})
			}
			records, err := githubRecordsWith(repository, fetch)
			if name == "valid" {
				if err != nil || len(records) != 1 {
					t.Fatalf("%v %v", records, err)
				}
			} else if err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
}
func TestGitHubPagination(t *testing.T) {
	calls := 0
	records, err := githubRecordsWith("example/RunPilot", func(url, accept string) ([]byte, error) {
		calls++
		if calls == 1 {
			entries := make([]map[string]string, 100)
			for i := range entries {
				entries[i] = map[string]string{"tag_name": "application"}
			}
			return json.Marshal(entries)
		}
		if !strings.HasSuffix(url, "page=2") {
			t.Fatal(url)
		}
		return []byte("[]"), nil
	})
	if err != nil || len(records) != 0 || calls != 2 {
		t.Fatalf("%v %v %d", records, err, calls)
	}
}
