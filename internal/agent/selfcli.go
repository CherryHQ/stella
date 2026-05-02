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
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open executable: %w", err)
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(binDir, ".anna-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy executable: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod executable: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	// Remove any existing file or symlink before rename so the destination is
	// always a plain regular file owned by this process.
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing binary: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("install executable: %w", err)
	}
	return nil
}
