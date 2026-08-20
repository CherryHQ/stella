package vision

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/resources/binaries"
)

// xbergTimeout bounds local extraction for canonical baseline fallback.
const xbergTimeout = 60 * time.Second

// ExtractWithXberg shells out to the Xberg CLI to extract text from a
// file. It returns an error when the binary is missing or extraction fails.
func ExtractWithXberg(ctx context.Context, path string) (string, error) {
	// Xberg ships embedded in stellad and is extracted to $STELLA_HOME/bin; it
	// is not a mise tool, so it has no shim. Looking under the mise shims dir —
	// which this used to do — never resolves, and the PATH fallback then misses
	// too because the daemon's own PATH does not carry $STELLA_HOME/bin. PATH is
	// kept only for a host that installed the CLI itself.
	bin := binaries.ToolPath(config.StellaHome(), "xberg")
	if bin == "" {
		found, err := exec.LookPath("xberg")
		if err != nil {
			return "", fmt.Errorf("xberg not available: %w", err)
		}
		bin = found
	}
	cctx, cancel := context.WithTimeout(ctx, xbergTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "extract", path)
	// Xberg auto-discovers config from its cwd and parents. Anchor discovery to
	// the input file instead of leaking stellad's operator-controlled cwd.
	cmd.Dir = filepath.Dir(path)
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
