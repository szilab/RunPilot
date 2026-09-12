// Package terminal owns short-lived, interactive PTY-backed shell sessions.
// It deliberately has no dependency on RunPilot's non-interactive launcher.
package terminal

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

const DefaultMaxSessions = 8

var (
	ErrUnsupported  = errors.New("terminal is not supported on this platform")
	ErrSessionLimit = errors.New("maximum number of terminal sessions reached")
	ErrUnknownShell = errors.New("requested shell is not available")
)

// Shell describes an executable that can be started as an interactive shell.
type Shell struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Available bool   `json:"available"`
}

// Manager tracks in-memory terminal sessions. Sessions are never persisted.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	shells   []Shell
	cwd      string
	max      int
}

func NewManager(dataDir string, maxSessions int) *Manager {
	if maxSessions <= 0 {
		maxSessions = DefaultMaxSessions
	}
	return &Manager{
		sessions: map[string]*Session{},
		shells:   DiscoverShells(),
		cwd:      workingDirectory(dataDir),
		max:      maxSessions,
	}
}

func Supported() bool { return runtime.GOOS == "linux" || runtime.GOOS == "windows" }

func (m *Manager) Available() bool { return Supported() && len(m.shells) > 0 }

func (m *Manager) Shells() []Shell {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Shell(nil), m.shells...)
}

func (m *Manager) DefaultShell() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.shells) == 0 {
		return ""
	}
	return m.shells[0].ID
}

func (m *Manager) Start(shellID string, cols, rows uint16) (*Session, error) {
	if !Supported() {
		return nil, ErrUnsupported
	}
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) >= m.max {
		return nil, ErrSessionLimit
	}
	shell, ok := m.shell(shellID)
	if !ok {
		return nil, ErrUnknownShell
	}
	p, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("initialize PTY: %w", err)
	}
	if err := p.Resize(int(cols), int(rows)); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("resize PTY: %w", err)
	}
	cmd := p.Command(shell.Path)
	cmd.Dir = m.cwd
	cmd.Env = terminalEnvironment(os.Environ())
	if err := cmd.Start(); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("start %s: %w", shell.Name, err)
	}
	cleanup, err := attachProcessTree(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = p.Close()
		return nil, fmt.Errorf("contain shell process: %w", err)
	}
	s := &Session{
		id:        randomID(),
		shell:     shell,
		createdAt: time.Now().UTC(),
		cols:      cols,
		rows:      rows,
		pty:       p,
		cmd:       cmd,
		cleanup:   cleanup,
		waitDone:  make(chan struct{}),
	}
	s.onClose = func() { m.remove(s.id) }
	m.sessions[s.id] = s
	go func() { _ = s.Wait() }()
	return s, nil
}

func (m *Manager) shell(id string) (Shell, bool) {
	if id == "" && len(m.shells) > 0 {
		return m.shells[0], true
	}
	for _, shell := range m.shells {
		if shell.ID == id {
			return shell, true
		}
	}
	return Shell{}, false
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func (m *Manager) Close() error {
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()
	var err error
	for _, session := range sessions {
		err = errors.Join(err, session.Close())
	}
	return err
}

func workingDirectory(dataDir string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if info, statErr := os.Stat(home); statErr == nil && info.IsDir() {
			return home
		}
	}
	if dataDir != "" {
		if info, err := os.Stat(dataDir); err == nil && info.IsDir() {
			return dataDir
		}
	}
	return "."
}

func terminalEnvironment(env []string) []string {
	values := append([]string(nil), env...)
	has := func(key string) bool {
		prefix := key + "="
		for _, value := range values {
			if runtime.GOOS == "windows" {
				if strings.EqualFold(value[:min(len(value), len(prefix))], prefix) {
					return true
				}
			} else if strings.HasPrefix(value, prefix) {
				return true
			}
		}
		return false
	}
	if runtime.GOOS != "windows" {
		if !has("TERM") {
			values = append(values, "TERM=xterm-256color")
		}
		if !has("COLORTERM") {
			values = append(values, "COLORTERM=truecolor")
		}
	}
	return values
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func randomID() string {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(bytes)
}

func shellFromPath(path string) string {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	return strings.ReplaceAll(name, " ", "-")
}

var _ io.ReadWriteCloser = (*Session)(nil)
