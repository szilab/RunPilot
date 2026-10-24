package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/core"
	webui "github.com/szilab/RunPilot/internal/web"
)

// ErrRestartRequested signals that RunPilot should reopen its controller and
// web server while retaining the process arguments and service configuration.
var ErrRestartRequested = errors.New("RunPilot restart requested")

// Options override the corresponding YAML server settings for this run.
// Zero-value fields leave the YAML setting unchanged.
type Options struct {
	Port        int
	BindAddress string
	BasePath    string
	ConfigDir   string
}

func Run(ctx context.Context, dataDir string, options ...Options) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var restartOnce sync.Once
	restartRequested := make(chan struct{})
	ctrl, err := core.OpenWithConfigDir(dataDir, optionConfigDir(options))
	if err != nil {
		return err
	}
	ctrlClosed := false
	defer func() {
		if !ctrlClosed {
			ctrl.Close()
		}
	}()

	cfg := ctrl.Snapshot()
	option := Options{}
	if len(options) > 0 {
		option = options[0]
	}
	addr, err := listenAddr(cfg.Server.Bind, cfg.Server.Port, option.Port, option.BindAddress)
	if err != nil {
		return err
	}
	basePath := cfg.Server.BasePath
	if option.BasePath != "" {
		basePath = option.BasePath
	}
	ui, err := webui.New(ctrl, basePath)
	if err != nil {
		return err
	}
	ui.SetDataRootSwitchAllowed(dataDir == "" && os.Getenv("RUNPILOT_DATA_DIR") == "")
	if err := ui.ConfigureProxyAuth(os.Getenv(webui.TrustedProxiesEnv), os.Getenv(webui.ProxyAuthHeaderEnv)); err != nil {
		return err
	}
	if header, proxies, ok := ui.ProxyAuthSummary(); ok {
		log.Printf("RunPilot proxy authentication: %s from %s", header, strings.Join(proxies, ", "))
	}
	ui.SetRestartHandler(func() {
		restartOnce.Do(func() {
			close(restartRequested)
			cancel()
		})
	})
	uiClosed := false
	defer func() {
		if !uiClosed {
			_ = ui.Close()
		}
	}()
	server := &http.Server{
		Addr:              addr,
		Handler:           ui.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("RunPilot listening on http://%s%s", addr, ui.BasePath())
		log.Printf("RunPilot data directory: %s", ctrl.DataDir())
		log.Printf("RunPilot config: %s", ctrl.ConfigPath())
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-runCtx.Done():
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("web server: %w", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	select {
	case <-restartRequested:
		if err := ui.Close(); err != nil {
			log.Printf("RunPilot web shutdown: %v", err)
		}
		uiClosed = true
		ctrl.Close()
		ctrlClosed = true
		if err := config.ApplyPendingDataRoot(); err != nil {
			log.Printf("RunPilot data-root relocation failed; retaining source data: %v", err)
		}
		return ErrRestartRequested
	default:
	}
	return nil
}

func optionConfigDir(options []Options) string {
	if len(options) == 0 {
		return ""
	}
	return options[0].ConfigDir
}

func listenAddr(bind string, configuredPort, overridePort int, overrideAddress string) (string, error) {
	port := configuredPort
	if overridePort != 0 {
		port = overridePort
	}
	if port == 0 && overrideAddress == "" {
		return bind, nil // Legacy server.bind values include the port.
	}
	if port != 0 && (port < 1 || port > 65535) {
		return "", fmt.Errorf("server port must be between 1 and 65535")
	}
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		host = bind
	}
	if overrideAddress != "" {
		if strings.TrimSpace(overrideAddress) != overrideAddress || net.ParseIP(strings.Trim(overrideAddress, "[]")) == nil {
			return "", fmt.Errorf("invalid bind address %q", overrideAddress)
		}
		host = strings.Trim(overrideAddress, "[]")
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if port == 0 {
		_, portText, splitErr := net.SplitHostPort(bind)
		if splitErr != nil {
			return "", fmt.Errorf("server port is required when overriding the bind address")
		}
		portNumber, parseErr := strconv.Atoi(portText)
		if parseErr != nil || portNumber < 1 || portNumber > 65535 {
			return "", fmt.Errorf("server port must be between 1 and 65535")
		}
		port = portNumber
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}
