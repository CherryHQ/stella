package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	internaltools "github.com/vaayne/anna/internal/tools"
)

// EnsureAnnaCLIInPath copies the currently running anna executable to
// $ANNA_HOME/bin/anna for sandbox sessions, whose PATH is intentionally
// restricted to anna-managed and system directories. Do not use a symlink here:
// sandbox path resolution rejects symlink traversal.
func EnsureAnnaCLIInPath(annaHome string) error {
	if annaHome == "" {
		return fmt.Errorf("anna home is required")
	}

	source, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("resolve executable symlink: %w", err)
	}

	binDir := internaltools.BinDir(annaHome)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create anna bin dir: %w", err)
	}

	dest := filepath.Join(binDir, "anna")
	if upToDate, _ := sameSize(source, dest); upToDate {
		return nil
	}
	if err := copyExecutable(source, dest); err != nil {
		return fmt.Errorf("copy anna cli into sandbox path: %w", err)
	}
	return nil
}

// sameSize reports whether src and dst exist and have identical sizes.
func sameSize(src, dst string) (bool, error) {
	si, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	di, err := os.Stat(dst)
	if err != nil {
		return false, err
	}
	return si.Size() == di.Size(), nil
}

func copyExecutable(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".anna-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Remove any existing regular file or symlink after the temp copy is fully
	// written. This keeps the old path available if copying fails, while ensuring
	// a stale symlink is replaced by a real executable file.
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	return ensureRegularFile(dest)
}

func ensureRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is still a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}
