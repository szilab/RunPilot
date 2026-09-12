package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestLocalRootOperationsAndBoundary(t *testing.T) {
	root := t.TempDir()
	p, err := NewLocal(model.LocalStorageSpec{Scope: model.LocalStorageScopeRoot, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CreateDirectory("", "Projects"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden"), []byte("hidden"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootListing, err := p.List("")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range rootListing.Entries {
		if entry.Name == ".hidden" {
			t.Fatal("hidden file was listed by default")
		}
	}
	rootListing, err = p.ListWithOptions("", ListOptions{ShowHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	foundHidden := false
	for _, entry := range rootListing.Entries {
		foundHidden = foundHidden || entry.Name == ".hidden"
	}
	if !foundHidden {
		t.Fatal("hidden file was not listed when requested")
	}
	if err := p.Upload("Projects", "hello 世界.txt", bytes.NewReader([]byte{0, 1, 2})); err != nil {
		t.Fatal(err)
	}
	listing, err := p.List("Projects")
	if err != nil || len(listing.Entries) != 1 {
		t.Fatalf("listing = %#v, %v", listing, err)
	}
	read, err := p.Open("Projects/hello 世界.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(read.Reader)
	read.Reader.Close()
	if !bytes.Equal(got, []byte{0, 1, 2}) {
		t.Fatal("download changed bytes")
	}
	if err := p.Rename("Projects/hello 世界.txt", "renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if err := p.Move("Projects/renamed.txt", ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Copy("renamed.txt", "Projects"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Projects", "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, err := p.ReadText("notes.txt")
	if err != nil || text != "before" {
		t.Fatalf("ReadText = %q, %v", text, err)
	}
	if err := p.WriteText("notes.txt", "after"); err != nil {
		t.Fatal(err)
	}
	text, err = p.ReadText("notes.txt")
	if err != nil || text != "after" {
		t.Fatalf("edited text = %q, %v", text, err)
	}
	if err := p.Delete("renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.List("../"); err == nil {
		t.Fatal("traversal was accepted")
	}
	if _, err := p.List(filepath.ToSlash(filepath.Join(root, "outside"))); err == nil {
		t.Fatal("absolute path was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "exists"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.Upload("", "exists", bytes.NewReader(nil)); err == nil {
		t.Fatal("upload overwrote existing file")
	}
}
