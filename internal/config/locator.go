package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LocatorPath identifies the small deployment record independently of dataDir.
func LocatorPath() string {
	if path := os.Getenv("RUNPILOT_DATA_ROOT_FILE"); path != "" {
		return path
	}
	if runtime.GOOS == "windows" {
		legacy := legacyWindowsLocatorPath()
		if legacy != "" {
			if _, err := os.Lstat(legacy); err == nil {
				return legacy
			}
		}
		if root := os.Getenv("APPDATA"); root != "" {
			return filepath.Join(root, "RunPilot", "data-root")
		}
		if home, err := userHomeDir(); err == nil {
			return filepath.Join(home, "AppData", "Roaming", "RunPilot", "data-root")
		}
		return ""
	}
	if root := DefaultConfigDir(); root != "" {
		return filepath.Join(root, "runpilot", "data-root")
	}
	return ""
}

func legacyWindowsLocatorPath() string {
	if root := os.Getenv("ProgramData"); root != "" {
		return filepath.Join(root, "RunPilot", "data-root")
	}
	return ""
}

// DefaultConfigDir returns the platform configuration directory without creating it.
func DefaultConfigDir() string {
	if runtime.GOOS == "windows" {
		if root := os.Getenv("APPDATA"); root != "" {
			return root
		}
		if home, err := userHomeDir(); err == nil {
			return filepath.Join(home, "AppData", "Roaming")
		}
		return ""
	}
	if root := os.Getenv("XDG_CONFIG_HOME"); root != "" {
		return root
	}
	if home, err := userHomeDir(); err == nil {
		return filepath.Join(home, ".config")
	}
	return ""
}

// ResolveDataDir applies explicit CLI, environment, persisted locator, then platform default precedence.
func ResolveDataDir(explicit string) (string, error) {
	candidate := strings.TrimSpace(explicit)
	if candidate == "" {
		candidate = strings.TrimSpace(os.Getenv("RUNPILOT_DATA_DIR"))
	}
	if candidate == "" {
		if locator := LocatorPath(); locator != "" {
			if content, err := os.ReadFile(locator); err == nil {
				candidate = strings.TrimSpace(string(content))
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("read data-root locator: %w", err)
			}
		}
	}
	if candidate == "" {
		if runtime.GOOS == "windows" {
			legacy := filepath.Join(os.Getenv("ProgramData"), "RunPilot")
			if os.Getenv("ProgramData") != "" {
				if _, err := os.Stat(filepath.Join(legacy, "runpilot.yaml")); err == nil {
					candidate = legacy
				}
			}
		}
		if candidate == "" {
			candidate = DefaultDataDir()
		}
	}
	if candidate == "" {
		return "", errors.New("resolve default data directory: home directory is unavailable")
	}
	if !filepath.IsAbs(candidate) {
		return "", errors.New("RunPilot data directory must be an absolute path")
	}
	return ValidateDataRoot(filepath.Clean(candidate))
}

// WriteDataRoot atomically persists a validated absolute path in bootstrap metadata.
func WriteDataRoot(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("RunPilot data directory must be an absolute path")
	}
	locator := LocatorPath()
	if locator == "" {
		return errors.New("persistent data-root locator is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(locator), 0o700); err != nil {
		return err
	}
	tmp := locator + ".tmp"
	if err := os.WriteFile(tmp, []byte(filepath.Clean(path)+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, locator); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
