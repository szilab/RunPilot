package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/szilab/RunPilot/internal/model"
)

type Store struct {
	mu      sync.Mutex
	dataDir string
	path    string
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "runs"), 0o755); err != nil {
		return nil, err
	}
	return &Store{
		dataDir: dataDir,
		path:    filepath.Join(dataDir, "history.jsonl"),
	}, nil
}

func (s *Store) RunLogPath(runID string) string {
	return filepath.Join(s.dataDir, "runs", runID+".log")
}

func (s *Store) Append(rec model.RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *Store) Recent(limit int) ([]model.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []model.RunRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var all []model.RunRecord
	sc := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, 4*1024*1024)
	for sc.Scan() {
		var r model.RunRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			all = append(all, r)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].StartedAt.After(all[j].StartedAt)
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
