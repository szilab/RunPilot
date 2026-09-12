package storage

import (
	"context"
	"errors"
	"testing"
)

type fakeDockerVolumes struct {
	state   State
	volumes []DockerVolumeDescriptor
	entries map[string][]Entry
	reads   map[string][]byte
	err     error
}

func (f *fakeDockerVolumes) DockerStorageState(context.Context) State { return f.state }
func (f *fakeDockerVolumes) ListStorageVolumes(context.Context) ([]DockerVolumeDescriptor, error) {
	return f.volumes, f.err
}
func (f *fakeDockerVolumes) ListDockerVolume(_ context.Context, volume, path string, _ ListOptions) ([]Entry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entries[volume+":"+path], nil
}
func (f *fakeDockerVolumes) ReadDockerVolume(_ context.Context, volume, path string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.reads[volume+":"+path], nil
}

func TestDockerVolumesIsOneVirtualStorageLocation(t *testing.T) {
	fake := &fakeDockerVolumes{state: State{Status: "ready"}, volumes: []DockerVolumeDescriptor{{Name: "postgres_data"}, {Name: "jellyfin_config"}}}
	registry := NewRegistry(fake)
	locations := registry.List(context.Background())
	if len(locations) != 2 || locations[0].ID != "local" || locations[1].ID != "docker-volumes" || locations[1].Name != "Docker volumes" {
		t.Fatalf("locations = %#v", locations)
	}
	p, err := registry.Provider(context.Background(), "docker-volumes")
	if err != nil {
		t.Fatal(err)
	}
	root, err := p.List("")
	if err != nil || len(root.Entries) != 2 || root.Entries[0].Path != "jellyfin_config" || root.Entries[0].Type != "directory" {
		t.Fatalf("root = %#v, %v", root, err)
	}
	if root.Capabilities == nil || !root.Capabilities.Browse || root.Capabilities.Download {
		t.Fatalf("root capabilities = %#v", root.Capabilities)
	}
}

func TestDockerVolumesPathsAndReadOnlyCapabilities(t *testing.T) {
	fake := &fakeDockerVolumes{state: State{Status: "ready"}, volumes: []DockerVolumeDescriptor{{Name: "app", Running: true}}, entries: map[string][]Entry{"app:config": {{Name: "settings.yml", Path: "config/settings.yml", Type: "file"}}}, reads: map[string][]byte{"app:config/settings.yml": []byte("ok")}}
	p := NewDockerVolumes(fake)
	for _, value := range []string{"../x", "/app/x", "app//x", "app/../x", "app\\x"} {
		if _, _, err := splitDockerVolumePath(value); err == nil {
			t.Fatalf("accepted unsafe path %q", value)
		}
	}
	listing, err := p.List("app/config")
	if err != nil || listing.ParentPath == nil || *listing.ParentPath != "app" || listing.State == nil || listing.State.Status != "read-only" {
		t.Fatalf("listing = %#v, %v", listing, err)
	}
	if caps := p.CapabilitiesFor("app/config"); !caps.Browse || !caps.Download || caps.Upload {
		t.Fatalf("caps = %#v", caps)
	}
	object, err := p.Open("app/config/settings.yml")
	if err != nil {
		t.Fatal(err)
	}
	object.Reader.Close()
}

func TestDockerVolumesNeverUsesHostMountpoint(t *testing.T) {
	// This fake has no mountpoint or Local adapter. Reads only reach the
	// Docker-mediated accessor, even when the host could not access a volume.
	fake := &fakeDockerVolumes{state: State{Status: "ready"}, reads: map[string][]byte{"private:secret": []byte("through Docker")}}
	p := NewDockerVolumes(fake)
	if _, err := p.Open("private/secret"); err != nil {
		t.Fatal(err)
	}
	fake.err = errors.New("helper mount failed")
	if _, err := p.Open("private/secret"); err == nil {
		t.Fatal("Docker helper failure was hidden")
	}
}
