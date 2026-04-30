package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	internaltools "github.com/vaayne/anna/internal/tools"
)

// ensureAnnaCLIInPath makes the currently running anna executable available at
// $ANNA_HOME/bin/anna for local sandbox sessions, whose PATH is intentionally
// restricted to anna-managed and system directories.
func ensureAnnaCLIInPath(annaHome string) error {
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
	if current, err := filepath.EvalSymlinks(dest); err == nil && current == source {
		return nil
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace existing anna cli link: %w", err)
	}
	if err := os.Symlink(source, dest); err == nil {
		return nil
	}
	if err := copyExecutable(source, dest); err != nil {
		return fmt.Errorf("link or copy anna cli into sandbox path: %w", err)
	}
	return nil
}

func copyExecutable(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(0o755)
}
