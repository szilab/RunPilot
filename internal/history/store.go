package history

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
}

type legacyRunRecord struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	TargetID   string  `json:"targetId"`
	TargetName string  `json:"targetName"`
	StartedAt  string  `json:"startedAt"`
	FinishedAt *string `json:"finishedAt,omitempty"`
	ExitCode   *int    `json:"exitCode,omitempty"`
	Success    *bool   `json:"success,omitempty"`
	LogPath    string  `json:"logPath"`
	Message    string  `json:"message,omitempty"`
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "runs"), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "history.db"))
	if err != nil {
		return nil, err
	}
	store := &Store{dataDir: dataDir, db: db}
	if err := store.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrateLegacy(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate legacy run history: %w", err)
	}
	return store, nil
}

func (s *Store) initialize() error {
	_, err := s.db.Exec(`
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
  message TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS runs_target_started ON runs(target_id, started_at DESC);
CREATE INDEX IF NOT EXISTS runs_started ON runs(started_at DESC);`)
	return err
}

func (s *Store) migrateLegacy() error {
	f, err := os.Open(filepath.Join(s.dataDir, "history.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO runs (id, kind, target_id, target_name, started_at, finished_at, exit_code, success, log_path, message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var legacy legacyRunRecord
		if err := json.Unmarshal(scanner.Bytes(), &legacy); err != nil {
			log.Printf("warning: ignoring malformed history.jsonl record at line %d: %v", line, err)
			continue
		}
		started, err := time.Parse(time.RFC3339Nano, legacy.StartedAt)
		if err != nil || strings.TrimSpace(legacy.ID) == "" {
			log.Printf("warning: ignoring incomplete history.jsonl record at line %d", line)
			continue
		}
		var finished any
		if legacy.FinishedAt != nil {
			value, parseErr := time.Parse(time.RFC3339Nano, *legacy.FinishedAt)
			if parseErr != nil {
				log.Printf("warning: ignoring history.jsonl record at line %d with invalid finishedAt: %v", line, parseErr)
				continue
			}
			finished = value.UnixNano()
		}
		var success any
		if legacy.Success != nil {
			if *legacy.Success {
				success = 1
			} else {
				success = 0
			}
		}
		if _, err := stmt.Exec(legacy.ID, legacy.Kind, legacy.TargetID, legacy.TargetName, started.UnixNano(), finished, legacy.ExitCode, success, legacy.LogPath, legacy.Message); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RunLogPath(runID string) string {
	return filepath.Join(s.dataDir, "runs", runID+".log")
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
	query := `SELECT id, kind, target_id, target_name, started_at, finished_at, exit_code, success, log_path, message FROM runs`
	args := []any{}
	if targetID != "" {
		query += ` WHERE target_id = ?`
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

func (s *Store) Close() error { return s.db.Close() }
