package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

type workspaceEvents struct {
	mu     sync.Mutex
	events []map[string]any
}

func (e *workspaceEvents) emit(_ string, name string, data any) {
	raw, _ := json.Marshal(data)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	decoded["event"] = name
	e.mu.Lock()
	e.events = append(e.events, decoded)
	e.mu.Unlock()
}

func (e *workspaceEvents) done(t *testing.T, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		for _, event := range e.events {
			if event["event"] == "workspace.operation.done" && event["id"] == id {
				e.mu.Unlock()
				return event
			}
		}
		e.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("operation %s did not finish", id)
	return nil
}

func newFilesController(t *testing.T) (*Controller, *workspaceEvents, string) {
	t.Helper()
	c := &Controller{dataDir: t.TempDir()}
	events := &workspaceEvents{}
	c.workspaceOperations = newWorkspaceOperationManager(c.openWorkspaceRoot, events.emit)
	c.workspaceTransfers = newWorkspaceTransferManager(c.openWorkspaceRoot)
	t.Cleanup(c.workspaceOperations.close)
	return c, events, t.TempDir()
}

func workspaceCall(t *testing.T, c *Controller, op string, params map[string]any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	value, err := (controllerPluginHost{c}).Workspace(context.Background(), "files", op, raw)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	_ = json.Unmarshal(value, &decoded)
	return decoded, nil
}

func failureCode(err error) string {
	var failure *plugins.HostFailure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceListReportsFileDetails(t *testing.T) {
	c, _, root := newFilesController(t)
	writeFile(t, filepath.Join(root, "notes.txt"), "hello")
	writeFile(t, filepath.Join(root, ".hidden"), "x")
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "notes.txt"), modified, modified); err != nil {
		t.Fatal(err)
	}
	listed, err := workspaceCall(t, c, "list", map[string]any{"root": root})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, raw := range listed["entries"].([]any) {
		entry := raw.(map[string]any)
		byName[entry["name"].(string)] = entry
	}
	notes := byName["notes.txt"]
	if notes["type"] != "file" || notes["size"] != float64(5) || notes["hidden"] != false || notes["modifiedAt"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("file entry = %v", notes)
	}
	if byName["folder"]["type"] != "directory" || byName["folder"]["directory"] != true || byName[".hidden"]["hidden"] != true {
		t.Fatalf("entries = %v", byName)
	}
	writeFile(t, filepath.Join(root, "folder", "deep", "inner.txt"), "inner")
	nested, err := workspaceCall(t, c, "list", map[string]any{"root": root, "path": "folder/deep"})
	if err != nil || len(nested["entries"].([]any)) != 1 || nested["entries"].([]any)[0].(map[string]any)["size"] != float64(5) {
		t.Fatalf("nested listing = %v, %v", nested, err)
	}
	if runtime.GOOS != "windows" {
		for name, target := range map[string]string{"to-file": "notes.txt", "to-dir": "folder", "broken": "missing", "outside": "/etc"} {
			if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
		}
		links, err := workspaceCall(t, c, "list", map[string]any{"root": root})
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]any{}
		for _, raw := range links["entries"].([]any) {
			entry := raw.(map[string]any)
			if entry["type"] == "symlink" {
				kinds[entry["name"].(string)] = entry["linkType"]
			}
		}
		if kinds["to-file"] != "file" || kinds["to-dir"] != "directory" || kinds["broken"] != "" || kinds["outside"] != "" {
			t.Fatalf("symlink targets = %v", kinds)
		}
	}
	stat, err := workspaceCall(t, c, "stat", map[string]any{"root": root, "path": "notes.txt"})
	if err != nil || stat["type"] != "file" || stat["size"] != float64(5) || stat["path"] != "notes.txt" {
		t.Fatalf("stat = %v, %v", stat, err)
	}
	if runtime.GOOS != "windows" {
		writeFile(t, filepath.Join(root, "Film: Title.mkv"), "x")
		if _, err := workspaceCall(t, c, "stat", map[string]any{"root": root, "path": "Film: Title.mkv"}); err != nil {
			t.Fatalf("colon file name rejected: %v", err)
		}
	}
}

