package core

import (
	"reflect"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestLauncherSaveLoadEditDeleteAndOrder(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.SaveLauncher([]model.LauncherEntry{{Name: " Docs ", URL: "https://example.com/docs", OpenMode: "external"}, {Name: "Internal", URL: "/p/app/", OpenMode: "runpilot"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID == "" || first[1].ID == "" || first[0].ID == first[1].ID || first[0].Name != "Docs" {
		t.Fatalf("bad launcher entries: %+v", first)
	}
	first[0].Name = "Guides"
	reordered, err := c.SaveLauncher([]model.LauncherEntry{first[1], first[0]})
	if err != nil {
		t.Fatal(err)
	}
	if reordered[0].ID != first[1].ID || reordered[1].Name != "Guides" {
		t.Fatalf("bad reorder/edit: %+v", reordered)
	}
	c.Close()
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reflect.DeepEqual(reopened.Snapshot().Launcher, reordered) {
		t.Fatalf("not persisted: %+v", reopened.Snapshot().Launcher)
	}
	deleted, err := reopened.SaveLauncher(reordered[:1])
	if err != nil || len(deleted) != 1 || deleted[0].ID != first[1].ID {
		t.Fatalf("bad delete: %+v, %v", deleted, err)
	}
}

func TestLauncherRejectsUnsafeAndInvalidChanges(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, url := range []string{"javascript:alert(1)", "data:text/html,x", "file:///etc/passwd", "http:missing-host", "https://user:pass@host/"} {
		if _, err := c.SaveLauncher([]model.LauncherEntry{{Name: "Bad", URL: url, OpenMode: "external"}}); err == nil {
			t.Errorf("accepted %q", url)
		}
	}
	if _, err := c.SaveLauncher([]model.LauncherEntry{{Name: "Bad", URL: "https://example.com", OpenMode: "runpilot"}}); err == nil {
		t.Fatal("embedded arbitrary URL")
	}
	if _, err := c.SaveLauncher([]model.LauncherEntry{{Name: "Bad", URL: "/p/app", Icon: "data:image/svg+xml,x", OpenMode: "runpilot"}}); err == nil {
		t.Fatal("unsafe icon")
	}
	if _, err := c.SaveLauncher([]model.LauncherEntry{{ID: strings.Repeat("a", 32), Name: "Unknown", URL: "https://example.com", OpenMode: "external"}}); err == nil {
		t.Fatal("unknown ID")
	}
	if len(c.Snapshot().Launcher) != 0 {
		t.Fatal("failed update changed configuration")
	}
}
