package core

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/szilab/RunPilot/internal/model"
)

const maxLauncherEntries = 64

// SaveLauncher validates and atomically replaces the ordered shortcuts.
// Existing IDs must belong to the current configuration; new entries get IDs here.
func (c *Controller) SaveLauncher(entries []model.LauncherEntry) ([]model.LauncherEntry, error) {
	if len(entries) > maxLauncherEntries {
		return nil, fmt.Errorf("launcher supports at most %d entries", maxLauncherEntries)
	}
	var saved []model.LauncherEntry
	err := c.config.Update(func(cfg *model.Config) error {
		known := make(map[string]bool, len(cfg.Launcher))
		for _, entry := range cfg.Launcher {
			known[entry.ID] = true
		}
		seen := make(map[string]bool, len(entries))
		saved = make([]model.LauncherEntry, 0, len(entries))
		for _, entry := range entries {
			entry.Name = strings.TrimSpace(entry.Name)
			entry.URL = strings.TrimSpace(entry.URL)
			entry.Icon = strings.TrimSpace(entry.Icon)
			if entry.Name == "" || len(entry.Name) > 100 {
				return errors.New("launcher name must be 1–100 characters")
			}
			if len(entry.URL) > 2048 || !validLauncherURL(entry.URL, entry.OpenMode) {
				return errors.New("launcher URL must be HTTP(S), or a /p/ path in RunPilot mode")
			}
			if len(entry.Icon) > 2048 || (entry.Icon != "" && !validLauncherURL(entry.Icon, "external")) {
				return errors.New("launcher icon must be an HTTP(S) URL")
			}
			if entry.ID == "" {
				var bytes [16]byte
				if _, err := rand.Read(bytes[:]); err != nil {
					return err
				}
				entry.ID = hex.EncodeToString(bytes[:])
			} else if !known[entry.ID] {
				return errors.New("unknown launcher ID")
			}
			if seen[entry.ID] {
				return errors.New("duplicate launcher ID")
			}
			seen[entry.ID] = true
			saved = append(saved, entry)
		}
		cfg.Launcher = saved
		return nil
	})
	return saved, err
}

func validLauncherURL(raw, mode string) bool {
	if mode != "external" && mode != "runpilot" {
		return false
	}
	if mode == "runpilot" {
		u, err := url.Parse(raw)
		return err == nil && strings.HasPrefix(u.Path, "/p/") && strings.HasPrefix(path.Clean(u.Path), "/p/") && !strings.ContainsAny(raw, "\\\r\n") && u.Host == ""
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && !strings.ContainsAny(raw, "\r\n")
}
