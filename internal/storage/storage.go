// Package storage owns provider lookup and provider-facing filesystem semantics.
package storage

import (
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

// NormalizeDefinition keeps provider-specific configuration rules with the
// provider factory, so callers do not need to inspect Local settings.
func NormalizeDefinition(d *model.StorageDefinition) error {
	if d.Type != model.StorageLocal || d.Local == nil {
		return fmt.Errorf("unsupported storage provider")
	}
	_, err := NewLocal(*d.Local)
	return err
}

func ProviderFor(d model.StorageDefinition) (Provider, error) {
	if err := NormalizeDefinition(&d); err != nil {
		return nil, err
	}
	return NewLocal(*d.Local)
}
