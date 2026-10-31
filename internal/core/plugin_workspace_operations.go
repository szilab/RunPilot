package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

const (
	maxWorkspaceOperationsPerOwner = 4
	maxWorkspaceOperationsGlobal   = 16
	maxWorkspaceOperationRecords   = 32
	workspaceOperationRetention    = 5 * time.Minute
	workspaceOperationProgressTick = 500 * time.Millisecond
	workspaceCopyBuffer            = 1 << 20
)

// workspaceOperationManager runs long file operations (copy, move, delete)
// outside the plugin's serialized call path and reports progress as events.
type workspaceOperationManager struct {
	mu     sync.Mutex
	items  map[string]*workspaceOperation
	open   func(owner, root string) (*os.Root, string, error)
	emit   func(owner, event string, data any)
	wg     sync.WaitGroup
	closed bool
}

type workspaceLocation struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

type workspaceOperationStatus struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	Phase      string `json:"phase,omitempty"`
	Bytes      int64  `json:"bytes"`
	TotalBytes int64  `json:"totalBytes"`
	Files      int64  `json:"files"`
	TotalFiles int64  `json:"totalFiles"`
	Skipped    int64  `json:"skipped"`
	Code       string `json:"code,omitempty"`
	Error      string `json:"error,omitempty"`
}

type workspaceOperation struct {
	owner, id, kind    string
	cancel             context.CancelFunc
	bytes, files, skip atomic.Int64
	mu                 sync.Mutex
	state, phase       string
	totalBytes         int64
	totalFiles         int64
	code, message      string
	finishedAt         time.Time
}

func newWorkspaceOperationManager(open func(owner, root string) (*os.Root, string, error), emit func(owner, event string, data any)) *workspaceOperationManager {
	return &workspaceOperationManager{items: map[string]*workspaceOperation{}, open: open, emit: emit}
}

func (op *workspaceOperation) snapshot() workspaceOperationStatus {
	op.mu.Lock()
	defer op.mu.Unlock()
	return workspaceOperationStatus{ID: op.id, Kind: op.kind, State: op.state, Phase: op.phase, Bytes: op.bytes.Load(), TotalBytes: op.totalBytes, Files: op.files.Load(), TotalFiles: op.totalFiles, Skipped: op.skip.Load(), Code: op.code, Error: op.message}
}

func (op *workspaceOperation) setPhase(phase string) {
	op.mu.Lock()
	op.phase = phase
	op.mu.Unlock()
}

func (m *workspaceOperationManager) handle(owner, action string, raw json.RawMessage) (json.RawMessage, error) {
	if m == nil {
		return nil, &plugins.HostFailure{Code: "unavailable", Message: "workspace operations are unavailable"}
	}
	if action == "start" {
		return m.start(owner, raw)
	}
	var in struct {
		ID string `json:"id"`
	}
	if decodeNetworkParams(raw, &in) != nil || in.ID == "" {
		return nil, workspaceInvalid("operation id is required")
	}
	m.mu.Lock()
	op := m.items[in.ID]
	m.mu.Unlock()
	if op == nil || op.owner != owner {
		return nil, &plugins.HostFailure{Code: "not_found", Message: "unknown workspace operation"}
	}
	if action == "cancel" {
		op.cancel()
	} else if action != "status" {
		return nil, workspaceInvalid("unknown workspace operation action")
	}
	return json.Marshal(op.snapshot())
}

