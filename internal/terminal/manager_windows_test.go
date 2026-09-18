//go:build windows

package terminal

import (
	"strings"
	"testing"
	"time"
)

func TestWindowsConPTYSmoke(t *testing.T) {
	manager := NewManager(t.TempDir(), 1)
	defer manager.Close()
	var shellID string
	for _, shell := range manager.Shells() {
		if shell.ID == "cmd" {
			shellID = shell.ID
			break
		}
	}
	if shellID == "" {
		t.Fatal("cmd.exe was not discovered")
	}
	session, err := manager.Start(shellID, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Write([]byte("echo RUNPILOT_TEST\r\n")); err != nil {
		t.Fatal(err)
	}
	result := make(chan string, 1)
	go func() {
		var output strings.Builder
		buffer := make([]byte, 1024)
		for output.Len() < 8192 {
			n, err := session.Read(buffer)
			if n > 0 {
				output.Write(buffer[:n])
				if strings.Contains(output.String(), "RUNPILOT_TEST") {
					result <- output.String()
					return
				}
			}
			if err != nil {
				result <- output.String()
				return
			}
		}
		result <- output.String()
	}()
	select {
	case output := <-result:
		if !strings.Contains(output, "RUNPILOT_TEST") {
			t.Fatalf("ConPTY output %q does not contain test marker", output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ConPTY output")
	}
}
