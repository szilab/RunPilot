package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"

	"github.com/szilab/RunPilot/internal/model"
	"gopkg.in/yaml.v3"
)

type Store struct {
	mu           sync.RWMutex
	path         string
	cfg          model.Config
	tokenCreated bool
}

func DefaultDataDir() string {
	if v := os.Getenv("RUNPILOT_DATA_DIR"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if p := os.Getenv("ProgramData"); p != "" {
			return filepath.Join(p, "RunPilot")
		}
	}
	return filepath.Join(".", "runpilot-data")
}

func Open(dataDir string) (*Store, error) {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "runpilot.yaml")}
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		s.cfg = defaultConfig()
		s.tokenCreated = true
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
		return s, nil
	} else if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, &s.cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	before := clone(s.cfg)
	normalize(&s.cfg)
	if before.Server.Token == "" {
		s.tokenCreated = true
	}
	if !reflect.DeepEqual(before, s.cfg) {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Path() string { return s.path }

// TokenCreated reports whether this open generated an API token. It is true
// only for a newly initialized or repaired configuration.
func (s *Store) TokenCreated() bool { return s.tokenCreated }

func (s *Store) Snapshot() model.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.cfg)
}

func (s *Store) Update(fn func(*model.Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.cfg)
	if err := fn(&next); err != nil {
		return err
	}
	normalize(&next)
	prev := s.cfg
	s.cfg = next
	if err := s.saveLocked(); err != nil {
		s.cfg = prev
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	b, err := yaml.Marshal(s.cfg)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func defaultConfig() model.Config {
	return model.Config{
		Version: 1,
		Server: model.ServerConfig{
			Bind:     "127.0.0.1:9070",
			Port:     9070,
			BasePath: "/",
			Token:    randomToken(),
		},
		Processes: []model.ProcessDefinition{},
		Jobs:      []model.JobDefinition{},
		Storage: []model.StorageDefinition{{
			ID: "storage-local", Name: "Helyi fájlrendszer", Type: model.StorageLocal,
			Local: &model.LocalStorageSpec{Scope: model.LocalStorageScopeHost},
		}},
		Software: model.SoftwareConfig{Providers: []model.SoftwareProviderDefinition{{
			ID: "scoop", Name: "RunPilot Scoop", Type: model.SoftwareProviderScoop,
			Scoop: &model.ScoopProviderSpec{},
		}}},
	}
}

func normalize(c *model.Config) {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Server.Bind == "" {
		c.Server.Bind = "127.0.0.1:9070"
	}
	if c.Server.BasePath == "" {
		c.Server.BasePath = "/"
	}
	if c.Server.Token == "" {
		c.Server.Token = randomToken()
	}
	hasLocalFilesystem := false
	for _, storage := range c.Storage {
		if storage.ID == "storage-local" {
			hasLocalFilesystem = true
			break
		}
	}
	if !hasLocalFilesystem {
		c.Storage = append(c.Storage, model.StorageDefinition{ID: "storage-local", Name: "Helyi fájlrendszer", Type: model.StorageLocal, Local: &model.LocalStorageSpec{Scope: model.LocalStorageScopeHost}})
	}
	hasScoop := false
	for _, provider := range c.Software.Providers {
		if provider.ID == "scoop" {
			hasScoop = true
			break
		}
	}
	if !hasScoop {
		c.Software.Providers = append(c.Software.Providers, model.SoftwareProviderDefinition{
			ID: "scoop", Name: "RunPilot Scoop", Type: model.SoftwareProviderScoop,
			Scoop: &model.ScoopProviderSpec{},
		})
	}
	for i := range c.Processes {
		model.NormalizeProcess(&c.Processes[i])
	}
	for i := range c.Jobs {
		model.NormalizeJob(&c.Jobs[i])
	}
}

func clone(in model.Config) model.Config {
	b, _ := yaml.Marshal(in)
	var out model.Config
	_ = yaml.Unmarshal(b, &out)
	return out
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "change-me"
	}
	return hex.EncodeToString(b)
}
