// Package storage owns provider lookup and provider-facing filesystem semantics.
package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

type Capabilities struct {
	Browse          bool `json:"browse"`
	Download        bool `json:"download"`
	Upload          bool `json:"upload"`
	CreateDirectory bool `json:"createDirectory"`
	Rename          bool `json:"rename"`
	Move            bool `json:"move"`
	Copy            bool `json:"copy"`
	Delete          bool `json:"delete"`
	TextEdit        bool `json:"textEdit"`
	Versions        bool `json:"versions"`
	Restore         bool `json:"restore"`
}
type State struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Descriptor is a runtime-discovered location. It is intentionally separate
// from the former persisted StorageDefinition configuration model.
type Descriptor struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Type         string       `json:"type"`
	Capabilities Capabilities `json:"capabilities"`
	State        State        `json:"state"`
}

type DockerVolumeDescriptor struct {
	Name    string
	Driver  string
	Running bool
}
type DockerVolumeCatalog interface {
	ListStorageVolumes(context.Context) ([]DockerVolumeDescriptor, error)
	DockerStorageState(context.Context) State
}
type DockerVolumeFilesystem interface {
	DockerVolumeCatalog
	ListDockerVolume(ctx context.Context, volume, path string, options ListOptions) ([]Entry, error)
	ReadDockerVolume(ctx context.Context, volume, path string) ([]byte, error)
}

// Registry discovers Storage locations on demand. The built-in local location
// is always present; Docker locations are an optional runtime capability.
type Registry struct {
	docker DockerVolumeFilesystem
}

func NewRegistry(docker DockerVolumeFilesystem) *Registry {
	return &Registry{docker: docker}
}
func LocalID() string         { return "local" }
func DockerVolumesID() string { return "docker-volumes" }
func (r *Registry) List(ctx context.Context) []Descriptor {
	local, _ := NewLocal(model.LocalStorageSpec{Scope: model.LocalStorageScopeHost})
	out := []Descriptor{{ID: LocalID(), Name: "Local filesystem", Type: "local", Capabilities: local.Capabilities(), State: local.State()}}
	if r.docker == nil {
		return out
	}
	provider := NewDockerVolumes(r.docker)
	out = append(out, Descriptor{ID: DockerVolumesID(), Name: "Docker volumes", Type: "docker-volumes", Capabilities: provider.Capabilities(), State: provider.State()})
	return out
}
func (r *Registry) Provider(_ context.Context, id string) (Provider, error) {
	if id == LocalID() {
		return NewLocal(model.LocalStorageSpec{Scope: model.LocalStorageScopeHost})
	}
	if id != DockerVolumesID() || r.docker == nil {
		return nil, fmt.Errorf("unknown storage %q", id)
	}
	return NewDockerVolumes(r.docker), nil
}

type Entry struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Type       string     `json:"type"`
	Size       *int64     `json:"size,omitempty"`
	ModifiedAt *time.Time `json:"modifiedAt,omitempty"`
}
type Listing struct {
	Path         string        `json:"path"`
	ParentPath   *string       `json:"parentPath"`
	Entries      []Entry       `json:"entries"`
	Capabilities *Capabilities `json:"capabilities,omitempty"`
	State        *State        `json:"state,omitempty"`
}
type ListOptions struct {
	ShowHidden bool
}
type ObjectReader struct {
	Reader     io.ReadCloser
	Name       string
	Size       *int64
	ModifiedAt *time.Time
}

// Provider deliberately only includes operations meaningful to all storage sources.
type Provider interface {
	Capabilities() Capabilities
	List(string) (Listing, error)
	Open(string) (*ObjectReader, error)
}
type ListOptionsProvider interface {
	ListWithOptions(string, ListOptions) (Listing, error)
}
type UploadProvider interface {
	Upload(string, string, io.Reader) error
}
type MutableProvider interface {
	CreateDirectory(string, string) error
	Rename(string, string) error
	Move(string, string) error
	Delete(string) error
}
type CopyProvider interface{ Copy(string, string) error }
type TextEditor interface {
	ReadText(string) (string, error)
	WriteText(string, string) error
}

type StateProvider interface{ State() State }
type PathCapabilitiesProvider interface{ CapabilitiesFor(string) Capabilities }
type PathStateProvider interface{ StateFor(string) State }

// BackupSource is an internal resolved filesystem location for a future
// backup source reference. It is deliberately not serialized through the API.
type BackupSource struct{ Path string }
type BackupSourceProvider interface {
	ResolveBackupSource(string) (BackupSource, error)
}
