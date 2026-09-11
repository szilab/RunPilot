//go:build linux

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/szilab/RunPilot/internal/daemon"
)

const (
	Name        = "runpilot"
	systemdDir  = "/etc/systemd/system"
	systemdUnit = Name + ".service"
)

func Install(dataDir string, options ...daemon.Options) error {
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
	path := filepath.Join(systemdDir, systemdUnit)
	if err := os.WriteFile(path, []byte(unitContents(exe, dataDir, option)), 0o644); err != nil {
		return fmt.Errorf("write %s (run as root): %w", path, err)
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	return systemctl("enable", Name)
}

func Uninstall() error {
	_ = systemctl("disable", "--now", Name)
	path := filepath.Join(systemdDir, systemdUnit)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return systemctl("daemon-reload")
}
func Start() error { return systemctl("start", Name) }
func Stop() error  { return systemctl("stop", Name) }

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
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func unitContents(executable, dataDir string, option daemon.Options) string {
	args := []string{unitQuote(executable), "service-run", "--data-dir", unitQuote(dataDir)}
	if option.Port != 0 {
		args = append(args, "--port", strconv.Itoa(option.Port))
	}
	if option.BasePath != "" {
		args = append(args, "--base-path", unitQuote(option.BasePath))
	}
	return "[Unit]\nDescription=RunPilot Process Manager\nAfter=network.target\n\n[Service]\nType=simple\nWorkingDirectory=" + unitQuote(dataDir) + "\nExecStart=" + strings.Join(args, " ") + "\nRestart=on-failure\nRestartSec=2\n\n[Install]\nWantedBy=multi-user.target\n"
}

func unitQuote(value string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}
