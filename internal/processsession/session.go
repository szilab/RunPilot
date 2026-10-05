// Package processsession provides a generic interactive process transport.
// Feature plugins own session meaning; this package owns only the OS PTY and
// attached process lifecycle.
package processsession

import (
	"errors"
	"sync"

	pty "github.com/aymanbagabas/go-pty"
)

type Session struct {
	pty         pty.Pty
	cmd         *pty.Cmd
	cleanup     func() error
	waitOnce    sync.Once
	waitDone    chan struct{}
	waitErr     error
	closeOnce   sync.Once
	cleanupOnce sync.Once
	cleanupErr  error
}

func Start(command string, args []string, dir string, env []string, rows, columns uint16) (*Session, error) {
	if rows == 0 || columns == 0 {
		return nil, errors.New("process session dimensions must be positive")
	}
	p, err := pty.New()
	if err != nil {
		return nil, err
	}
	if err = p.Resize(int(columns), int(rows)); err != nil {
		_ = p.Close()
		return nil, err
	}
	cmd := p.Command(command, args...)
	cmd.Dir, cmd.Env = dir, env
	if err = cmd.Start(); err != nil {
		_ = p.Close()
		return nil, err
	}
	cleanup, err := contain(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = p.Close()
		return nil, err
	}
	s := &Session{pty: p, cmd: cmd, cleanup: cleanup, waitDone: make(chan struct{})}
	return s, nil
}

func (s *Session) Wait() error {
	s.waitOnce.Do(func() { s.waitErr = s.cmd.Wait(); close(s.waitDone) })
	<-s.waitDone
	return s.waitErr
}
func (s *Session) Done() <-chan struct{}             { return s.waitDone }
func (s *Session) Resize(rows, columns uint16) error { return s.pty.Resize(int(columns), int(rows)) }
func (s *Session) Read(data []byte) (int, error)     { return s.pty.Read(data) }
func (s *Session) Write(data []byte) (int, error)    { return s.pty.Write(data) }
func (s *Session) ExitCode() (int, bool) {
	if s.cmd.ProcessState == nil {
		return 0, false
	}
	return s.cmd.ProcessState.ExitCode(), true
}
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.cleanupOnce.Do(func() {
			if s.cleanup != nil {
				s.cleanupErr = s.cleanup()
			}
		})
		err = errors.Join(err, s.cleanupErr)
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		err = errors.Join(err, s.pty.Close())
	})
	return err
}
func (s *Session) Force() error {
	s.cleanupOnce.Do(func() {
		if s.cleanup != nil {
			s.cleanupErr = s.cleanup()
		}
	})
	if s.cmd.Process == nil {
		return s.cleanupErr
	}
	if err := killTree(s.cmd.Process); err != nil {
		return errors.Join(s.cleanupErr, err)
	}
	return s.cleanupErr
}
func (s *Session) Graceful() error { return graceful(s.cmd.Process, s.pty) }
