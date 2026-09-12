package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
)

// DockerVolumes is one virtual Storage location. Its root consists of Docker
// volume names, and every remaining path component is inside that volume.
// Content access is delegated to Docker, never to a daemon host mountpoint.
type DockerVolumes struct{ docker DockerVolumeFilesystem }

func NewDockerVolumes(docker DockerVolumeFilesystem) *DockerVolumes {
	return &DockerVolumes{docker: docker}
}

func (d *DockerVolumes) State() State { return d.docker.DockerStorageState(context.Background()) }
func (d *DockerVolumes) StateFor(path string) State {
	state := d.State()
	if state.Status != "ready" {
		return state
	}
	volume, _, err := splitDockerVolumePath(path)
	if path == "" || err != nil {
		return state
	}
	for _, item := range d.volumes() {
		if item.Name == volume && item.Running {
			return State{Status: "read-only", Reason: "Volume is currently used by a running container. RunPilot exposes it read-only."}
		}
	}
	return state
}

// Global capabilities describe the available read-only provider operations.
// CapabilitiesFor supplies the effective set for the virtual root or volume.
func (d *DockerVolumes) Capabilities() Capabilities {
	return Capabilities{Browse: true, Download: true}
}
func (d *DockerVolumes) CapabilitiesFor(path string) Capabilities {
	if path == "" {
		return Capabilities{Browse: true}
	}
	if d.StateFor(path).Status == "unavailable" {
		return Capabilities{}
	}
	return Capabilities{Browse: true, Download: true}
}

func (d *DockerVolumes) volumes() []DockerVolumeDescriptor {
	items, err := d.docker.ListStorageVolumes(context.Background())
	if err != nil {
		return nil
	}
	return items
}

func validDockerVolumeName(value string) bool {
	if value == "" {
		return false
	}
	for i, ch := range value {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || (i > 0 && (ch == '.' || ch == '_' || ch == '-'))) {
			return false
		}
	}
	return true
}

func splitDockerVolumePath(value string) (string, string, error) {
	if value == "" {
		return "", "", nil
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", "", fmt.Errorf("invalid Docker volume path")
	}
	parts := strings.Split(value, "/")
	if len(parts) == 0 || !validDockerVolumeName(parts[0]) {
		return "", "", fmt.Errorf("invalid Docker volume name")
	}
	for _, part := range parts[1:] {
		if part == "" || part == "." || part == ".." || strings.Contains(part, ":") {
			return "", "", fmt.Errorf("invalid Docker volume path")
		}
	}
	return parts[0], strings.Join(parts[1:], "/"), nil
}

func (d *DockerVolumes) List(path string) (Listing, error) {
	return d.ListWithOptions(path, ListOptions{})
}
func (d *DockerVolumes) ListWithOptions(path string, options ListOptions) (Listing, error) {
	state := d.StateFor(path)
	if state.Status == "unavailable" {
		return Listing{}, fmt.Errorf("%s", state.Reason)
	}
	if path == "" {
		items, err := d.docker.ListStorageVolumes(context.Background())
		if err != nil {
			return Listing{}, err
		}
		entries := make([]Entry, 0, len(items))
		for _, item := range items {
			entries = append(entries, Entry{Name: item.Name, Path: item.Name, Type: "directory"})
		}
		sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name) })
		caps := d.CapabilitiesFor(path)
		return Listing{Path: "", Entries: entries, Capabilities: &caps, State: &state}, nil
	}
	volume, inside, err := splitDockerVolumePath(path)
	if err != nil {
		return Listing{}, err
	}
	entries, err := d.docker.ListDockerVolume(context.Background(), volume, inside, options)
	if err != nil {
		return Listing{}, err
	}
	for i := range entries {
		entries[i].Path = volume + "/" + strings.TrimPrefix(entries[i].Path, "/")
	}
	parent := ""
	if strings.Contains(path, "/") {
		parent = path[:strings.LastIndex(path, "/")]
	}
	caps := d.CapabilitiesFor(path)
	return Listing{Path: path, ParentPath: &parent, Entries: entries, Capabilities: &caps, State: &state}, nil
}

func (d *DockerVolumes) Open(path string) (*ObjectReader, error) {
	if !d.CapabilitiesFor(path).Download {
		return nil, fmt.Errorf("download is not supported")
	}
	volume, inside, err := splitDockerVolumePath(path)
	if err != nil || inside == "" {
		return nil, fmt.Errorf("invalid Docker volume file path")
	}
	data, err := d.docker.ReadDockerVolume(context.Background(), volume, inside)
	if err != nil {
		return nil, err
	}
	size := int64(len(data))
	name := inside[strings.LastIndex(inside, "/")+1:]
	return &ObjectReader{Reader: io.NopCloser(bytes.NewReader(data)), Name: name, Size: &size}, nil
}

func (d *DockerVolumes) ReadText(path string) (string, error) {
	object, err := d.Open(path)
	if err != nil {
		return "", err
	}
	defer object.Reader.Close()
	data, err := io.ReadAll(object.Reader)
	return string(data), err
}

// Mutating Docker-volume contents is intentionally deferred until ownership
// semantics can be preserved safely through the helper protocol.
func (d *DockerVolumes) WriteText(string, string) error {
	return fmt.Errorf("editing Docker volume contents is not supported")
}

// Docker-volume backups must later stream or stage through Docker. Returning a
// host mountpoint here would recreate the unsupported access model.
func (d *DockerVolumes) ResolveBackupSource(string) (BackupSource, error) {
	return BackupSource{}, fmt.Errorf("Docker volume backup sources require Docker-mediated export support")
}
