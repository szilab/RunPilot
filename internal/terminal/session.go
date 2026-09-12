package terminal

import (
	"errors"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

// Session is a single shell attached to a real OS pseudo-terminal.
type Session struct {
	id         string
	shell      Shell
	createdAt  time.Time
	cols, rows uint16
	pty        pty.Pty
	cmd        *pty.Cmd
	cleanup    func() error
	onClose    func()
	mu         sync.RWMutex
	closeOnce  sync.Once
	waitOnce   sync.Once
	waitDone   chan struct{}
	waitErr    error
}

func (s *Session) ID() string           { return s.id }
func (s *Session) Shell() Shell         { return s.shell }
func (s *Session) CreatedAt() time.Time { return s.createdAt }

func (s *Session) Read(p []byte) (int, error)  { return s.pty.Read(p) }
func (s *Session) Write(p []byte) (int, error) { return s.pty.Write(p) }

func (s *Session) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return errors.New("terminal dimensions must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pty.Resize(int(cols), int(rows)); err != nil {
		return err
	}
	s.cols, s.rows = cols, rows
	return nil
}

func (s *Session) Close() (err error) {
	s.closeOnce.Do(func() {
		if s.cleanup != nil {
			err = errors.Join(err, s.cleanup())
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		if s.pty != nil {
			err = errors.Join(err, s.pty.Close())
		}
		if s.onClose != nil {
			s.onClose()
		}
	})
	return err
}

// Wait waits for the shell process. It is safe to call concurrently.
func (s *Session) Wait() error {
	s.waitOnce.Do(func() {
		s.waitErr = s.cmd.Wait()
		close(s.waitDone)
		_ = s.Close()
	})
	<-s.waitDone
	return s.waitErr
}
