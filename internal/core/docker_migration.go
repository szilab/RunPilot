package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// migrateLegacyDockerProjects is a transitional, one-way copy. The source
// remains untouched for Storage's legacy Docker-volume integration and rollback.
func migrateLegacyDockerProjects(dataDir string) error {
	source := filepath.Join(dataDir, "compose")
	info, statErr := os.Lstat(source)
	if os.IsNotExist(statErr) {
		return nil
	}
	if statErr != nil {
		return statErr
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("legacy Docker project root is not a regular directory")
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	workspace, err := (&Controller{dataDir: dataDir}).workspaceRoot("docker")
	if err != nil {
		return err
	}
	defer workspace.Close()
	if err := workspaceMkdir(workspace, "projects"); err != nil {
		return err
	}
	target, err := (&Controller{dataDir: dataDir}).workspaceHostPath("docker", "projects")
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		name := entry.Name()
		if !legacyProjectName(name) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		dest := filepath.Join(target, name)
		if _, err := os.Lstat(dest); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			failures = append(failures, fmt.Errorf("migrate legacy Docker project %s: %w", name, err))
			continue
		}
		tmp, err := os.MkdirTemp(target, ".migration-")
		if err != nil {
			failures = append(failures, fmt.Errorf("migrate legacy Docker project %s: %w", name, err))
			continue
		}
		err = copyLegacyProject(filepath.Join(source, name), tmp)
		if err == nil {
			var marker *os.File
			marker, err = os.OpenFile(filepath.Join(tmp, ".runpilot-legacy-origin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				_, err = marker.WriteString(filepath.Join(source, name))
				if closeErr := marker.Close(); err == nil {
					err = closeErr
				}
			}
		}
		if err == nil {
			err = os.Rename(tmp, dest)
		}
		if err != nil {
			_ = os.RemoveAll(tmp)
			failures = append(failures, fmt.Errorf("migrate legacy Docker project %s: %w", name, err))
		}
	}
	return errors.Join(failures...)
}
func legacyProjectName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && (r == '-' || r == '_')) {
			return false
		}
	}
	return true
}
func copyLegacyProject(src, dst string) error {
	// Service data below a project often belongs to containers and is neither
	// readable by RunPilot nor safe to move to a new bind-mount path. Compose
	// actions retain src as their project directory; only editable control
	// files need a workspace copy.
	composeFileCopied := false
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml", ".env"} {
		from, to := filepath.Join(src, name), filepath.Join(dst, name)
		fi, err := os.Lstat(from)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() || fi.Size() > 16<<20 {
			return fmt.Errorf("project control file %s is unsupported or oversized", name)
		}
		input, err := os.Open(from)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fi.Mode().Perm())
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.CopyN(output, input, fi.Size())
		closeErr := output.Close()
		input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if name != ".env" {
			composeFileCopied = true
		}
	}
	if !composeFileCopied {
		return fmt.Errorf("project has no Compose file")
	}
	return nil
}