func (m *workspaceOperationManager) start(owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		Kind   string            `json:"kind"`
		Source workspaceLocation `json:"source"`
		Target workspaceLocation `json:"target"`
	}
	if len(raw) > 64<<10 || decodeNetworkParams(raw, &in) != nil || !validWorkspacePath(in.Source.Path, false) {
		return nil, workspaceInvalid("operation requires a valid kind and source path")
	}
	switch in.Kind {
	case "delete":
		if in.Target != (workspaceLocation{}) {
			return nil, workspaceInvalid("delete takes no target")
		}
	case "copy", "move":
		if !validWorkspacePath(in.Target.Path, false) {
			return nil, workspaceInvalid("copy and move require a valid target path")
		}
	default:
		return nil, workspaceInvalid("operation kind must be copy, move or delete")
	}
	source, sourcePath, err := m.open(owner, in.Source.Root)
	if err != nil {
		return nil, workspaceFileError(err)
	}
	var target *os.Root
	targetPath := ""
	closeRoots := func() {
		source.Close()
		if target != nil {
			target.Close()
		}
	}
	if _, err := source.Lstat(in.Source.Path); err != nil {
		closeRoots()
		return nil, workspaceFileError(err)
	}
	if in.Kind != "delete" {
		if target, targetPath, err = m.open(owner, in.Target.Root); err != nil {
			closeRoots()
			return nil, workspaceFileError(err)
		}
		if _, err := target.Lstat(in.Target.Path); err == nil {
			closeRoots()
			return nil, workspaceFileError(errWorkspaceExists)
		} else if !errors.Is(err, os.ErrNotExist) {
			closeRoots()
			return nil, workspaceFileError(err)
		}
		targetDir, _ := splitWorkspacePath(in.Target.Path)
		if info, err := target.Stat(targetDir); err != nil || !info.IsDir() {
			closeRoots()
			return nil, &plugins.HostFailure{Code: "not_found", Message: "target directory does not exist"}
		}
		// Copying a directory into itself would never finish.
		from := filepath.Join(sourcePath, filepath.FromSlash(in.Source.Path))
		to := filepath.Join(targetPath, filepath.FromSlash(in.Target.Path))
		if relative, err := filepath.Rel(from, to); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			closeRoots()
			return nil, workspaceInvalid("target cannot be inside the source")
		}
	}
	var idBytes [18]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		closeRoots()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	op := &workspaceOperation{owner: owner, id: base64.RawURLEncoding.EncodeToString(idBytes[:]), kind: in.Kind, cancel: cancel, state: "running", phase: "scanning"}
	m.mu.Lock()
	running, owned, records := 0, 0, 0
	for _, current := range m.items {
		current.mu.Lock()
		active := current.state == "running"
		current.mu.Unlock()
		if active {
			running++
			if current.owner == owner {
				owned++
			}
		}
		if current.owner == owner {
			records++
		}
	}
	if m.closed || running >= maxWorkspaceOperationsGlobal || owned >= maxWorkspaceOperationsPerOwner {
		m.mu.Unlock()
		cancel()
		closeRoots()
		return nil, &plugins.HostFailure{Code: "resource_limit", Message: "too many workspace operations are running"}
	}
	if records >= maxWorkspaceOperationRecords {
		m.pruneLocked(owner, true)
	}
	m.items[op.id] = op
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer closeRoots()
		m.run(ctx, op, workspaceSide{source, sourcePath, in.Source.Path}, workspaceSide{target, targetPath, in.Target.Path})
	}()
	return json.Marshal(op.snapshot())
}

// pruneLocked drops finished records; force also drops recent ones.
func (m *workspaceOperationManager) pruneLocked(owner string, force bool) {
	now := time.Now()
	for id, current := range m.items {
		current.mu.Lock()
		finished := current.state != "running"
		old := now.Sub(current.finishedAt) > workspaceOperationRetention
		current.mu.Unlock()
		if current.owner == owner && finished && (force || old) {
			delete(m.items, id)
		}
	}
}

// workspaceSide is one operand: an opened root, its host path and a relative name.
type workspaceSide struct {
	root *os.Root
	base string
	name string
}

func (m *workspaceOperationManager) run(ctx context.Context, op *workspaceOperation, source, target workspaceSide) {
	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(workspaceOperationProgressTick)
		defer ticker.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				m.emit(op.owner, "workspace.operation.progress", op.snapshot())
			}
		}
	}()
	err := m.execute(ctx, op, source, target)
	close(stopProgress)
	<-progressDone
	op.mu.Lock()
	switch {
	case err == nil:
		op.state = "succeeded"
	case ctx.Err() != nil:
		op.state, op.code, op.message = "canceled", "canceled", "operation was canceled"
	default:
		op.state = "failed"
		if failure, ok := workspaceFileError(err).(*plugins.HostFailure); ok {
			op.code, op.message = failure.Code, failure.Message
		}
	}
	op.phase = ""
	op.finishedAt = time.Now()
	op.mu.Unlock()
	op.cancel()
	m.emit(op.owner, "workspace.operation.done", op.snapshot())
	m.mu.Lock()
	m.pruneLocked(op.owner, false)
	m.mu.Unlock()
}

