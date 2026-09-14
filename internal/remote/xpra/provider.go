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
	"regexp"
	"runtime"
	"sort"
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
	environ  func() []string
}

func New() *Provider {
	return &Provider{lookPath: exec.LookPath, environ: os.Environ, run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}}
}

func (p *Provider) ID() string { return providerID }

func (p *Provider) Status(ctx context.Context) model.RemoteProviderStatus {
	status := model.RemoteProviderStatus{ID: providerID, Name: "Xpra", Platform: runtime.GOOS, Capabilities: model.RemoteProviderCapabilities{ApplicationSessions: true, DesktopSessions: true, Clipboard: true, DynamicResize: true, Fullscreen: true}}
	defaults := model.DefaultXpraRemoteOptions()
	status.XpraDefaults = &defaults
	if runtime.GOOS != "linux" {
		status.State = "unsupported"
		status.Message = "Xpra remote-session server support is currently Linux-only"
		status.InstallHint = "Xpra server sessions are Linux-only. Use the built-in RDP provider for Windows desktop sessions."
		return status
	}
	path, err := p.lookPath("xpra")
	if err != nil {
		status.State = "not-installed"
		status.Message = "The xpra executable was not found in PATH"
		status.InstallHint = "Install Xpra with your distribution package manager (for example: sudo apt install xpra), then restart RunPilot."
		return status
	}
	versionProbe, cancelVersion := context.WithTimeout(ctx, 2*time.Second)
	out, err := p.run(versionProbe, path, "--version")
	cancelVersion()
	if err != nil {
		status.State = "unavailable"
		status.Message = "Xpra could not be queried"
		status.InstallHint = "Check that Xpra is installed and executable by the RunPilot service account, then restart RunPilot."
		return status
	}
	status.State = "available"
	status.Version = strings.TrimSpace(string(out))
	if _, err := p.lookPath("dbus-launch"); err == nil {
		status.DBusLaunchAvailable = true
	} else {
		status.Warnings = append(status.Warnings, "dbus-launch was not found; isolated sessions can run, but some modern GUI applications may need a session D-Bus launcher (often supplied by dbus-x11).")
	}
	configProbe, cancelConfig := context.WithTimeout(ctx, 2*time.Second)
	configOut, configErr := p.run(configProbe, path, "showsetting", "html")
	cancelConfig()
	status.HTML5Available = configErr == nil && strings.Contains(strings.ToLower(string(configOut)), "html")
	if !status.HTML5Available {
		status.Warnings = append(status.Warnings, "HTML5 support could not be confirmed; a session launch will report any missing Xpra HTML5 assets.")
	}
	status.Message = strings.Join(status.Warnings, " ")
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
	dbusMode := request.Target.DBusMode
	if dbusMode == "" {
		dbusMode = model.RemoteDBusIsolated
	}
	hostBus := environmentValue(p.environ(), "DBUS_SESSION_BUS_ADDRESS")
	serverEnvironment, err := sanitizedServerEnvironment(p.environ(), dbusMode, hostBus)
	if err != nil {
		return remote.Runtime{}, err
	}
	request.Target.DBusMode = dbusMode
	environment, err := childEnvironment(request.Target, hostBus)
	if err != nil {
		return remote.Runtime{}, err
	}
	dbusLaunch, dbusErr := p.lookPath("dbus-launch")
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
	options, err := model.NormalizeXpraRemoteOptions(request.Target.Xpra)
	if err != nil {
		_ = writeFD.Close()
		return remote.Runtime{}, err
	}
	args := sessionArgs(request.Target.Type, dbusMode, command, port, runtimeDir, dbusLaunch, dbusErr == nil, options)
	for _, key := range sortedKeys(environment) {
		value := environment[key]
		args = append(args, "--env="+key+"="+value)
	}
	cmd := exec.Command(xpra, args...)
	cmd.Env = serverEnvironment
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
	probe := func(probeCtx context.Context) (remote.SessionProbe, error) {
		// info accepts the exact private display/socket and exposes state.windows.
		// list-windows only lists every session in a socket directory on Xpra 6.
		out, err := exec.CommandContext(probeCtx, xpra, "info", "--socket-dir="+runtimeDir, display).CombinedOutput()
		if err != nil {
			return remote.SessionProbe{}, fmt.Errorf("query Xpra windows: %w: %s", err, strings.TrimSpace(string(out)))
		}
		count, found := windowCount(string(out))
		if !found {
			return remote.SessionProbe{}, fmt.Errorf("could not read Xpra window count")
		}
		if count == 0 {
			return remote.SessionProbe{WindowCount: 0, Message: "Xpra is ready, but no application windows are currently visible."}, nil
		}
		return remote.SessionProbe{WindowCount: count}, nil
	}
	return remote.Runtime{Client: remote.ClientDescriptor{Kind: remote.ClientXpraHTML5}, Endpoint: endpoint, ClientParams: clientParams(options), Stop: stop, Done: done, Probe: probe, Log: output.String}, nil
}

