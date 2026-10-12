package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ValidateDataRoot checks absolute paths, symlink components and service-account access.
func ValidateDataRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("RunPilot data directory must be an absolute path")
	}
	root = filepath.Clean(root)
	if filepath.Dir(root) == root {
		return "", errors.New("filesystem root cannot be used as the RunPilot data directory")
	}
	volume := filepath.VolumeName(root)
	current := volume + string(filepath.Separator)
	rest := strings.TrimPrefix(strings.TrimPrefix(root, volume), string(filepath.Separator))
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("check data directory %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("data directory path contains a symbolic link: %s", current)
		}
	}
	probe := root
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("access data directory: %w", err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", errors.New("data directory parent does not exist")
		}
		probe = parent
	}
	info, err := os.Stat(probe)
	if err != nil || !info.IsDir() {
		return "", errors.New("data directory parent is not accessible")
	}
	file, err := os.Open(probe)
	if err != nil {
		return "", fmt.Errorf("data directory is not accessible to the RunPilot service account: %w", err)
	}
	_ = file.Close()
	probeFile, err := os.CreateTemp(probe, ".runpilot-access-*")
	if err != nil {
		return "", fmt.Errorf("data directory is not writable by the RunPilot service account: %w", err)
	}
	probeName := probeFile.Name()
	if err := probeFile.Close(); err != nil {
		_ = os.Remove(probeName)
		return "", err
	}
	if err := os.Remove(probeName); err != nil {
		return "", fmt.Errorf("remove data directory access check: %w", err)
	}
	return root, nil
}

// ValidateNewDataRoot requires a missing destination whose existing parent is writable.
func ValidateNewDataRoot(root string) (string, error) {
	root, err := ValidateDataRoot(root)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(root); err == nil {
		return "", errors.New("destination must be a new directory")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(root)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", errors.New("data directory parent must already exist")
	}
	f, err := os.CreateTemp(parent, ".runpilot-access-*")
	if err != nil {
		return "", fmt.Errorf("data directory parent is not writable by the RunPilot service account: %w", err)
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return root, nil
}

// CopyDataRoot copies RunPilot state into a new or empty directory without following links.
func CopyDataRoot(source, destination string) (bool, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return false, err
	}
	destination, err = ValidateNewDataRoot(destination)
	if err != nil {
		return false, err
	}
	if filepath.Clean(source) == destination {
		return false, nil
	}
	if inside(source, destination) || inside(destination, source) {
		return false, errors.New("source and destination data directories cannot contain one another")
	}
	if filepath.Dir(destination) == destination {
		return false, errors.New("filesystem root cannot be used as the RunPilot data directory")
	}
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(destination, 0o700); err != nil {
			return false, fmt.Errorf("create data directory: %w", err)
		}
	} else if err != nil {
		return false, err
	} else {
		return false, errors.New("copy destination must be a new directory")
	}
	var copiedDirectories []struct {
		path string
		mode os.FileMode
	}
	copyErr := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to copy symbolic link %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destination, rel)
		if info.IsDir() {
			mode := info.Mode().Perm()
			if err := os.Mkdir(target, mode|0o700); err != nil {
				return err
			}
			copiedDirectories = append(copiedDirectories, struct {
				path string
				mode os.FileMode
			}{target, mode})
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to copy non-regular file %s", path)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		return os.Chmod(target, info.Mode().Perm())
	})
	if copyErr != nil {
		_ = os.RemoveAll(destination)
		return true, copyErr
	}
	for i := len(copiedDirectories) - 1; i >= 0; i-- {
		if err := os.Chmod(copiedDirectories[i].path, copiedDirectories[i].mode); err != nil {
			_ = os.RemoveAll(destination)
			return true, err
		}
	}
	return true, nil
}

type pendingDataRoot struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	SwitchOnly  bool   `json:"switchOnly,omitempty"`
}

var migrationQueueMu sync.Mutex

func pendingDataRootPath() string {
	path := LocatorPath()
	if path == "" {
		return ""
	}
	return path + ".pending"
}
func dataRootStatusPath() string {
	path := LocatorPath()
	if path == "" {
		return ""
	}
	return path + ".status"
}

