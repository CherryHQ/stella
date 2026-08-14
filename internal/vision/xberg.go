package vision

import (
	"context"
	"fmt"
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

// xbergTimeout bounds local extraction for canonical baseline fallback.
const xbergTimeout = 60 * time.Second

// ExtractWithXberg shells out to the Xberg CLI to extract text from a
// file. It returns an error when the binary is missing or extraction fails.
func ExtractWithXberg(ctx context.Context, path string) (string, error) {
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
	cmd := exec.CommandContext(cctx, bin, "extract", path)
	// Xberg auto-discovers config from its cwd and parents. Anchor discovery to
	// the input file instead of leaking stellad's operator-controlled cwd.
	cmd.Dir = filepath.Dir(path)
	if managedShim {
		miseEnv := manifestplugins.RuntimeMiseEnv(stellaHome, "", "", "")
		// RuntimeMiseEnv uses the sandbox's /tmp by default; this command runs in
		// the daemon process, so use the host platform's temporary directory.
		miseEnv["MISE_STATE_DIR"] = filepath.Join(os.TempDir(), "stella-mise-state")
		cmd.Env = withEnvOverrides(os.Environ(), miseEnv)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("xberg returned no text")
	}
	return text, nil
}

// extractBytesWithXberg stages data in a temporary file so Xberg — which only
// reads from disk — can extract text from image bytes that never came from a
// file the daemon can reach.
func extractBytesWithXberg(ctx context.Context, data []byte, mime string) (string, error) {
	dir, err := os.MkdirTemp("", "stella-vision-")
	if err != nil {
		return "", fmt.Errorf("stage image for xberg: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "image"+extensionForMime(mime))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("stage image for xberg: %w", err)
	}
	return ExtractWithXberg(ctx, path)
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
