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
	mu   sync.RWMutex
	path string
	cfg  model.Config
}

var userHomeDir = os.UserHomeDir

func DefaultDataDir() string {
	if v := os.Getenv("RUNPILOT_DATA_DIR"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if p := os.Getenv("ProgramData"); p != "" {
			return filepath.Join(p, "RunPilot")
		}
	}
	if runtime.GOOS == "linux" {
		if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
			return filepath.Join(dataHome, "runpilot")
		}
		if home, err := userHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", "runpilot")
		}
		return ""
	}
	return filepath.Join(".", "runpilot-data")
}

func Open(dataDir string) (*Store, error) {
	var err error
	dataDir, err = ResolveDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "runpilot.yaml")}
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		config, err := defaultConfig()
		if err != nil {
			return nil, err
		}
		s.cfg = config
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
	// Storage used to be persisted configuration. Decode it only so opening an
	// older file can rewrite it without the obsolete section; locations are now
	// discovered at runtime by the Storage registry. A valid token without the
	// new setup marker is an existing installation and remains initialized.
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	_, hadLegacyStorage := raw["storage"]
	_, hadLegacyProcesses := raw["processes"]
	serverRaw, _ := raw["server"].(map[string]any)
	_, hadSetupMarker := serverRaw["setupComplete"]
	_, hadBearerAuthSetting := serverRaw["bearerAuthEnabled"]
	if !hadSetupMarker && s.cfg.Server.Token != "" {
		s.cfg.Server.SetupComplete = true
	}
	// Preserve the established API-token integration path on upgrades. Fresh
	// configurations persist false, so bearer use remains an explicit choice.
	if !hadBearerAuthSetting && s.cfg.Server.SetupComplete {
		s.cfg.Server.BearerAuthEnabled = true
	}
	before := clone(s.cfg)
	normalize(&s.cfg)
	if before.Server.Token == "" {
	}
	if hadLegacyStorage || hadLegacyProcesses || !hadBearerAuthSetting || !reflect.DeepEqual(before, s.cfg) {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Path() string { return s.path }

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
	if err := normalize(&next); err != nil {
		return err
	}
	prev := s.cfg
	s.cfg = next
	if err := s.saveLocked(); err != nil {
		s.cfg = prev
		return err
	}
	return nil
}

// ConfigureAdmin creates the single local administrator exactly once.
func (s *Store) ConfigureAdmin(username, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Server.AdminUsername != "" || s.cfg.Server.AdminPasswordHash != "" {
		return errors.New("administrator account is already configured")
	}
	prev := s.cfg
	s.cfg.Server.AdminUsername = username
	s.cfg.Server.AdminPasswordHash = passwordHash
	s.cfg.Server.SetupComplete = true
	if err := s.saveLocked(); err != nil {
		s.cfg = prev
		return err
	}
	return nil
}

func (s *Store) ChangeAdminPassword(username, currentHash, nextHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Server.AdminUsername != username || s.cfg.Server.AdminPasswordHash != currentHash || nextHash == "" {
		return errors.New("administrator account changed")
	}
	prev := s.cfg
	s.cfg.Server.AdminPasswordHash = nextHash
	if err := s.saveLocked(); err != nil {
		s.cfg = prev
		return err
	}
	return nil
}

func (s *Store) SetBearerAuth(enabled bool) error {
	return s.Update(func(cfg *model.Config) error { cfg.Server.BearerAuthEnabled = enabled; return nil })
}

func (s *Store) ResetAdmin() error {
	return s.Update(func(cfg *model.Config) error {
		cfg.Server.AdminUsername = ""
		cfg.Server.AdminPasswordHash = ""
		cfg.Server.SetupComplete = false
		return nil
	})
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

func defaultConfig() (model.Config, error) {
	token, err := randomToken()
	if err != nil {
		return model.Config{}, err
	}
	config := model.Config{
		Version: 1,
		Server: model.ServerConfig{
			Bind:          "127.0.0.1:9070",
			Port:          9070,
			BasePath:      "/",
			Token:         token,
			SetupComplete: false,
		},
		Jobs:    []model.JobDefinition{},
		Plugins: map[string]model.PluginSettings{},
	}
	if runtime.GOOS == "windows" {
		config.Software.Providers = []model.SoftwareProviderDefinition{{
			ID: "scoop", Name: "RunPilot Scoop", Type: model.SoftwareProviderScoop,
			Scoop: &model.ScoopProviderSpec{},
		}}
	}
	return config, nil
}

func normalize(c *model.Config) error {
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
		token, err := randomToken()
		if err != nil {
			return err
		}
		c.Server.Token = token
	}
	if c.Server.AdminUsername == "" || c.Server.AdminPasswordHash == "" {
		c.Server.AdminUsername = ""
		c.Server.AdminPasswordHash = ""
	}
	if c.Plugins == nil {
		c.Plugins = map[string]model.PluginSettings{}
	}
	if runtime.GOOS == "windows" {
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
	}
	jobs := c.Jobs[:0]
	for i := range c.Jobs {
		if c.Jobs[i].Type != model.JobBackup {
			continue
		}
		model.NormalizeJob(&c.Jobs[i])
		jobs = append(jobs, c.Jobs[i])
	}
	c.Jobs = jobs
	// RDP and VNC targets now belong to their plugin-owned storage. Remove
	// obsolete combined-core targets once when opening an older configuration.
	targets := c.RemoteTargets[:0]
	for _, target := range c.RemoteTargets {
		if target.Provider == "rdp" || target.Provider == "vnc" {
			continue
		}
		targets = append(targets, target)
	}
	c.RemoteTargets = targets
	for i := range c.RemoteTargets {
		target := &c.RemoteTargets[i]
		if target.DBusMode != model.RemoteDBusIsolated && target.DBusMode != model.RemoteDBusHost {
			if target.ForwardDBus {
				target.DBusMode = model.RemoteDBusHost
			} else {
				target.DBusMode = model.RemoteDBusIsolated
			}
		}
		// Remove the compatibility field on the next normal configuration save.
		target.ForwardDBus = false
	}
	return nil
}

func clone(in model.Config) model.Config {
	b, _ := yaml.Marshal(in)
	var out model.Config
	_ = yaml.Unmarshal(b, &out)
	return out
}

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate initial token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
