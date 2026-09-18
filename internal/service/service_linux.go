//go:build linux

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/szilab/RunPilot/internal/daemon"
)

const (
	Name        = "runpilot"
	systemdUnit = Name + ".service"
)

var (
	userHomeDir     = os.UserHomeDir
	currentUserName = func() (string, error) {
		account, err := user.Current()
		if err != nil {
			return "", err
		}
		return account.Username, nil
	}
	runSystemctl = func(args ...string) ([]byte, error) {
		return exec.Command("systemctl", args...).CombinedOutput()
	}
	runLoginctl = func(args ...string) ([]byte, error) {
		return exec.Command("loginctl", args...).CombinedOutput()
	}
)

func Install(dataDir string, options ...daemon.Options) error {
	if dataDir == "" {
		return fmt.Errorf("data directory is unavailable; set --data-dir or RUNPILOT_DATA_DIR")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	var option daemon.Options
	if len(options) > 0 {
		option = options[0]
	}
	dir, err := userSystemdDir()
	if err != nil {
		return fmt.Errorf("resolve systemd user directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}
	path := filepath.Join(dir, systemdUnit)
	if err := os.WriteFile(path, []byte(unitContents(exe, dataDir, option)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", systemdUnit); err != nil {
		return err
	}
	printLingerNotice()
	return nil
}

func Uninstall() error {
	_ = systemctl("disable", "--now", systemdUnit)
	dir, err := userSystemdDir()
	if err != nil {
		return fmt.Errorf("resolve systemd user directory: %w", err)
	}
	path := filepath.Join(dir, systemdUnit)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return systemctl("daemon-reload")
}
func Start() error { return systemctl("start", systemdUnit) }
func Stop() error  { return systemctl("stop", systemdUnit) }

// Run is invoked by systemd and deliberately uses the same daemon path as
// foreground operation so scheduling and process supervision stay identical.
func Run(dataDir string, options ...daemon.Options) error {
	option := daemon.Options{}
	if len(options) > 0 {
		option = options[0]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.Run(ctx, dataDir, option)
}

func systemctl(args ...string) error {
	args = append([]string{"--user"}, args...)
	out, err := runSystemctl(args...)
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args[1:], " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func userSystemdDir() (string, error) {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "systemd", "user"), nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func lingerEnabled(username string) (bool, error) {
	out, err := runLoginctl("show-user", username, "-p", "Linger", "--value")
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(string(out))) {
	case "yes", "true", "1":
		return true, nil
	case "no", "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected Linger value %q", strings.TrimSpace(string(out)))
	}
}

func printLingerNotice() {
	username, err := currentUserName()
	if err != nil {
		return
	}
	enabled, err := lingerEnabled(username)
	if err != nil || enabled {
		return
	}
	fmt.Printf("RunPilot was installed as a systemd user service.\n\nTo let it start at boot and continue running without an interactive login, enable lingering for this user:\n\n  sudo loginctl enable-linger %s\n", username)
}

func unitContents(executable, dataDir string, option daemon.Options) string {
	args := []string{unitQuote(executable), "service-run", "--data-dir", unitQuote(dataDir)}
	if option.Port != 0 {
		args = append(args, "--port", strconv.Itoa(option.Port))
	}
	if option.BasePath != "" {
		args = append(args, "--base-path", unitQuote(option.BasePath))
	}
	return "[Unit]\nDescription=RunPilot Process Manager\n\n[Service]\nType=simple\nWorkingDirectory=" + unitQuote(dataDir) + "\nExecStart=" + strings.Join(args, " ") + "\nRestart=on-failure\nRestartSec=2\n\n[Install]\nWantedBy=default.target\n"
}

func unitQuote(value string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}
