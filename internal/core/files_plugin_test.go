package core

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/model"
)

// TestFilesPluginWASMManagesSourceFolders drives the compiled Files plugin
// through the real workspace capabilities.
func TestFilesPluginWASMManagesTheFilesystem(t *testing.T) {
	dataDir := t.TempDir()
	copySourcePlugin(t, dataDir, "files", "files")
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error {
		enabled := true
		cfg.Plugins = map[string]model.PluginSettings{"files": {Enabled: &enabled}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	controller, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	call := func(method string, params any) (map[string]any, string) {
		t.Helper()
		raw, _ := json.Marshal(params)
		response, err := controller.PluginCall(context.Background(), "files", method, raw)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		var decoded map[string]any
		_ = json.Unmarshal(response, &decoded)
		if failure, ok := decoded["error"].(map[string]any); ok {
			return nil, failure["code"].(string)
		}
		return decoded, ""
	}
	must := func(method string, params any) map[string]any {
		t.Helper()
		value, code := call(method, params)
		if code != "" {
			t.Fatalf("%s failed: %s", method, code)
		}
		return value
	}

	// Files always shows the whole filesystem; paths are relative to "/".
	home := t.TempDir()
	rel := func(path string) string { return strings.TrimPrefix(filepath.ToSlash(path), "/") }
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	listed := must("files.list", map[string]any{"path": rel(home)})
	if entries := listed["entries"].([]any); len(entries) != 1 || entries[0].(map[string]any)["size"] != float64(5) {
		t.Fatalf("listing = %v", listed)
	}
	if top := must("files.list", map[string]any{"path": ""}); top["top"] != true || len(top["entries"].([]any)) == 0 {
		t.Fatalf("top listing = %v", top)
	}
	opened := must("files.text.read", map[string]any{"path": rel(home) + "/notes.txt"})
	must("files.text.write", map[string]any{"path": rel(home) + "/notes.txt", "content": "second", "expectedModifiedAt": opened["modifiedAt"]})
	must("files.folder.create", map[string]any{"path": rel(home), "name": "Film: Extras"})
	must("files.rename", map[string]any{"path": rel(home) + "/notes.txt", "name": "renamed.txt"})
	if data, _ := os.ReadFile(filepath.Join(home, "renamed.txt")); string(data) != "second" {
		t.Fatalf("renamed content = %q", data)
	}
	if _, code := call("files.operation.start", map[string]any{"kind": "delete", "from": ""}); code != "invalid_argument" {
		t.Fatalf("deleting the top = %s", code)
	}

	must("files.operation.start", map[string]any{"kind": "copy", "from": rel(home) + "/renamed.txt", "to": rel(home) + "/Film: Extras/copy.txt"})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(filepath.Join(home, "Film: Extras", "copy.txt")); err == nil && string(data) == "second" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background copy did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}

	ticket := must("files.download", map[string]any{"path": rel(home) + "/renamed.txt"})
	download, err := controller.OpenWorkspaceDownload(strings.TrimPrefix(ticket["url"].(string), WorkspaceTransferPath))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(download.File)
	download.File.Close()
	if string(data) != "second" {
		t.Fatalf("download = %q", data)
	}
	uploadTicket := must("files.upload", map[string]any{"path": rel(home) + "/Film: Extras", "name": "upload.bin"})
	if _, err := controller.AcceptWorkspaceUpload(context.Background(), strings.TrimPrefix(uploadTicket["url"].(string), WorkspaceTransferPath), strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "Film: Extras", "upload.bin")); string(data) != "payload" {
		t.Fatalf("uploaded = %q", data)
	}
}
