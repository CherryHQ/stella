package builddeps

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
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
// When Docker/overlayfs makes rename return EXDEV, it falls back to copy+remove.
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
		if err := renameOrCopyDir(dest, backup); err != nil {
			// Another concurrent process may have already moved dest away.
			if os.IsNotExist(err) {
				backup = ""
			} else {
				return fmt.Errorf("backup existing dir: %w", err)
			}
		}
	}

	if err := renameOrCopyDir(src, dest); err != nil {
		if backup != "" {
			if restoreErr := renameOrCopyDir(backup, dest); restoreErr != nil {
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

func renameOrCopyDir(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		return err
	}

	if err := copyDir(src, dest); err != nil {
		return err
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("remove source dir after copy: %w", err)
	}
	return nil
}

func copyDir(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("compute relative path: %w", err)
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("create dir %s: %w", target, err)
			}
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dest string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", dest, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dest, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dest, err)
	}
	return nil
}

func isCrossDevice(err error) bool {
	var linkErr *os.LinkError
	return errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV)
}
