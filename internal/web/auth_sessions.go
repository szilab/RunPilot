package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const maxPersistedAuthSessions = 128

type persistedAuthSessions struct {
	Version  int                             `json:"version"`
	Sessions map[string]persistedAuthSession `json:"sessions"`
}

type persistedAuthSession struct {
	CSRFHash string    `json:"csrfHash"`
	Expires  time.Time `json:"expires"`
}

func (s *Server) authSessionsPath() string {
	return filepath.Join(s.ctrl.DataDir(), "internal", "auth-sessions.json")
}

func (s *Server) loadSessions() error {
	path := s.authSessionsPath()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect browser session store: %w", err)
	}
	if info.Size() > 128*1024 {
		return errors.New("browser session store is too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read browser session store: %w", err)
	}
	var stored persistedAuthSessions
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("decode browser session store: %w", err)
	}
	if stored.Version != 1 || len(stored.Sessions) > maxPersistedAuthSessions {
		return errors.New("browser session store has an unsupported format")
	}
	now := time.Now()
	for id, record := range stored.Sessions {
		if len(id) != 43 || len(record.CSRFHash) != 43 || record.Expires.IsZero() {
			return errors.New("browser session store contains an invalid session")
		}
		if now.Before(record.Expires) {
			s.sessions[id] = authSession{csrfHash: record.CSRFHash, expires: record.Expires}
		}
	}
	return nil
}

// saveSessionsLocked must be called while sessionMu is held.
func (s *Server) saveSessionsLocked() error {
	stored := persistedAuthSessions{Version: 1, Sessions: make(map[string]persistedAuthSession, len(s.sessions))}
	for id, session := range s.sessions {
		stored.Sessions[id] = persistedAuthSession{CSRFHash: session.csrfHash, Expires: session.expires}
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	directory := filepath.Dir(s.authSessionsPath())
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create browser session store directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".auth-sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("create browser session store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure browser session store: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write browser session store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync browser session store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close browser session store: %w", err)
	}
	if err := os.Rename(temporaryPath, s.authSessionsPath()); err != nil {
		return fmt.Errorf("replace browser session store: %w", err)
	}
	return nil
}

// revokePersistedSessionsLocked falls back to revoking every saved browser
// session if replacing the session table fails. It must be called with
// sessionMu held.
func (s *Server) revokePersistedSessionsLocked() error {
	if err := s.saveSessionsLocked(); err == nil {
		return nil
	} else {
		removeErr := os.Remove(s.authSessionsPath())
		if errors.Is(removeErr, os.ErrNotExist) {
			return nil
		}
		return removeErr
	}
}