// QueueDataRootMigration schedules an explicit, restart-bound relocation.
func QueueDataRootMigration(source, destination string) error {
	return queueDataRoot(source, destination, false)
}

// QueueDataRootSwitch selects a directory on restart without copying or moving data.
func QueueDataRootSwitch(source, destination string) error {
	return queueDataRoot(source, destination, true)
}

func queueDataRoot(source, destination string, switchOnly bool) error {
	migrationQueueMu.Lock()
	defer migrationQueueMu.Unlock()
	var err error
	if switchOnly {
		destination, err = ValidateDataRoot(destination)
	} else {
		destination, err = ValidateNewDataRoot(destination)
	}
	if err != nil {
		return err
	}
	if filepath.Clean(source) == destination {
		return errors.New("destination is already the active data directory")
	}
	if inside(source, destination) || inside(destination, source) {
		return errors.New("source and destination data directories cannot contain one another")
	}
	if !switchOnly {
		if _, err := os.Lstat(destination); err == nil {
			return errors.New("destination must be a new directory; RunPilot will not merge or overwrite existing contents")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	locator := pendingDataRootPath()
	if locator == "" {
		return errors.New("persistent data-root locator is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(locator), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(locator); err == nil {
		return errors.New("a data-root relocation is already pending")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	value, _ := json.Marshal(pendingDataRoot{Source: source, Destination: destination, SwitchOnly: switchOnly})
	tmp := locator + ".tmp"
	if err := os.WriteFile(tmp, value, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, locator); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.WriteFile(dataRootStatusPath(), []byte("pending restart\n"), 0o600)
	return nil
}

// ApplyPendingDataRoot runs after managed activity has stopped and before managers reopen.
func ApplyPendingDataRoot() error {
	path := pendingDataRootPath()
	if path == "" {
		return nil
	}
	value, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending pendingDataRoot
	if err = json.Unmarshal(value, &pending); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("invalid pending data-root migration: %w", err)
	}
	active, err := ResolveDataDir("")
	if err != nil {
		return err
	}
	if filepath.Clean(active) != filepath.Clean(pending.Source) {
		_ = os.Remove(path)
		return errors.New("pending data-root migration no longer matches the active deployment")
	}
	created := false
	var copyErr error
	if pending.SwitchOnly {
		_, copyErr = ValidateDataRoot(pending.Destination)
	} else {
		created, copyErr = CopyDataRoot(pending.Source, pending.Destination)
		if copyErr == nil {
			copyErr = VerifyDataRoot(pending.Source, pending.Destination)
		}
	}
	if copyErr == nil {
		copyErr = WriteDataRoot(pending.Destination)
	}
	if copyErr != nil && created {
		_ = os.RemoveAll(pending.Destination)
	}
	status := []byte("complete\n")
	if copyErr != nil {
		status = []byte("failed: " + copyErr.Error() + "\n")
	}
	_ = os.WriteFile(dataRootStatusPath(), status, 0o600)
	_ = os.Remove(path)
	return copyErr
}

func VerifyDataRoot(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source contains symbolic link %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destination, rel)
		other, err := os.Lstat(target)
		if err != nil {
			return fmt.Errorf("copied data is missing %s", rel)
		}
		if info.IsDir() {
			if !other.IsDir() {
				return fmt.Errorf("copied path type mismatch %s", rel)
			}
			return nil
		}
		if !info.Mode().IsRegular() || !other.Mode().IsRegular() || info.Size() != other.Size() {
			return fmt.Errorf("copied file mismatch %s", rel)
		}
		left, err := fileDigest(path)
		if err != nil {
			return err
		}
		right, err := fileDigest(target)
		if err != nil {
			return err
		}
		if !bytes.Equal(left[:], right[:]) {
			return fmt.Errorf("copied file verification failed %s", rel)
		}
		return nil
	})
}

func fileDigest(path string) ([32]byte, error) {
	var result [32]byte
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return result, err
	}
	copy(result[:], h.Sum(nil))
	return result, nil
}
func inside(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
