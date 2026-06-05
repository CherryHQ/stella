package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnsureStellaCLIInPath installs the stella CLI binary into
// $STELLA_HOME/bin/stella for sandbox sessions, whose PATH is intentionally
// restricted to stella-managed and system directories.
//
// When the running process is stellad (the daemon), the stella CLI binary is
// located alongside it in the same directory — goreleaser, Homebrew, and nfpm
// all install both binaries to the same prefix. If the companion binary is
// not found, the function returns an error rather than copying the daemon.
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

// stellaCompanionName returns the expected stella CLI binary name for the
// current OS ("stella" on unix, "stella.exe" on windows).
func stellaCompanionName() string {
	if runtime.GOOS == "windows" {
		return "stella.exe"
	}
	return "stella"
}

// isDaemonBinary reports whether the given base name is a stellad binary.
func isDaemonBinary(base string) bool {
	base = strings.ToLower(base)
	return base == "stellad" || base == "stellad.exe"
}

// resolveStellaBinary finds the stella CLI binary. If the running process is
// stellad, it looks for the stella companion in the same directory and errors
// if not found (never falls back to copying the daemon into sandboxes).
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
	if isDaemonBinary(base) {
		companion := filepath.Join(filepath.Dir(self), stellaCompanionName())
		if _, err := os.Stat(companion); err == nil {
			return companion, nil
		}
		return "", fmt.Errorf(
			"stella CLI binary not found alongside stellad at %s; "+
				"both binaries must be installed in the same directory",
			filepath.Dir(self),
		)
	}

	return self, nil
}
