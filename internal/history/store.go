package history

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/szilab/RunPilot/internal/model"
)

const defaultLimit = 100

type Store struct {
	mu      sync.Mutex
	dataDir string
	db      *sql.DB
	owners  map[string]*sql.DB
}

func Open(dataDir string) (*Store, error) {
	legacyDir := filepath.Join(dataDir, "legacy")
	if err := os.MkdirAll(filepath.Join(legacyDir, "runs"), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(legacyDir, "history.db"))
	if err != nil {
		return nil, err
	}
	store := &Store{dataDir: dataDir, db: db, owners: map[string]*sql.DB{}}
	if err := store.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.AbandonExecutions(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("finish interrupted plugin executions: %w", err)
	}
	return store, nil
}

func (s *Store) initialize() error {
	return initializeDB(s.db)
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
	return filepath.Join(s.dataDir, "legacy", "runs", runID+".log")
}

func (s *Store) Append(rec model.RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var finished any
	if rec.FinishedAt != nil {
		finished = rec.FinishedAt.UnixNano()
	}
	var success any
	if rec.Success != nil {
		if *rec.Success {
			success = 1
		} else {
			success = 0
		}
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO runs (id, kind, target_id, target_name, started_at, finished_at, exit_code, success, log_path, message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rec.ID, rec.Kind, rec.TargetID, rec.TargetName, rec.StartedAt.UnixNano(), finished, rec.ExitCode, success, rec.LogPath, rec.Message)
	return err
}

func (s *Store) Recent(targetID string, limit int) ([]model.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = defaultLimit
	}
	query := `SELECT id, kind, target_id, target_name, started_at, finished_at, exit_code, success, log_path, message FROM runs WHERE owner = ''`
	args := []any{}
	if targetID != "" {
		query += ` AND target_id = ?`
		args = append(args, targetID)
	}
	query += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.RunRecord{}
	for rows.Next() {
		var r model.RunRecord
		var started int64
		var finished, exitCode, success sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Kind, &r.TargetID, &r.TargetName, &started, &finished, &exitCode, &success, &r.LogPath, &r.Message); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(0, started)
		if finished.Valid {
			value := time.Unix(0, finished.Int64)
			r.FinishedAt = &value
		}
		if exitCode.Valid {
			value := int(exitCode.Int64)
			r.ExitCode = &value
		}
		if success.Valid {
			value := success.Int64 != 0
			r.Success = &value
		}
		result = append(result, r)
	}
	return result, rows.Err()
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
	errs = append(errs, s.db.Close())
	return errors.Join(errs...)
}
