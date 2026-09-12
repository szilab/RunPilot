package storage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

type fakeVolumeResolver struct {
	details DockerVolumeDetails
	usage   DockerVolumeUsage
	err     error
}

func (f *fakeVolumeResolver) VolumeDetails(context.Context, string) (DockerVolumeDetails, error) {
	return f.details, f.err
}
func (f *fakeVolumeResolver) VolumeUsage(context.Context, string) (DockerVolumeUsage, error) {
	return f.usage, f.err
}

func TestDockerVolumeDelegatesAndBecomesReadOnly(t *testing.T) {
	root := t.TempDir()
	f := &fakeVolumeResolver{details: DockerVolumeDetails{Driver: "local", Mountpoint: root}}
	p, err := NewDockerVolume(model.DockerVolumeStorageSpec{Volume: "app_db"}, f)
	if err != nil {
		t.Fatal(err)
	}
	if p.State().Status != "ready" || !p.Capabilities().Upload {
		t.Fatalf("state %#v caps %#v", p.State(), p.Capabilities())
	}
	if err := p.Upload("", "note.txt", bytes.NewBufferString("ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "note.txt")); err != nil {
		t.Fatal(err)
	}
	f.usage.Running = true
	if p.State().Status != "read-only" || p.Capabilities().Upload || !p.Capabilities().Browse {
		t.Fatalf("readonly state %#v caps %#v", p.State(), p.Capabilities())
	}
	if err := p.WriteText("note.txt", "no"); err == nil {
		t.Fatal("running volume mutation accepted")
	}
	if _, err := p.ResolveBackupSource(""); err == nil {
		t.Fatal("running volume backup source accepted")
	}
	f.usage.Running = false
	source, err := p.ResolveBackupSource("")
	if err != nil || source.Path == "" {
		t.Fatalf("source %#v %v", source, err)
	}
}

func TestDockerVolumeUnavailableAndDefinitionValidation(t *testing.T) {
	if err := NormalizeDefinition(&model.StorageDefinition{Type: model.StorageDockerVolume}); err == nil {
		t.Fatal("missing volume config accepted")
	}
	f := &fakeVolumeResolver{details: DockerVolumeDetails{Driver: "nfs", Mountpoint: "/unused"}}
	p, err := NewDockerVolume(model.DockerVolumeStorageSpec{Volume: "remote"}, f)
	if err != nil {
		t.Fatal(err)
	}
	if state := p.State(); state.Status != "unavailable" {
		t.Fatalf("state %#v", state)
	}
	f.err = errors.New("Docker daemon unavailable")
	if state := p.State(); state.Status != "unavailable" {
		t.Fatalf("state %#v", state)
	}
	if _, err := ProviderFor(model.StorageDefinition{Type: model.StorageDockerVolume, DockerVolume: &model.DockerVolumeStorageSpec{Volume: "remote"}}, f); err != nil {
		t.Fatalf("factory: %v", err)
	}
}
