// Package xpra implements the Linux Xpra remote-session provider.
package xpra

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/remote"
)

const providerID = "xpra"

type Provider struct {
	lookPath func(string) (string, error)
	run      func(context.Context, string, ...string) ([]byte, error)
}

func New() *Provider {
	return &Provider{lookPath: exec.LookPath, run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}}
}

func (p *Provider) ID() string { return providerID }

func (p *Provider) Status(ctx context.Context) model.RemoteProviderStatus {
	status := model.RemoteProviderStatus{ID: providerID, Name: "Xpra", Platform: runtime.GOOS, Capabilities: model.RemoteProviderCapabilities{ApplicationSessions: true, DesktopSessions: true, Clipboard: true, DynamicResize: true, Fullscreen: true}}
	if runtime.GOOS != "linux" {
		status.State = "unsupported"
		status.Message = "Xpra remote-session server support is currently Linux-only"
		return status
	}
	path, err := p.lookPath("xpra")
	if err != nil {
		status.State = "not-installed"
		status.Message = "The xpra executable was not found in PATH"
		return status
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := p.run(probe, path, "--version")
	if err != nil {
		status.State = "unavailable"
		status.Message = "Xpra could not be queried"
		return status
	}
	status.State = "available"
	status.Version = strings.TrimSpace(string(out))
	return status
}

func (p *Provider) Start(ctx context.Context, request remote.StartRequest) (remote.Runtime, error) {
	if runtime.GOOS != "linux" {
		return remote.Runtime{}, fmt.Errorf("Xpra remote-session server is unsupported on %s", runtime.GOOS)
	}
	xpra, err := p.lookPath("xpra")
	if err != nil {
		return remote.Runtime{}, fmt.Errorf("xpra executable was not found")
	}
	environment, err := childEnvironment(request.Target)
	if err != nil {
		return remote.Runtime{}, err
	}
	port, err := reservePort()
	if err != nil {
		return remote.Runtime{}, err
	}
	runtimeDir := filepath.Join(request.DataDir, "remote", request.ID)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return remote.Runtime{}, err
	}
	readFD, writeFD, err := os.Pipe()
	if err != nil {
		return remote.Runtime{}, err
	}
	defer readFD.Close()
	command := shellCommand(request.Target.Command)
	if command == "" {
		_ = writeFD.Close()
		return remote.Runtime{}, fmt.Errorf("remote target command is required")
	}
	mode := "start"
	if request.Target.Type == model.RemoteTargetDesktop {
		mode = "start-desktop"
	}
	args := []string{mode, "--daemon=no", "--mdns=no", "--html=on", "--bind-ws=127.0.0.1:" + port, "--socket-dir=" + runtimeDir, "--displayfd=3", "--resize-display=yes", "--start-child=" + command, "--exit-with-children=yes", "--start-new-commands=no"}
	for key, value := range environment {
		args = append(args, "--env="+key+"="+value)
	}
	cmd := exec.Command(xpra, args...)
	cmd.ExtraFiles = []*os.File{writeFD}
	cmd.Dir = request.Target.Command.WorkingDirectory
	var output boundedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		_ = writeFD.Close()
		return remote.Runtime{}, fmt.Errorf("start xpra: %w", err)
	}
	_ = writeFD.Close()
	// Xpra's documented displayfd lets it allocate a collision-free display.
	// The WS port is reserved immediately before launch because Xpra does not
	// report an automatically selected WS listener port to its parent.
	display, err := readDisplay(readFD, 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return remote.Runtime{}, fmt.Errorf("xpra did not report a display: %w (%s)", err, output.String())
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if err != nil {
			done <- fmt.Errorf("xpra exited: %w (%s)", err, output.String())
		} else {
			done <- nil
		}
		close(done)
		_ = os.RemoveAll(runtimeDir)
	}()
	endpoint := "127.0.0.1:" + port
	if err := waitEndpoint(ctx, endpoint, 15*time.Second, done); err != nil {
		_ = cmd.Process.Kill()
		return remote.Runtime{}, err
	}
	var stopOnce sync.Once
	stop := func(stopCtx context.Context) error {
		var stopErr error
		stopOnce.Do(func() {
			// Ask Xpra to stop the exact display through the private socket first.
			// The owned process is only interrupted/killed if that graceful request
			// cannot complete in time.
			_ = exec.CommandContext(stopCtx, xpra, "stop", "--socket-dir="+runtimeDir, display).Run()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Signal(os.Interrupt)
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					stopErr = cmd.Process.Kill()
				}
			}
		})
		return stopErr
	}
	return remote.Runtime{Endpoint: endpoint, Stop: stop, Done: done}, nil
}

func reservePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port)
	err = l.Close()
	return port, err
}
func readDisplay(file *os.File, timeout time.Duration) (string, error) {
	result := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		b := make([]byte, 64)
		n, err := file.Read(b)
		result <- struct {
			value string
			err   error
		}{strings.TrimSpace(string(b[:n])), err}
	}()
	select {
	case item := <-result:
		if item.err != nil {
			return "", item.err
		}
		if item.value == "" {
			return "", fmt.Errorf("empty display")
		}
		return item.value, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("timed out")
	}
}
func waitEndpoint(ctx context.Context, endpoint string, timeout time.Duration, done <-chan error) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := net.DialTimeout("tcp", endpoint, 150*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case err := <-done:
			if err == nil {
				return fmt.Errorf("xpra stopped before becoming ready")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("xpra did not become ready within %s", timeout)
		case <-ticker.C:
		}
	}
}
func shellCommand(spec model.CommandSpec) string {
	if strings.TrimSpace(spec.Path) == "" {
		return ""
	}
	words := append([]string{spec.Path}, spec.Args...)
	for i := range words {
		words[i] = shellQuote(words[i])
	}
	return strings.Join(words, " ")
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

// childEnvironment returns only explicitly configured application variables.
// Forwarding a D-Bus session is opt-in because it allows the target to contact
// services in the RunPilot process's desktop session.
func childEnvironment(target model.RemoteTarget) (map[string]string, error) {
	environment := make(map[string]string, len(target.Command.Environment)+1)
	for key, value := range target.Command.Environment {
		environment[key] = value
	}
	if !target.ForwardDBus {
		return environment, nil
	}
	if _, explicit := environment["DBUS_SESSION_BUS_ADDRESS"]; explicit {
		return environment, nil
	}
	address := strings.TrimSpace(os.Getenv("DBUS_SESSION_BUS_ADDRESS"))
	if address == "" {
		return nil, fmt.Errorf("D-Bus forwarding is enabled, but RunPilot has no DBUS_SESSION_BUS_ADDRESS; configure it in the target environment or run RunPilot in the desktop session")
	}
	environment["DBUS_SESSION_BUS_ADDRESS"] = address
	return environment, nil
}

type boundedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	const limit = 16 * 1024
	if b.b.Len() < limit {
		remaining := limit - b.b.Len()
		if len(p) > remaining {
			b.b.Write(p[:remaining])
		} else {
			b.b.Write(p)
		}
	}
	return len(p), nil
}
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.b.String())
}
