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
type Entry struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Type       string     `json:"type"`
	Size       *int64     `json:"size,omitempty"`
	ModifiedAt *time.Time `json:"modifiedAt,omitempty"`
}
type Listing struct {
	Path       string  `json:"path"`
	ParentPath *string `json:"parentPath"`
	Entries    []Entry `json:"entries"`
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

// BackupSource is an internal resolved filesystem location for a future
// backup source reference. It is deliberately not serialized through the API.
type BackupSource struct{ Path string }
type BackupSourceProvider interface {
	ResolveBackupSource(string) (BackupSource, error)
}

// DockerVolumeDetails and DockerVolumeResolver keep Docker resolution behind
// the provider factory. Storage never constructs a Docker daemon mount path.
type DockerVolumeDetails struct{ Driver, Mountpoint string }
type DockerVolumeUsage struct{ Running bool }
type DockerVolumeResolver interface {
	VolumeDetails(context.Context, string) (DockerVolumeDetails, error)
	VolumeUsage(context.Context, string) (DockerVolumeUsage, error)
}

// NormalizeDefinition keeps provider-specific configuration rules with the
// provider factory, so callers do not need to inspect Local settings.
func NormalizeDefinition(d *model.StorageDefinition) error {
	switch d.Type {
	case model.StorageLocal:
		if d.Local == nil {
			return fmt.Errorf("local storage configuration is required")
		}
		_, err := NewLocal(*d.Local)
		return err
	case model.StorageDockerVolume:
		if d.DockerVolume == nil || d.DockerVolume.Volume == "" {
			return fmt.Errorf("Docker volume storage requires a volume name")
		}
		return nil
	default:
		return fmt.Errorf("unsupported storage provider")
	}
}

func ProviderFor(d model.StorageDefinition, resolvers ...DockerVolumeResolver) (Provider, error) {
	if err := NormalizeDefinition(&d); err != nil {
		return nil, err
	}
	if d.Type == model.StorageLocal {
		return NewLocal(*d.Local)
	}
	if len(resolvers) == 0 || resolvers[0] == nil {
		return nil, fmt.Errorf("Docker volume resolver is unavailable")
	}
	return NewDockerVolume(*d.DockerVolume, resolvers[0])
}
