package history

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

type Store struct {
	mu      sync.Mutex
	dataDir string
	owners  map[string]*sql.DB
}

// Open initializes only generic plugin-owned history. The retained Backup
// runner uses bounded internal log files and does not create a global run DB.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "internal", "runs"), 0o700); err != nil {
		return nil, err
	}
	store := &Store{dataDir: dataDir, owners: map[string]*sql.DB{}}
	if err := store.AbandonExecutions(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func initializeDB(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  target_name TEXT NOT NULL,
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  exit_code INTEGER,
  success INTEGER,
  log_path TEXT NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  owner TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS runs_target_started ON runs(target_id, started_at DESC);
CREATE INDEX IF NOT EXISTS runs_started ON runs(started_at DESC);
CREATE INDEX IF NOT EXISTS runs_owner_subject ON runs(owner, target_id, started_at DESC);`)
	return err
}

func (s *Store) RunLogPath(runID string) string {
	return filepath.Join(s.dataDir, "internal", "runs", runID+".log")
}

func (s *Store) ownerDB(owner string) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if db := s.owners[owner]; db != nil {
		return db, nil
	}
	dir := filepath.Join(s.dataDir, "plugins", owner, "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "history.db"))
	if err != nil {
		return nil, err
	}
	if err := initializeDB(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.owners[owner] = db
	return db, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, db := range s.owners {
		errs = append(errs, db.Close())
	}
	return errors.Join(errs...)
}
