package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/core"
	webui "github.com/szilab/RunPilot/internal/web"
)

// ErrRestartRequested signals that RunPilot should reopen its controller and
// web server while retaining the process arguments and service configuration.
var ErrRestartRequested = errors.New("RunPilot restart requested")

// Options override the corresponding YAML server settings for this run.
// Zero-value fields leave the YAML setting unchanged.
type Options struct {
	Port     int
	BasePath string
}

func Run(ctx context.Context, dataDir string, options ...Options) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var restartOnce sync.Once
	restartRequested := make(chan struct{})
	ctrl, err := core.Open(dataDir)
	if err != nil {
		return err
	}
	defer ctrl.Close()
	ctrl.Start()

	cfg := ctrl.Snapshot()
	option := Options{}
	if len(options) > 0 {
		option = options[0]
	}
	addr, err := listenAddr(cfg.Server.Bind, cfg.Server.Port, option.Port)
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
	ui.SetRestartHandler(func() {
		restartOnce.Do(func() {
			close(restartRequested)
			cancel()
		})
	})
	defer ui.Close()
	if ctrl.TokenCreated() {
		log.Printf("RunPilot API token (save it now): %s", cfg.Server.Token)
	}
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
		return ErrRestartRequested
	default:
		return nil
	}
}

func listenAddr(bind string, configuredPort, overridePort int) (string, error) {
	port := configuredPort
	if overridePort != 0 {
		port = overridePort
	}
	if port == 0 {
		return bind, nil // Legacy server.bind values include the port.
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("server port must be between 1 and 65535")
	}
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		host = bind
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}
