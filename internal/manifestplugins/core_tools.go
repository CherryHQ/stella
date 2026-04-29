package manifestplugins

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// fdVersion is the default fd release to install via mise.
// darwin/amd64 uses a separate older release because fd 10.4.2 has no
// x86_64-apple-darwin asset.
const (
	fdVersion            = "10.4.2"
	fdDarwinAMD64Version = "10.3.0"
	rgVersion            = "15.1.0"
)

type coreState struct {
	once sync.Once
	err  error
}

var (
	coreMu     sync.Mutex
	coreStates = make(map[string]*coreState)
)

// EnsureCoreBinaries installs fd and rg into annaHome/bin/ using the
// embedded mise binary. It is idempotent within a single process (sync.Once
// per annaHome) and skips already-present binaries. Non-fatal: callers should
// log and continue when this returns an error.
func EnsureCoreBinaries(ctx context.Context, annaHome string) error {
	coreMu.Lock()
	state := coreStates[annaHome]
	if state == nil {
		state = &coreState{}
		coreStates[annaHome] = state
	}
	coreMu.Unlock()

	state.once.Do(func() {
		state.err = installCoreBinaries(ctx, annaHome)
	})
	return state.err
}

func installCoreBinaries(ctx context.Context, annaHome string) error {
	fdBin := filepath.Join(annaHome, "bin", runtimeBinaryName("fd"))
	rgBin := filepath.Join(annaHome, "bin", runtimeBinaryName("rg"))

	if fileExists(fdBin) && fileExists(rgBin) {
		return nil
	}

	if !fileExists(fdBin) {
		ver := fdVersion
		if runtime.GOOS == "darwin" && runtime.GOARCH == "amd64" {
			ver = fdDarwinAMD64Version
		}
		slog.Info("installing fd via mise", "version", ver)
		if _, err := installBinaryWithMise(ctx, ManifestBinary{
			Name:    "fd",
			Tool:    "github:sharkdp/fd",
			Version: ver,
		}, annaHome); err != nil {
			return fmt.Errorf("install fd: %w", err)
		}
	}

	if !fileExists(rgBin) {
		slog.Info("installing rg via mise", "version", rgVersion)
		if _, err := installBinaryWithMise(ctx, ManifestBinary{
			Name:    "rg",
			Tool:    "github:BurntSushi/ripgrep",
			Version: rgVersion,
		}, annaHome); err != nil {
			return fmt.Errorf("install rg: %w", err)
		}
	}

	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
