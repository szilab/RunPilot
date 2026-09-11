package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyRobocopyBackupIsNormalized(t *testing.T) {
	dir := t.TempDir()
	yaml := `version: 1
server: { bind: 127.0.0.1:9070, token: token }
jobs:
  - id: old
    name: Old backup
    enabled: true
    type: backup
    schedule: { type: interval, intervalSeconds: 60 }
    backup: { engine: robocopy, source: C:\Data, destination: F:\Backup, mode: mirror, retries: 2, retryWaitSeconds: 5 }
`
	if err := os.WriteFile(filepath.Join(dir, "runpilot.yaml"), []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := s.Snapshot().Jobs[0].Backup
	if b.Robocopy == nil || b.Robocopy.Source != `C:\Data` || b.Robocopy.Mode != "mirror" {
		t.Fatalf("backup = %#v", b)
	}
	if b.Source != "" {
		t.Fatal("legacy fields remained in normalized model")
	}
}
