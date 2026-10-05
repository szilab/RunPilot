package history

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Owner-scoped executions are a generic record of "something a plugin ran".
// Each owner has its own database and log directory, but every
// operation is namespaced by the owner and log paths are always derived here:
// callers never supply a filesystem path.
const (
	MaxExecutionLogBytes  = 16 << 20
	MaxExecutionAppend    = 64 << 10
	MaxExecutionRead      = 1 << 20
	DefaultExecutionRead  = 64 << 10
	DefaultExecutionLimit = 50
	MaxExecutionLimit     = 100
	// ExecutionRetention is the number of finished executions kept per owner
	// and subject. Older finished executions and their logs are pruned.
	ExecutionRetention = 200

	maxKindLength    = 32
	maxSubjectLength = 128
	maxLabelLength   = 256
	maxMessageLength = 1024
)

var (
	ErrExecutionNotFound = errors.New("unknown execution")
	ErrInvalidExecution  = errors.New("invalid execution")
)

type Execution struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Subject    string     `json:"subject"`
	Label      string     `json:"label"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	Success    *bool      `json:"success,omitempty"`
	Message    string     `json:"message,omitempty"`
}

func validName(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' || r == ':') {
			return false
		}
	}
	return true
}

func validOwner(owner string) bool {
	return validName(owner, 128) && !strings.Contains(owner, ":") && owner != "." && owner != ".."
}

func truncateText(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func newExecutionID() (string, error) {
	var raw [9]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "exec-" + hex.EncodeToString(raw[:]), nil
}

func (s *Store) executionLogPath(owner, id string) string {
	return filepath.Join(s.dataDir, "plugins", owner, "data", "runs", id+".log")
}

// BeginExecution creates an unfinished execution and its empty log.
func (s *Store) BeginExecution(owner, kind, subject, label string) (Execution, error) {
	if !validOwner(owner) || !validName(kind, maxKindLength) || !validName(subject, maxSubjectLength) {
		return Execution{}, fmt.Errorf("%w: owner, kind and subject must be short identifiers", ErrInvalidExecution)
	}
	label = truncateText(strings.TrimSpace(label), maxLabelLength)
	id, err := newExecutionID()
	if err != nil {
		return Execution{}, err
	}
	db, err := s.ownerDB(owner)
	if err != nil {
		return Execution{}, err
	}
	path := s.executionLogPath(owner, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Execution{}, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Execution{}, err
	}
	_ = file.Close()
	started := time.Now()
	s.mu.Lock()
	_, err = db.Exec(`INSERT INTO runs (id, kind, target_id, target_name, started_at, log_path, owner) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, kind, subject, label, started.UnixNano(), filepath.ToSlash(filepath.Join("runs", id+".log")), owner)
	s.mu.Unlock()
	if err != nil {
		_ = os.Remove(path)
		return Execution{}, err
	}
	return Execution{ID: id, Kind: kind, Subject: subject, Label: label, StartedAt: time.Unix(0, started.UnixNano())}, nil
}

const executionColumns = `id, kind, target_id, target_name, started_at, finished_at, exit_code, success, message`

type rowScanner interface{ Scan(...any) error }

func scanExecution(row rowScanner) (Execution, error) {
	var e Execution
	var started int64
	var finished, exitCode, success sql.NullInt64
	if err := row.Scan(&e.ID, &e.Kind, &e.Subject, &e.Label, &started, &finished, &exitCode, &success, &e.Message); err != nil {
		return Execution{}, err
	}
	e.StartedAt = time.Unix(0, started)
	if finished.Valid {
		value := time.Unix(0, finished.Int64)
		e.FinishedAt = &value
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		e.ExitCode = &value
	}
	if success.Valid {
		value := success.Int64 != 0
		e.Success = &value
	}
	return e, nil
}

