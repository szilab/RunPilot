package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	token := s.Snapshot().Server.Token
	if token == "" {
		t.Fatal("missing token")
	}
	if !s.TokenCreated() {
		t.Fatal("new configuration did not report a generated token")
	}
	if _, err := os.Stat(filepath.Join(dir, "runpilot.yaml")); err != nil {
		t.Fatalf("YAML configuration was not created: %v", err)
	}
	if err := s.Update(func(c *model.Config) error {
		c.Server.Bind = "127.0.0.1:9999"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Snapshot().Server.Bind; got != "127.0.0.1:9999" {
		t.Fatalf("bind = %q", got)
	}
	if s2.TokenCreated() {
		t.Fatal("existing configuration unexpectedly generated a token")
	}
}
