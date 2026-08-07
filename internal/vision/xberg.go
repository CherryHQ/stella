package vision

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	// xbergTimeout bounds local extraction for canonical baseline fallback.
	xbergTimeout = 60 * time.Second
	// xbergMaxStdoutBytes is intentionally far above the durable 12k-rune
	// baseline ceiling while bounding untrusted child-process output in memory.
	xbergMaxStdoutBytes = 256 * 1024
)

// extractBytesWithXberg stages already-validated, service-owned image bytes for
// the one daemon-side Xberg invocation. The staging directory and its fixed
// filename prevent callers from selecting either the input path or Xberg cwd.
func extractBytesWithXberg(ctx context.Context, data []byte, mime string) (string, error) {
	if err := xbergFallbackSupported(); err != nil {
		return "", fmt.Errorf("xberg fallback: %w", err)
	}
	dir, err := os.MkdirTemp("", "stella-vision-")
	if err != nil {
		return "", fmt.Errorf("stage image for xberg: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "image"+extensionForMime(mime))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("stage image for xberg: %w", err)
	}
	if err := writeAndClose(file, data); err != nil {
		return "", fmt.Errorf("stage image for xberg: %w", err)
	}
	return runXberg(ctx, dir, path)
}

func writeAndClose(file *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if err != nil {
			_ = file.Close()
			return err
		}
		if n == 0 {
			_ = file.Close()
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return file.Close()
}

// runXberg is deliberately private: its path and cwd originate exclusively
// from extractBytesWithXberg's daemon-owned staging directory.
func runXberg(ctx context.Context, stagingDir, stagedPath string) (string, error) {
	// Reconciliation installs the Xberg shim under STELLA_HOME; the daemon's
	// own PATH need not contain sandbox-only tool directories.
	stellaHome := config.StellaHome()
	bin := filepath.Join(pkgsandbox.MiseShimsDir(stellaHome), "xberg")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	managedShim := true
	if _, err := os.Stat(bin); err != nil {
		managedShim = false
		bin, err = exec.LookPath("xberg")
		if err != nil {
			return "", fmt.Errorf("xberg not available: %w", err)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, xbergTimeout)
	defer cancel()
	cmd := manifestplugins.ManagedCommandContext(cctx, bin, "extract", stagedPath)
	cmd.Dir = stagingDir
	if managedShim {
		miseEnv := manifestplugins.RuntimeMiseEnv(stellaHome, "", "")
		// RuntimeMiseEnv uses the sandbox's /tmp by default; this command runs in
		// the daemon process, so use the host platform's temporary directory.
		miseEnv["MISE_STATE_DIR"] = filepath.Join(os.TempDir(), "stella-mise-state")
		cmd.Env = withEnvOverrides(os.Environ(), miseEnv)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("start xberg stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, xbergMaxStdoutBytes+1))
	if readErr != nil {
		_ = cmd.Cancel()
		waitErr := cmd.Wait()
		if err := cctx.Err(); err != nil {
			return "", err
		}
		if waitErr != nil {
			return "", waitErr
		}
		return "", fmt.Errorf("read xberg output: %w", readErr)
	}
	if len(out) > xbergMaxStdoutBytes {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		if err := cctx.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("xberg output exceeds %d bytes", xbergMaxStdoutBytes)
	}
	if err := cmd.Wait(); err != nil {
		if ctxErr := cctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", err
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("xberg returned no text")
	}
	return text, nil
}

// withEnvOverrides replaces environment values while retaining the process
// environment required by external tools (for example, PATH and certificates).
func withEnvOverrides(env []string, overrides map[string]string) []string {
	out := append([]string(nil), env...)
	for key, value := range overrides {
		prefix := key + "="
		found := false
		for i, entry := range out {
			if strings.HasPrefix(entry, prefix) {
				out[i] = prefix + value
				found = true
				break
			}
		}
		if !found {
			out = append(out, prefix+value)
		}
	}
	return out
}
