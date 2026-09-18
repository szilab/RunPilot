package terminal

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func shID(t *testing.T, manager *Manager) string {
	t.Helper()
	for _, shell := range manager.Shells() {
		if shell.ID == "sh" {
			return shell.ID
		}
	}
	t.Skip("a POSIX sh shell is not available")
	return ""
}

func TestManagerConcurrentStartRespectsLimit(t *testing.T) {
	if !Supported() {
		t.Skip("terminal is unsupported on this platform")
	}
	manager := NewManager(t.TempDir(), 2)
	defer manager.Close()
	shell := shID(t, manager)
	var wg sync.WaitGroup
	type result struct {
		session *Session
		err     error
	}
	results := make(chan result, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, err := manager.Start(shell, 80, 24)
			results <- result{session, err}
		}()
	}
	wg.Wait()
	close(results)
	var started, limited int
	for result := range results {
		if result.err == nil {
			started++
			_ = result.session.Close()
		} else if errors.Is(result.err, ErrSessionLimit) {
			limited++
		} else {
			t.Fatalf("unexpected concurrent start error: %v", result.err)
		}
	}
	if started != 2 || limited != 2 {
		t.Fatalf("started = %d, limited = %d", started, limited)
	}
}

func TestDiscoverShellsDoesNotDuplicateEquivalentPaths(t *testing.T) {
	if !Supported() {
		t.Skip("terminal is unsupported on this platform")
	}
	seen := map[string]bool{}
	for _, shell := range DiscoverShells() {
		key := canonicalShellPath(shell.Path)
		if seen[key] {
			t.Fatalf("shell %q is duplicated at %q", shell.Name, shell.Path)
		}
		seen[key] = true
	}
}

func TestManagerRejectsUnknownShellAndEnforcesLimit(t *testing.T) {
	if !Supported() {
		t.Skip("terminal is unsupported on this platform")
	}
	manager := NewManager(t.TempDir(), 1)
	defer manager.Close()
	if _, err := manager.Start("does-not-exist", 80, 24); !errors.Is(err, ErrUnknownShell) {
		t.Fatalf("unknown shell error = %v", err)
	}
	session, err := manager.Start(shID(t, manager), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := manager.Start(shID(t, manager), 80, 24); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("session limit error = %v", err)
	}
}

func TestLinuxPTYSmoke(t *testing.T) {
	if !Supported() {
		t.Skip("terminal is unsupported on this platform")
	}
	manager := NewManager(t.TempDir(), 1)
	defer manager.Close()
	session, err := manager.Start(shID(t, manager), 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Resize(120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if _, err := session.Write([]byte("printf 'RUNPILOT_TEST\\n'\\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	read := make(chan string, 1)
	go func() {
		var output strings.Builder
		buf := make([]byte, 1024)
		for output.Len() < 8192 {
			n, err := session.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				if strings.Contains(output.String(), "RUNPILOT_TEST") {
					read <- output.String()
					return
				}
			}
			if err != nil {
				read <- output.String()
				return
			}
		}
		read <- output.String()
	}()
	select {
	case output := <-read:
		if !strings.Contains(output, "RUNPILOT_TEST") {
			t.Fatalf("PTY output %q does not contain test marker", output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for PTY output")
	}
}