func (m *workspaceOperationManager) execute(ctx context.Context, op *workspaceOperation, sourceSide, targetSide workspaceSide) error {
	source, sourceName, target, targetName := sourceSide.root, sourceSide.name, targetSide.root, targetSide.name
	switch op.kind {
	case "delete":
		if err := scanWorkspaceTree(ctx, op, source, sourceName); err != nil {
			return err
		}
		op.setPhase("deleting")
		return removeWorkspaceTree(ctx, op, source, sourceName)
	case "move":
		op.setPhase("moving")
		err := renameInRoot(source, sourceSide.base, sourceName, target, targetSide.base, targetName, false)
		if err == nil {
			op.files.Store(1)
			return nil
		}
		if !errors.Is(err, errWorkspaceCrossDevice) {
			return err
		}
	}
	// copy, or a move across filesystems: copy, then remove the source.
	if err := scanWorkspaceTree(ctx, op, source, sourceName); err != nil {
		return err
	}
	op.setPhase("copying")
	if err := copyWorkspaceTree(ctx, op, source, sourceName, target, targetName); err != nil {
		// The target did not exist before, so a partial copy is removed.
		_ = removeWorkspaceTree(context.Background(), &workspaceOperation{}, target, targetName)
		return err
	}
	if op.kind == "move" {
		op.setPhase("deleting")
		return removeWorkspaceTree(ctx, &workspaceOperation{}, source, sourceName)
	}
	return nil
}

func readWorkspaceDir(root *os.Root, name string) ([]os.DirEntry, error) {
	dir, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(-1)
}

func scanWorkspaceTree(ctx context.Context, op *workspaceOperation, root *os.Root, name string) error {
	var files, bytes int64
	var walk func(string) error
	walk = func(current string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files++
			bytes += info.Size()
			return nil
		}
		if !info.IsDir() {
			files++
			return nil
		}
		entries, err := readWorkspaceDir(root, current)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := walk(path.Join(current, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(name); err != nil {
		return err
	}
	op.mu.Lock()
	op.totalFiles, op.totalBytes = files, bytes
	op.mu.Unlock()
	return nil
}

// copyWorkspaceTree copies regular files and directories. Symlinks and
// special files are skipped and counted, so a copy never follows a link.
func copyWorkspaceTree(ctx context.Context, op *workspaceOperation, source *os.Root, from string, target *os.Root, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := source.Lstat(from)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := target.Mkdir(to, info.Mode().Perm()|0o700); err != nil {
			return err
		}
		entries, err := readWorkspaceDir(source, from)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyWorkspaceTree(ctx, op, source, path.Join(from, entry.Name()), target, path.Join(to, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		return copyWorkspaceFile(ctx, op, source, from, target, to, info)
	default:
		op.skip.Add(1)
		op.files.Add(1)
		return nil
	}
}

func copyWorkspaceFile(ctx context.Context, op *workspaceOperation, source *os.Root, from string, target *os.Root, to string, info os.FileInfo) error {
	in, err := source.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := target.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	buffer := make([]byte, workspaceCopyBuffer)
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return err
		}
		n, readErr := in.Read(buffer)
		if n > 0 {
			if _, err := out.Write(buffer[:n]); err != nil {
				out.Close()
				return err
			}
			op.bytes.Add(int64(n))
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			out.Close()
			return readErr
		}
	}
	_ = setFileModTime(out, info.ModTime())
	if err := out.Close(); err != nil {
		return err
	}
	op.files.Add(1)
	return nil
}

func removeWorkspaceTree(ctx context.Context, op *workspaceOperation, root *os.Root, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := readWorkspaceDir(root, name)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := removeWorkspaceTree(ctx, op, root, path.Join(name, entry.Name())); err != nil {
				return err
			}
		}
	}
	if err := root.Remove(name); err != nil {
		return err
	}
	if !info.IsDir() {
		op.files.Add(1)
		op.bytes.Add(info.Size())
	}
	return nil
}

func (m *workspaceOperationManager) stopOwner(owner string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	for _, op := range m.items {
		if op.owner == owner {
			op.cancel()
		}
	}
	m.mu.Unlock()
}

func (m *workspaceOperationManager) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	for _, op := range m.items {
		op.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
