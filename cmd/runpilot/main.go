package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/daemon"
	"github.com/szilab/RunPilot/internal/service"
	"github.com/szilab/RunPilot/internal/version"
)

func main() {
	if err := run(); err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) == 0 {
		return runForeground([]string{})
	}
	switch args[0] {
	case "run":
		return runForeground(args[1:])
	case "service-run":
		fs := flag.NewFlagSet("service-run", flag.ContinueOnError)
		dataDir := fs.String("data-dir", "", "RunPilot data directory override")
		port := fs.Int("port", 0, "HTTP port (overrides YAML)")
		bindAddress := fs.String("bind-address", "", "HTTP bind address (overrides YAML)")
		basePath := fs.String("base-path", "", "URL base path, for example /runpilot (overrides YAML)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return service.Run(*dataDir, daemon.Options{Port: *port, BindAddress: *bindAddress, BasePath: *basePath})
	case "service":
		return runServiceCommand(args[1:])
	case "reset-admin":
		fs := flag.NewFlagSet("reset-admin", flag.ContinueOnError)
		dataDir := fs.String("data-dir", "", "RunPilot data directory override")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		resolved, err := config.ResolveDataDir(*dataDir)
		if err != nil {
			return err
		}
		store, err := config.Open(resolved)
		if err != nil {
			return err
		}
		if err := store.ResetAdmin(); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Administrator account reset. Stop RunPilot first, restart it, then complete administrator setup within five minutes.")
		return nil
	case "version", "--version", "-v":
		fmt.Println("RunPilot", version.Version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runForeground(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "RunPilot data directory override")
	port := fs.Int("port", 0, "HTTP port (overrides YAML)")
	bindAddress := fs.String("bind-address", "", "HTTP bind address (overrides YAML)")
	basePath := fs.String("base-path", "", "URL base path, for example /runpilot (overrides YAML)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	option := daemon.Options{Port: *port, BindAddress: *bindAddress, BasePath: *basePath}
	for {
		err := daemon.Run(ctx, *dataDir, option)
		if errors.Is(err, daemon.ErrRestartRequested) {
			log.Printf("RunPilot restarting")
			continue
		}
		return err
	}
}

func runServiceCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("service subcommand required: install|uninstall|start|stop")
	}
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("service install", flag.ContinueOnError)
		dataDir := fs.String("data-dir", "", "RunPilot data directory override")
		port := fs.Int("port", 0, "HTTP port (overrides YAML)")
		bindAddress := fs.String("bind-address", "", "HTTP bind address (overrides YAML)")
		basePath := fs.String("base-path", "", "URL base path, for example /runpilot (overrides YAML)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		installDir := *dataDir
		if installDir == "" && os.Getenv("RUNPILOT_DATA_DIR") != "" {
			installDir = os.Getenv("RUNPILOT_DATA_DIR")
		}
		if installDir != "" {
			var err error
			installDir, err = config.ResolveDataDir(installDir)
			if err != nil {
				return err
			}
		}
		return service.Install(installDir, daemon.Options{Port: *port, BindAddress: *bindAddress, BasePath: *basePath})
	case "uninstall":
		return service.Uninstall()
	case "start":
		return service.Start()
	case "stop":
		return service.Stop()
	default:
		return fmt.Errorf("unknown service subcommand %q", args[0])
	}
}

func usage() {
	fmt.Print(`RunPilot - cross-platform plugin host and management framework

Usage:
  runpilot run [--data-dir PATH] [--bind-address IP] [--port N] [--base-path PATH]
                                                Run in the foreground
  runpilot service install [--data-dir PATH] [--bind-address IP] [--port N] [--base-path PATH]
                                                Install the native service
  runpilot service start                     Start the native service
  runpilot service stop                      Stop the native service
  runpilot service uninstall                 Remove the native service
  runpilot reset-admin [--data-dir PATH]    Reset admin; setup is available for five minutes after restart
  runpilot version                           Print version

With no arguments RunPilot starts in foreground mode.
`)
}