func TestWorkspaceRenameStaysInsideRoot(t *testing.T) {
	c, _, root := newFilesController(t)
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.txt"), "b")
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceCall(t, c, "rename", map[string]any{"root": root, "path": "a.txt", "target": "b.txt"}); failureCode(err) != "already_exists" {
		t.Fatalf("replace without overwrite = %v", err)
	}
	if _, err := workspaceCall(t, c, "rename", map[string]any{"root": root, "path": "a.txt", "target": "dir/c.txt"}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "dir", "c.txt")); err != nil || string(data) != "a" {
		t.Fatalf("moved file = %q, %v", data, err)
	}
	if _, err := workspaceCall(t, c, "rename", map[string]any{"root": root, "path": "dir/c.txt", "target": "b.txt", "overwrite": true}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(data) != "a" {
		t.Fatalf("overwrite result = %q", data)
	}
	if _, err := workspaceCall(t, c, "rename", map[string]any{"root": root, "path": "missing", "target": "x"}); failureCode(err) != "not_found" {
		t.Fatalf("missing source = %v", err)
	}
	for _, target := range []string{"../escape.txt", "/tmp/escape.txt"} {
		if _, err := workspaceCall(t, c, "rename", map[string]any{"root": root, "path": "b.txt", "target": target}); failureCode(err) != "invalid_argument" {
			t.Errorf("target %q = %v", target, err)
		}
	}
	if runtime.GOOS != "windows" {
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		if _, err := workspaceCall(t, c, "rename", map[string]any{"root": root, "path": "b.txt", "target": "link/escaped.txt"}); err == nil {
			t.Fatal("rename followed a symlink out of the root")
		}
		if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); err == nil {
			t.Fatal("file escaped the root")
		}
	}
	missingRoot := filepath.Join(t.TempDir(), "missing")
	if _, err := workspaceCall(t, c, "rename", map[string]any{"root": missingRoot, "path": "a", "target": "b"}); err == nil {
		t.Fatal("rename created or used a missing root")
	}
	if _, err := os.Stat(missingRoot); err == nil {
		t.Fatal("a file operation created its root")
	}
}

