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
	Versions        bool `json:"versions"`
	Restore         bool `json:"restore"`
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

func ProviderFor(d model.StorageDefinition) (Provider, error) {
	if d.Type != model.StorageLocal || d.Local == nil {
		return nil, fmt.Errorf("unsupported storage provider")
	}
	return NewLocal(*d.Local)
}