func sessionArgs(kind model.RemoteTargetType, dbusMode model.RemoteDBusMode, command, port, runtimeDir, dbusLaunch string, hasDBusLaunch bool, options model.XpraRemoteOptions) []string {
	resize, clipboard := "no", "no"
	if *options.DynamicResize {
		resize = "yes"
	}
	if *options.Clipboard {
		clipboard = "yes"
	}
	args := []string{"start", "--daemon=no", "--mdns=no", "--html=on", "--bind-ws=127.0.0.1:" + port, "--socket-dir=" + runtimeDir, "--displayfd=3", "--resize-display=" + resize, "--start-new-commands=no", "--clipboard=" + clipboard, "--file-transfer=no", "--printing=no", "--speaker=off", "--microphone=off"}
	if options.DPIMode != model.XpraDPIAuto {
		args = append(args, fmt.Sprintf("--dpi=%d", options.DPI))
	}
	if kind == model.RemoteTargetDesktop {
		args[0] = "start-desktop"
		if *options.LaunchAfterConnect {
			args = append(args, "--start-child-after-connect="+command, "--exit-with-children=yes")
		} else {
			args = append(args, "--start-child="+command, "--exit-with-children=yes")
		}
	} else {
		// Application launchers often fork or D-Bus-activate another process;
		// RunPilot owns this Xpra server and stops it explicitly instead.
		if *options.LaunchAfterConnect {
			args = append(args, "--start-after-connect="+command)
		} else {
			args = append(args, "--start="+command)
		}
	}
	if dbusMode == model.RemoteDBusIsolated && hasDBusLaunch {
		args = append(args, "--dbus-launch="+dbusLaunch)
	} else {
		args = append(args, "--dbus-launch=no")
	}
	return args
}

// clientParams is limited to xpra-html5 v19 parameters verified in its bundled
// connect.html/index.html. Server-only settings such as DPI never appear here.
func clientParams(options model.XpraRemoteOptions) map[string]string {
	yesNo := func(value bool) string {
		if value {
			return "yes"
		}
		return "no"
	}
	params := map[string]string{
		"encoding": options.Encoding, "video": yesNo(*options.Video),
		"clipboard": yesNo(*options.Clipboard), "sound": "no", "printing": "no", "file_transfer": "no",
		"toolbar_position": options.ToolbarPosition,
	}
	switch options.Menu {
	case model.XpraMenuAutohide:
		params["floating_menu"], params["autohide"] = "yes", "yes"
	case model.XpraMenuVisible:
		params["floating_menu"], params["autohide"] = "yes", "no"
	case model.XpraMenuHidden:
		params["floating_menu"], params["autohide"] = "no", "no"
	}
	return params
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

// sanitizedServerEnvironment retains normal user/process settings but removes
// bindings to the physical graphical desktop before Xpra itself is launched.
func sanitizedServerEnvironment(source []string, mode model.RemoteDBusMode, hostBus string) ([]string, error) {
	blocked := map[string]bool{
		"DISPLAY": true, "WAYLAND_DISPLAY": true, "DBUS_SESSION_BUS_ADDRESS": true,
		"DBUS_SESSION_BUS_PID": true, "DBUS_STARTER_ADDRESS": true, "DBUS_STARTER_BUS_TYPE": true,
		"XAUTHORITY": true, "DESKTOP_STARTUP_ID": true, "XDG_ACTIVATION_TOKEN": true,
		"XDG_SESSION_TYPE": true, "XDG_CURRENT_DESKTOP": true, "XDG_SESSION_DESKTOP": true,
		"GDK_BACKEND": true, "QT_QPA_PLATFORM": true, "MOZ_ENABLE_WAYLAND": true,
	}
	environment := make([]string, 0, len(source)+1)
	for _, item := range source {
		key, _, found := strings.Cut(item, "=")
		if found && blocked[key] {
			continue
		}
		environment = append(environment, item)
	}
	if mode == model.RemoteDBusHost {
		if strings.TrimSpace(hostBus) == "" {
			return nil, fmt.Errorf("host-session D-Bus mode is enabled, but RunPilot has no DBUS_SESSION_BUS_ADDRESS")
		}
		environment = append(environment, "DBUS_SESSION_BUS_ADDRESS="+hostBus)
	}
	return environment, nil
}

// childEnvironment is passed with Xpra's supported --env option. It makes the
// target an X11 child of the virtual Xpra display while target configuration can
// still deliberately override any of these values.
func childEnvironment(target model.RemoteTarget, hostBus string) (map[string]string, error) {
	environment := map[string]string{
		"XDG_SESSION_TYPE": "x11",
		"GDK_BACKEND":      "x11",
		"QT_QPA_PLATFORM":  "xcb",
		"WAYLAND_DISPLAY":  "",
	}
	for key, value := range target.Command.Environment {
		environment[key] = value
	}
	if target.DBusMode != model.RemoteDBusHost {
		return environment, nil
	}
	if _, explicit := environment["DBUS_SESSION_BUS_ADDRESS"]; explicit {
		return environment, nil
	}
	if strings.TrimSpace(hostBus) == "" {
		return nil, fmt.Errorf("host-session D-Bus mode is enabled, but RunPilot has no DBUS_SESSION_BUS_ADDRESS; configure it in the target environment")
	}
	environment["DBUS_SESSION_BUS_ADDRESS"] = hostBus
	return environment, nil
}

func environmentValue(source []string, key string) string {
	for _, item := range source {
		name, value, found := strings.Cut(item, "=")
		if found && name == key {
			return value
		}
	}
	return ""
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var windowsPattern = regexp.MustCompile(`(?m)^state\.windows=(\d+)$`)

func windowCount(output string) (int, bool) {
	matches := windowsPattern.FindStringSubmatch(output)
	if len(matches) != 2 {
		return 0, false
	}
	var count int
	_, err := fmt.Sscanf(matches[1], "%d", &count)
	return count, err == nil
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
