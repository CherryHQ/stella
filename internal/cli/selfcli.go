package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// EnsureStellaCLIInPath installs the stella CLI binary into
// $STELLA_HOME/bin/stella for sandbox sessions, whose PATH is intentionally
// restricted to stella-managed and system directories.
//
// When the running process is stellad (the daemon), the stella CLI binary is
// located alongside it in the same directory — goreleaser, Homebrew, and nfpm
// all install both binaries to the same prefix. If the companion binary is
// not found, the function falls back to copying the running executable.
//
// Do not use a symlink: sandbox path resolution rejects symlink traversal.
func EnsureStellaCLIInPath(stellaHome string) error {
	if stellaHome == "" {
		return fmt.Errorf("stella home is required")
	}

	source, err := resolveStellaBinary()
	if err != nil {
		return err
	}

	binDir := filepath.Join(stellaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create stella bin dir: %w", err)
	}

	dest := filepath.Join(binDir, "stella")
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open executable: %w", err)
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(binDir, ".stella-*")
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
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing binary: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("install executable: %w", err)
	}
	return nil
}

// resolveStellaBinary finds the stella CLI binary. If the running process is
// stellad, it looks for a stella binary in the same directory. Falls back to
// the running executable itself (for the case where stella is the running process).
func resolveStellaBinary() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", fmt.Errorf("resolve executable symlink: %w", err)
	}

	base := filepath.Base(self)
	if base == "stellad" || base == "stellad.exe" {
		companion := filepath.Join(filepath.Dir(self), "stella")
		if _, err := os.Stat(companion); err == nil {
			return companion, nil
		}
	}

	return self, nil
}
