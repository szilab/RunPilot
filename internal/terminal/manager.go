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
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/processsession"
)

const DefaultMaxSessions = 8

var (
	ErrUnsupported  = errors.New("terminal is not supported on this platform")
	ErrSessionLimit = errors.New("maximum number of terminal sessions reached")
)

// Manager tracks in-memory terminal sessions. Sessions are never persisted.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	cwd      string
	max      int
}

func NewManager(dataDir string, maxSessions int) *Manager {
	if maxSessions <= 0 {
		maxSessions = DefaultMaxSessions
	}
	return &Manager{sessions: map[string]*Session{}, cwd: workingDirectory(dataDir), max: maxSessions}
}

func Supported() bool { return runtime.GOOS == "linux" || runtime.GOOS == "windows" }

// StartCommand starts a named, prevalidated interactive command in a PTY.
// It is intentionally used only by narrow typed integrations such as Docker
// container terminals, never as a generic command execution surface.
func (m *Manager) StartCommand(name, path string, args []string, cols, rows uint16) (*Session, error) {
	return m.startCommand(name, path, args, cols, rows)
}

func (m *Manager) startCommand(name, path string, args []string, cols, rows uint16) (*Session, error) {
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
	process, err := processsession.Start(path, args, m.cwd, terminalEnvironment(os.Environ()), rows, cols)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	s := &Session{
		id:        randomID(),
		name:      name,
		createdAt: time.Now().UTC(),
		cols:      cols,
		rows:      rows,
		process:   process,
		waitDone:  make(chan struct{}),
	}
	s.onClose = func() { m.remove(s.id) }
	m.sessions[s.id] = s
	go func() { _ = s.Wait() }()
	return s, nil
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

var _ io.ReadWriteCloser = (*Session)(nil)
