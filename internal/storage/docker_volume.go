package storage

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/szilab/RunPilot/internal/model"
)

// DockerVolume delegates all filesystem behavior to Local after resolving the
// mountpoint through the Docker CLI resolver. The mountpoint never leaves this
// package through ordinary provider responses.
type DockerVolume struct {
	spec     model.DockerVolumeStorageSpec
	resolver DockerVolumeResolver
}

func NewDockerVolume(spec model.DockerVolumeStorageSpec, resolver DockerVolumeResolver) (*DockerVolume, error) {
	if strings.TrimSpace(spec.Volume) == "" {
		return nil, fmt.Errorf("Docker volume storage requires a volume name")
	}
	if resolver == nil {
		return nil, fmt.Errorf("Docker volume resolver is unavailable")
	}
	return &DockerVolume{spec: spec, resolver: resolver}, nil
}

func (d *DockerVolume) local() (*Local, DockerVolumeUsage, error) {
	ctx := context.Background()
	details, err := d.resolver.VolumeDetails(ctx, d.spec.Volume)
	if err != nil {
		return nil, DockerVolumeUsage{}, err
	}
	if details.Driver != "local" {
		return nil, DockerVolumeUsage{}, fmt.Errorf("Docker volume driver %q cannot be browsed directly by RunPilot", details.Driver)
	}
	if strings.TrimSpace(details.Mountpoint) == "" {
		return nil, DockerVolumeUsage{}, fmt.Errorf("Docker volume has no browseable mountpoint")
	}
	usage, err := d.resolver.VolumeUsage(ctx, d.spec.Volume)
	if err != nil {
		return nil, DockerVolumeUsage{}, err
	}
	l, err := NewLocal(model.LocalStorageSpec{Scope: model.LocalStorageScopeRoot, Root: details.Mountpoint})
	return l, usage, err
}
func (d *DockerVolume) State() State {
	l, usage, err := d.local()
	if err != nil {
		return State{Status: "unavailable", Reason: err.Error()}
	}
	if state := l.State(); state.Status != "ready" {
		return state
	}
	if usage.Running {
		return State{Status: "read-only", Reason: "This volume is currently used by a running container. RunPilot exposes it read-only."}
	}
	return State{Status: "ready"}
}
func (d *DockerVolume) Capabilities() Capabilities {
	l, usage, err := d.local()
	if err != nil || l.State().Status != "ready" {
		return Capabilities{}
	}
	caps := l.Capabilities()
	if usage.Running {
		caps.Upload = false
		caps.CreateDirectory = false
		caps.Rename = false
		caps.Move = false
		caps.Copy = false
		caps.Delete = false
		caps.TextEdit = false
	}
	return caps
}
func (d *DockerVolume) List(p string) (Listing, error) {
	l, _, err := d.local()
	if err != nil {
		return Listing{}, err
	}
	return l.List(p)
}
func (d *DockerVolume) ListWithOptions(p string, o ListOptions) (Listing, error) {
	l, _, err := d.local()
	if err != nil {
		return Listing{}, err
	}
	return l.ListWithOptions(p, o)
}
func (d *DockerVolume) Open(p string) (*ObjectReader, error) {
	l, _, err := d.local()
	if err != nil {
		return nil, err
	}
	return l.Open(p)
}
func (d *DockerVolume) mutable() (*Local, error) {
	l, usage, err := d.local()
	if err != nil {
		return nil, err
	}
	if usage.Running {
		return nil, fmt.Errorf("Docker volume is currently used by a running container and is read-only")
	}
	return l, nil
}
func (d *DockerVolume) Upload(parent, name string, r io.Reader) error {
	l, e := d.mutable()
	if e != nil {
		return e
	}
	return l.Upload(parent, name, r)
}
func (d *DockerVolume) CreateDirectory(parent, name string) error {
	l, e := d.mutable()
	if e != nil {
		return e
	}
	return l.CreateDirectory(parent, name)
}
func (d *DockerVolume) Rename(p, name string) error {
	l, e := d.mutable()
	if e != nil {
		return e
	}
	return l.Rename(p, name)
}
func (d *DockerVolume) Move(src, destination string) error {
	l, e := d.mutable()
	if e != nil {
		return e
	}
	return l.Move(src, destination)
}
func (d *DockerVolume) Copy(src, destination string) error {
	l, e := d.mutable()
	if e != nil {
		return e
	}
	return l.Copy(src, destination)
}
func (d *DockerVolume) Delete(p string) error {
	l, e := d.mutable()
	if e != nil {
		return e
	}
	return l.Delete(p)
}
func (d *DockerVolume) ReadText(p string) (string, error) {
	l, _, e := d.local()
	if e != nil {
		return "", e
	}
	return l.ReadText(p)
}
func (d *DockerVolume) WriteText(p, content string) error {
	l, e := d.mutable()
	if e != nil {
		return e
	}
	return l.WriteText(p, content)
}
func (d *DockerVolume) ResolveBackupSource(p string) (BackupSource, error) {
	l, usage, e := d.local()
	if e != nil {
		return BackupSource{}, e
	}
	if usage.Running {
		return BackupSource{}, fmt.Errorf("Docker volume is currently used by a running container")
	}
	return l.ResolveBackupSource(p)
}
