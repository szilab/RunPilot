package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/szilab/RunPilot/internal/core"
	webui "github.com/szilab/RunPilot/internal/web"
)

// Options override the corresponding YAML server settings for this run.
// Zero-value fields leave the YAML setting unchanged.
type Options struct {
	Port     int
	BasePath string
}

func Run(ctx context.Context, dataDir string, options ...Options) error {
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
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("web server: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
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