// GetExecution returns one execution owned by owner.
func (s *Store) GetExecution(owner, id string) (Execution, error) {
	if !validOwner(owner) || !validName(id, 128) {
		return Execution{}, ErrExecutionNotFound
	}
	db, err := s.ownerDB(owner)
	if err != nil {
		return Execution{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getExecutionLocked(db, owner, id)
}

func (s *Store) getExecutionLocked(db *sql.DB, owner, id string) (Execution, error) {
	e, err := scanExecution(db.QueryRow(`SELECT `+executionColumns+` FROM runs WHERE owner = ? AND id = ?`, owner, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Execution{}, ErrExecutionNotFound
	}
	return e, err
}

// FinishExecution records the outcome once. Finishing a finished execution
// returns the existing record so exit/reconcile paths may race safely.
func (s *Store) FinishExecution(owner, id string, exitCode *int, success *bool, message string) (Execution, error) {
	if !validOwner(owner) || !validName(id, 128) {
		return Execution{}, ErrExecutionNotFound
	}
	db, err := s.ownerDB(owner)
	if err != nil {
		return Execution{}, err
	}
	message = truncateText(message, maxMessageLength)
	var code, ok any
	if exitCode != nil {
		code = *exitCode
	}
	if success != nil {
		if *success {
			ok = 1
		} else {
			ok = 0
		}
	}
	s.mu.Lock()
	result, err := db.Exec(`UPDATE runs SET finished_at = ?, exit_code = ?, success = ?, message = ? WHERE owner = ? AND id = ? AND finished_at IS NULL`, time.Now().UnixNano(), code, ok, message, owner, id)
	if err != nil {
		s.mu.Unlock()
		return Execution{}, err
	}
	changed, _ := result.RowsAffected()
	execution, err := s.getExecutionLocked(db, owner, id)
	s.mu.Unlock()
	if err != nil {
		return Execution{}, err
	}
	if changed > 0 {
		s.pruneExecutions(owner, execution.Subject)
	}
	return execution, nil
}

// ListExecutions returns the newest executions, optionally for one subject.
func (s *Store) ListExecutions(owner, subject string, limit int) ([]Execution, error) {
	if !validOwner(owner) || (subject != "" && !validName(subject, maxSubjectLength)) {
		return nil, fmt.Errorf("%w: owner or subject", ErrInvalidExecution)
	}
	if limit <= 0 {
		limit = DefaultExecutionLimit
	}
	if limit > MaxExecutionLimit {
		limit = MaxExecutionLimit
	}
	db, err := s.ownerDB(owner)
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + executionColumns + ` FROM runs WHERE owner = ?`
	args := []any{owner}
	if subject != "" {
		query += ` AND target_id = ?`
		args = append(args, subject)
	}
	query += ` ORDER BY started_at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Execution{}
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) pruneExecutions(owner, subject string) {
	db, err := s.ownerDB(owner)
	if err != nil {
		return
	}
	s.mu.Lock()
	rows, err := db.Query(`SELECT id FROM runs WHERE owner = ? AND target_id = ? AND finished_at IS NOT NULL ORDER BY started_at DESC, rowid DESC LIMIT -1 OFFSET ?`, owner, subject, ExecutionRetention)
	if err != nil {
		s.mu.Unlock()
		return
	}
	var stale []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			stale = append(stale, id)
		}
	}
	_ = rows.Close()
	for _, id := range stale {
		_, _ = db.Exec(`DELETE FROM runs WHERE owner = ? AND id = ?`, owner, id)
	}
	s.mu.Unlock()
	for _, id := range stale {
		_ = os.Remove(s.executionLogPath(owner, id))
	}
}

// AbandonExecutions finishes plugin executions left unfinished by a previous
// RunPilot process. It must only run before any plugin can start work.
func (s *Store) AbandonExecutions() error {
	entries, err := os.ReadDir(filepath.Join(s.dataDir, "plugins"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validOwner(entry.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.dataDir, "plugins", entry.Name(), "data", "history.db")); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		db, err := s.ownerDB(entry.Name())
		if err != nil {
			return err
		}
		s.mu.Lock()
		_, err = db.Exec(`UPDATE runs SET finished_at = ?, success = 0, message = 'interrupted by RunPilot restart' WHERE owner = ? AND finished_at IS NULL`, time.Now().UnixNano(), entry.Name())
		s.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// ExecutionLog is a size-capped, concurrency-safe writer for one execution.
// Writes beyond the cap are discarded after a single truncation marker; they
// never fail, so a chatty child cannot be blocked or broken by the cap.
type ExecutionLog struct {
	mu        sync.Mutex
	file      *os.File
	size      int64
	truncated bool
	closed    bool
	onWrite   func(size int64)
}

const truncationMarker = "\n[output truncated: execution log limit reached]\n"

// OpenExecutionLog opens an existing, unfinished execution's log for append.
// onWrite (optional) is called after each accepted write with the new size and
// must return quickly.
func (s *Store) OpenExecutionLog(owner, id string, onWrite func(size int64)) (*ExecutionLog, error) {
	execution, err := s.GetExecution(owner, id)
	if err != nil {
		return nil, err
	}
	if execution.FinishedAt != nil {
		return nil, fmt.Errorf("%w: execution is finished", ErrInvalidExecution)
	}
	file, err := os.OpenFile(s.executionLogPath(owner, id), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &ExecutionLog{file: file, size: info.Size(), onWrite: onWrite}, nil
}

func (l *ExecutionLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return len(p), nil
	}
	remaining := int64(MaxExecutionLogBytes) - l.size
	data := p
	if int64(len(data)) > remaining {
		if remaining < 0 {
			remaining = 0
		}
		data = data[:remaining]
	}
	if len(data) > 0 {
		n, err := l.file.Write(data)
		l.size += int64(n)
		if err != nil {
			l.mu.Unlock()
			return len(p), nil
		}
	}
	if len(data) < len(p) && !l.truncated {
		l.truncated = true
		n, _ := l.file.WriteString(truncationMarker)
		l.size += int64(n)
	}
	size, notify := l.size, l.onWrite
	l.mu.Unlock()
	if notify != nil {
		notify(size)
	}
	return len(p), nil
}

// Size reports the bytes accepted so far.
func (l *ExecutionLog) Size() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.size
}

func (l *ExecutionLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.file.Close()
}

var _ io.WriteCloser = (*ExecutionLog)(nil)

// AppendExecutionOutput appends bounded plugin-supplied text to a running or
// finished execution's log, subject to the same size cap as captured output.
func (s *Store) AppendExecutionOutput(owner, id, text string) error {
	if len(text) > MaxExecutionAppend {
		return fmt.Errorf("%w: text exceeds %d bytes", ErrInvalidExecution, MaxExecutionAppend)
	}
	if _, err := s.GetExecution(owner, id); err != nil {
		return err
	}
	file, err := os.OpenFile(s.executionLogPath(owner, id), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size()+int64(len(text)) > MaxExecutionLogBytes {
		return nil
	}
	_, err = file.WriteString(text)
	return err
}

// ReadExecutionOutput returns at most maxBytes from the end of the log.
func (s *Store) ReadExecutionOutput(owner, id string, maxBytes int) (text string, size int64, truncated bool, err error) {
	if _, err := s.GetExecution(owner, id); err != nil {
		return "", 0, false, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultExecutionRead
	}
	if maxBytes > MaxExecutionRead {
		maxBytes = MaxExecutionRead
	}
	file, err := os.Open(s.executionLogPath(owner, id))
	if errors.Is(err, os.ErrNotExist) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, false, err
	}
	size = info.Size()
	offset := int64(0)
	if size > int64(maxBytes) {
		offset, truncated = size-int64(maxBytes), true
	}
	buf := make([]byte, size-offset)
	n, err := file.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", 0, false, err
	}
	buf = buf[:n]
	if truncated {
		// Drop a partial leading rune left by cutting mid-character.
		for len(buf) > 0 && !utf8.RuneStart(buf[0]) {
			buf = buf[1:]
		}
	}
	return strings.ToValidUTF8(string(buf), "\uFFFD"), size, truncated, nil
}
