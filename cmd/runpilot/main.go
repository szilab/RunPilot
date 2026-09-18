package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/daemon"
	"github.com/szilab/RunPilot/internal/service"
)

var version = "1.0.1"

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
		dataDir := fs.String("data-dir", config.DefaultDataDir(), "RunPilot data directory")
		port := fs.Int("port", 0, "HTTP port (overrides YAML)")
		basePath := fs.String("base-path", "", "URL base path, for example /runpilot (overrides YAML)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return service.Run(*dataDir, daemon.Options{Port: *port, BasePath: *basePath})
	case "service":
		return runServiceCommand(args[1:])
	case "version", "--version", "-v":
		fmt.Println("RunPilot", version)
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
	dataDir := fs.String("data-dir", config.DefaultDataDir(), "RunPilot data directory")
	port := fs.Int("port", 0, "HTTP port (overrides YAML)")
	basePath := fs.String("base-path", "", "URL base path, for example /runpilot (overrides YAML)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.Run(ctx, *dataDir, daemon.Options{Port: *port, BasePath: *basePath})
}

func runServiceCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("service subcommand required: install|uninstall|start|stop")
	}
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("service install", flag.ContinueOnError)
		dataDir := fs.String("data-dir", config.DefaultDataDir(), "RunPilot data directory")
		port := fs.Int("port", 0, "HTTP port (overrides YAML)")
		basePath := fs.String("base-path", "", "URL base path, for example /runpilot (overrides YAML)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return service.Install(*dataDir, daemon.Options{Port: *port, BasePath: *basePath})
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
	fmt.Print(`RunPilot - cross-platform process manager and task scheduler

Usage:
  runpilot run [--data-dir PATH] [--port N] [--base-path PATH]
                                                Run in the foreground
  runpilot service install [--data-dir PATH] [--port N] [--base-path PATH]
                                                Install the native service
  runpilot service start                     Start the native service
  runpilot service stop                      Stop the native service
  runpilot service uninstall                 Remove the native service
  runpilot version                           Print version

With no arguments RunPilot starts in foreground mode.
`)
}
