package builddeps

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteFile writes content to path via a temp file in the same directory.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// AtomicReplaceDir swaps dest with src by renaming src into place.
// If dest already exists, it is first moved aside and restored if the final rename fails.
// src must already exist on the same filesystem as dest.
func AtomicReplaceDir(src, dest string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	backup := ""
	if _, err := os.Stat(dest); err == nil {
		backup = filepath.Join(parent, ".backup-"+filepath.Base(dest))
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove old backup dir: %w", err)
		}
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("backup existing dir: %w", err)
		}
	}

	if err := os.Rename(src, dest); err != nil {
		if backup != "" {
			if restoreErr := os.Rename(backup, dest); restoreErr != nil {
				return fmt.Errorf("rename dir: %w (restore failed: %w)", err, restoreErr)
			}
		}
		return fmt.Errorf("rename dir: %w", err)
	}
	if backup != "" {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove backup dir: %w", err)
		}
	}
	return nil
}