func TestWorkspaceOperationsCopyMoveAndDelete(t *testing.T) {
	c, events, source := newFilesController(t)
	target := t.TempDir()
	writeFile(t, filepath.Join(source, "tree", "one.txt"), "one")
	writeFile(t, filepath.Join(source, "tree", "sub", "two.txt"), "two!")
	modified := time.Date(2025, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(source, "tree", "one.txt"), modified, modified); err != nil {
		t.Fatal(err)
	}
	skipped := int64(0)
	if runtime.GOOS != "windows" {
		if err := os.Symlink("/etc/passwd", filepath.Join(source, "tree", "link")); err != nil {
			t.Fatal(err)
		}
		skipped = 1
	}
	start := func(params map[string]any) map[string]any {
		t.Helper()
		started, err := workspaceCall(t, c, "operation.start", params)
		if err != nil {
			t.Fatal(err)
		}
		return events.done(t, started["id"].(string))
	}

	done := start(map[string]any{"kind": "copy", "source": map[string]any{"root": source, "path": "tree"}, "target": map[string]any{"root": target, "path": "copy"}})
	if done["state"] != "succeeded" || done["bytes"] != float64(7) || done["totalBytes"] != float64(7) || done["skipped"] != float64(skipped) {
		t.Fatalf("copy = %v", done)
	}
	if data, _ := os.ReadFile(filepath.Join(target, "copy", "sub", "two.txt")); string(data) != "two!" {
		t.Fatalf("copied content = %q", data)
	}
	if info, err := os.Stat(filepath.Join(target, "copy", "one.txt")); err != nil || !info.ModTime().Equal(modified) {
		t.Fatalf("copied modification time = %v, %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(target, "copy", "link")); err == nil {
		t.Fatal("copy followed or recreated a symlink")
	}

	// The target must not exist, and a directory cannot be copied into itself.
	if _, err := workspaceCall(t, c, "operation.start", map[string]any{"kind": "copy", "source": map[string]any{"root": source, "path": "tree"}, "target": map[string]any{"root": target, "path": "copy"}}); failureCode(err) != "already_exists" {
		t.Fatalf("existing target = %v", err)
	}
	if _, err := workspaceCall(t, c, "operation.start", map[string]any{"kind": "copy", "source": map[string]any{"root": source, "path": "tree"}, "target": map[string]any{"root": source, "path": "tree/sub/inside"}}); failureCode(err) != "invalid_argument" {
		t.Fatalf("copy into itself = %v", err)
	}

	done = start(map[string]any{"kind": "move", "source": map[string]any{"root": target, "path": "copy"}, "target": map[string]any{"root": target, "path": "moved"}})
	if done["state"] != "succeeded" {
		t.Fatalf("move = %v", done)
	}
	if _, err := os.Stat(filepath.Join(target, "moved", "one.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "copy")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("move kept its source")
	}

	done = start(map[string]any{"kind": "delete", "source": map[string]any{"root": target, "path": "moved"}})
	if done["state"] != "succeeded" || done["files"] != float64(2) {
		t.Fatalf("delete = %v", done)
	}
	if _, err := os.Stat(filepath.Join(target, "moved")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("delete kept the tree")
	}
	status, err := workspaceCall(t, c, "operation.status", map[string]any{"id": done["id"]})
	if err != nil || status["state"] != "succeeded" {
		t.Fatalf("status = %v, %v", status, err)
	}
	raw, _ := json.Marshal(map[string]any{"id": done["id"]})
	if _, err := (controllerPluginHost{c}).Workspace(context.Background(), "other", "operation.status", raw); failureCode(err) != "not_found" {
		t.Fatalf("other owner status = %v", err)
	}
}

func TestWorkspaceOperationCancelRemovesPartialCopy(t *testing.T) {
	c, events, root := newFilesController(t)
	file, err := os.Create(filepath.Join(root, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file is quick to create but takes a while to copy.
	if err := file.Truncate(8 << 30); err != nil {
		file.Close()
		t.Skipf("sparse files unavailable: %v", err)
	}
	file.Close()
	started, err := workspaceCall(t, c, "operation.start", map[string]any{"kind": "copy", "source": map[string]any{"root": root, "path": "large.bin"}, "target": map[string]any{"root": root, "path": "copy.bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceCall(t, c, "operation.cancel", map[string]any{"id": started["id"]}); err != nil {
		t.Fatal(err)
	}
	done := events.done(t, started["id"].(string))
	if done["state"] != "canceled" {
		t.Fatalf("canceled operation = %v", done)
	}
	if _, err := os.Stat(filepath.Join(root, "copy.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial copy was kept")
	}
}

func TestWorkspaceTransfers(t *testing.T) {
	c, _, root := newFilesController(t)
	writeFile(t, filepath.Join(root, "report.txt"), "report body")
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	if err := os.WriteFile(filepath.Join(root, "photo.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "fake.png"), "<html><script>alert(1)</script>")
	writeFile(t, filepath.Join(root, "drawing.svg"), "<svg/>")

	issued, err := workspaceCall(t, c, "transfer.download", map[string]any{"root": root, "path": "report.txt"})
	if err != nil || !strings.HasPrefix(issued["url"].(string), WorkspaceTransferPath) || issued["size"] != float64(11) {
		t.Fatalf("download ticket = %v, %v", issued, err)
	}
	ticket := strings.TrimPrefix(issued["url"].(string), WorkspaceTransferPath)
	for range 2 { // download tickets can be reused for ranged resumes
		download, err := c.OpenWorkspaceDownload(ticket)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(download.File)
		download.File.Close()
		if string(data) != "report body" || download.Inline || download.ContentType != "application/octet-stream" || !strings.HasPrefix(download.ContentDisposition(), "attachment") {
			t.Fatalf("download = %q %+v", data, download)
		}
	}
	inline := func(name string) bool {
		t.Helper()
		issued, err := workspaceCall(t, c, "transfer.download", map[string]any{"root": root, "path": name, "inline": true})
		if err != nil {
			t.Fatal(err)
		}
		download, err := c.OpenWorkspaceDownload(strings.TrimPrefix(issued["url"].(string), WorkspaceTransferPath))
		if err != nil {
			t.Fatal(err)
		}
		download.File.Close()
		return download.Inline && download.ContentType == "image/png"
	}
	if !inline("photo.png") || inline("fake.png") || inline("drawing.svg") {
		t.Fatal("only real raster images may be served inline")
	}
	if _, err := workspaceCall(t, c, "transfer.download", map[string]any{"root": root, "path": "."}); err == nil {
		t.Fatal("directory download was accepted")
	}

	upload := func(params map[string]any, body string) (int64, error) {
		t.Helper()
		issued, err := workspaceCall(t, c, "transfer.upload", params)
		if err != nil {
			return 0, err
		}
		return c.AcceptWorkspaceUpload(context.Background(), strings.TrimPrefix(issued["url"].(string), WorkspaceTransferPath), strings.NewReader(body))
	}
	if size, err := upload(map[string]any{"root": root, "directory": "", "name": "new.txt"}, "uploaded"); err != nil || size != 8 {
		t.Fatalf("upload = %d, %v", size, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "new.txt")); string(data) != "uploaded" {
		t.Fatalf("uploaded content = %q", data)
	}
	if _, err := upload(map[string]any{"root": root, "name": "new.txt"}, "again"); failureCode(err) != "already_exists" {
		t.Fatalf("upload over existing file = %v", err)
	}
	if _, err := upload(map[string]any{"root": root, "name": "new.txt", "overwrite": true}, "replaced"); err != nil {
		t.Fatal(err)
	}
	if _, err := upload(map[string]any{"root": root, "name": "big.txt", "maxBytes": 3}, "too large"); failureCode(err) != "too_large" {
		t.Fatalf("oversized upload = %v", err)
	}
	if _, err := workspaceCall(t, c, "transfer.upload", map[string]any{"root": root, "name": "sub/dir.txt"}); failureCode(err) != "invalid_argument" {
		t.Fatalf("nested upload name = %v", err)
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".runpilot-upload-") {
			t.Fatalf("temporary upload file was left: %s", entry.Name())
		}
	}
	issued, _ = workspaceCall(t, c, "transfer.upload", map[string]any{"root": root, "name": "once.txt"})
	once := strings.TrimPrefix(issued["url"].(string), WorkspaceTransferPath)
	if _, err := c.AcceptWorkspaceUpload(context.Background(), once, strings.NewReader("1")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AcceptWorkspaceUpload(context.Background(), once, strings.NewReader("2")); !errors.Is(err, ErrWorkspaceTransferUnknown) {
		t.Fatalf("reused upload ticket = %v", err)
	}
	c.workspaceTransfers.stopOwner("files")
	if _, err := c.OpenWorkspaceDownload(ticket); !errors.Is(err, ErrWorkspaceTransferUnknown) {
		t.Fatal("download ticket survived its owner stopping")
	}
}
