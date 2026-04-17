package boxshclient

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"go.opentelemetry.io/otel/attribute"
)

// SharedBackend provides a shared boxsh session for all core tools.
// It manages the lifecycle of a single boxsh --rpc process that is
// reused by bash, read, write, and edit operations.
type SharedBackend struct {
	client      *Client
	binaryPath  string
	sessionDir  string // ephemeral overlay root exposed inside the sandbox
	sessionSrc  string
	cleanupOnce sync.Once
	mu          sync.RWMutex
}

// BackendConfig configures the shared backend.
type BackendConfig struct {
	// BinaryPath is the path to the boxsh binary. If empty, uses managed binary.
	BinaryPath string

	// AnnaHome is the Anna home directory (used for binary resolution).
	AnnaHome string

	// UserRoot is the writable root mounted into the sandbox.
	UserRoot string

	// Sandbox contains the sandbox network configuration.
	Sandbox NetworkConfig

	// WorkDir is the working directory for tool execution.
	// This is resolved relative to the user root.
	WorkDir string

	// SessionBaseDir is the base directory for ephemeral session directories.
	// Defaults to AnnaHome/cache/sandbox/sessions.
	SessionBaseDir string

	// ReadOnlyDirs are mounted read-only into the sandbox so helper binaries
	// and user PATH directories remain available from shell commands.
	ReadOnlyDirs []string
}

// NewSharedBackend creates a new shared backend without starting it.
// Use Start() to initialize the boxsh process.
func NewSharedBackend(cfg BackendConfig) (*SharedBackend, error) {
	if !PlatformSupportsBoxsh() {
		return nil, fmt.Errorf("boxshclient: platform %s does not support boxsh", runtime.GOOS)
	}

	binaryPath := cfg.BinaryPath
	if binaryPath == "" {
		annaHome := cfg.AnnaHome
		if annaHome == "" {
			annaHome = DefaultAnnaHome()
		}
		path, err := ResolveManagedBoxshPath(annaHome)
		if err != nil {
			return nil, fmt.Errorf("boxshclient: %w", err)
		}
		binaryPath = path
	}

	return &SharedBackend{
		binaryPath: binaryPath,
	}, nil
}

// Start initializes the shared backend by creating the session directory
// and starting the boxsh RPC process.
func (b *SharedBackend) Start(ctx context.Context, cfg BackendConfig) error {
	ctx, span := tracer.Start(ctx, "sandbox.boxsh.backend_start")
	defer span.End()

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client != nil {
		err := fmt.Errorf("boxshclient: backend already started")
		recordTraceError(span, err)
		return err
	}

	// Determine user root (SRC).
	src := cfg.UserRoot
	span.SetAttributes(
		attribute.String("anna.sandbox.binary", b.binaryPath),
		attribute.String("anna.sandbox.src", src),
		attribute.String("anna.sandbox.work_dir", cfg.WorkDir),
		attribute.Int("anna.sandbox.readonly_dir_count", len(uniqueCleanAbsPaths(cfg.ReadOnlyDirs))),
		attribute.String("anna.sandbox.network.mode", cfg.Sandbox.ModeOrDefault()),
	)

	// Create an ephemeral overlay root. Use AnnaHome/cache/sandbox/sessions so
	// session dirs are isolated from user workspace directories.
	sessionBaseDir := cfg.SessionBaseDir
	if sessionBaseDir == "" {
		annaHome := cfg.AnnaHome
		if annaHome == "" {
			annaHome = DefaultAnnaHome()
		}
		sessionBaseDir = filepath.Join(annaHome, "cache", "sandbox", "sessions")
	}

	sessionDir, err := CreateSessionDir(sessionBaseDir)
	if err != nil {
		err = fmt.Errorf("boxshclient: create session dir: %w", err)
		recordTraceError(span, err)
		return err
	}
	if err := os.Remove(sessionDir); err != nil {
		err = fmt.Errorf("boxshclient: prepare session dir: %w", err)
		recordTraceError(span, err)
		return err
	}
	if err := os.Mkdir(sessionDir, 0o755); err != nil {
		err = fmt.Errorf("boxshclient: recreate session dir: %w", err)
		recordTraceError(span, err)
		return err
	}
	b.sessionDir = sessionDir
	b.sessionSrc = src

	if err := WriteSessionMeta(sessionDir, src); err != nil {
		slog.Warn("boxsh backend: write session metadata",
			"component", "boxsh_backend", "error", err, "session_dir", sessionDir)
	}

	// boxsh mounts the overlay at DST, so remap the requested workdir from SRC
	// into the overlay root.
	cwd := remapCwdToSessionRoot(src, sessionDir, cfg.WorkDir)
	span.SetAttributes(
		attribute.String("anna.sandbox.dst", sessionDir),
		attribute.String("anna.sandbox.cwd", cwd),
	)

	// Build session configuration.
	sessionCfg := SessionConfig{
		Src:              src,
		Dst:              sessionDir,
		Cwd:              cwd,
		ReadOnlyDirs:     cfg.ReadOnlyDirs,
		NetworkMode:      cfg.Sandbox.ModeOrDefault(),
		NetworkAllowlist: cfg.Sandbox.Allowlist,
	}

	// Create and start the client.
	client := New(b.binaryPath, sessionCfg)
	if err := client.Start(ctx); err != nil {
		recordTraceError(span, err)
		slog.Warn("boxsh backend failed to start client",
			"component", "boxsh_backend",
			"binary", b.binaryPath,
			"src", src,
			"dst", sessionDir,
			"cwd", cwd,
			"readonly_dirs", uniqueCleanAbsPaths(cfg.ReadOnlyDirs),
			"network_mode", sessionCfg.NetworkMode,
			"stderr", client.Stderr(),
			"error", err,
		)
		_ = CleanupSessionDir(sessionDir)
		b.sessionDir = ""
		return fmt.Errorf("boxshclient: start client: %w", err)
	}

	b.client = client
	span.AddEvent("sandbox.boxsh.backend.started")
	slog.Info("boxsh backend started",
		"component", "boxsh_backend",
		"binary", b.binaryPath,
		"src", src,
		"dst", sessionDir,
		"cwd", cwd,
		"readonly_dir_count", len(uniqueCleanAbsPaths(cfg.ReadOnlyDirs)),
		"network_mode", sessionCfg.NetworkMode,
	)
	return nil
}

// Sync copies changed files from the session overlay back to the source
// workspace without closing the session.
func (b *SharedBackend) Sync() error {
	b.mu.RLock()
	sessionDir := b.sessionDir
	src := b.sessionSrc
	b.mu.RUnlock()

	if sessionDir == "" || src == "" {
		return nil
	}
	return SyncSessionToSrc(sessionDir, src)
}

// Client returns the underlying boxsh client for direct RPC calls.
// Returns nil if the backend is not started or already closed.
func (b *SharedBackend) Client() *Client {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.client
}

// Alive reports whether the shared backend is healthy and the boxsh process is running.
func (b *SharedBackend) Alive() bool {
	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()

	if client == nil {
		return false
	}
	return client.Alive()
}

// Close shuts down the shared backend, terminating the boxsh process
// and cleaning up the ephemeral session directory.
func (b *SharedBackend) Close() error {
	b.mu.Lock()
	client := b.client
	b.client = nil
	sessionDir := b.sessionDir
	b.sessionDir = ""
	src := b.sessionSrc
	b.sessionSrc = ""
	b.mu.Unlock()

	var errs []error

	if client != nil {
		if err := client.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	// Sync changes back to the source workspace, then clean up the session dir.
	b.cleanupOnce.Do(func() {
		if sessionDir == "" {
			return
		}
		if src != "" {
			if err := SyncSessionToSrc(sessionDir, src); err != nil {
				slog.Warn("boxsh backend: sync session to src",
					"component", "boxsh_backend", "error", err, "dst", sessionDir, "src", src)
				errs = append(errs, fmt.Errorf("sync session: %w", err))
			}
		}
		if err := CleanupSessionDir(sessionDir); err != nil {
			errs = append(errs, err)
		}
	})

	slog.Info("boxsh backend stopped",
		"component", "boxsh_backend",
		"binary", b.binaryPath,
		"dst", sessionDir,
	)
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// SessionDir returns the ephemeral session directory path (DST).
// This is primarily for testing and debugging.
func (b *SharedBackend) SessionDir() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sessionDir
}

// UserRoot returns the sandbox-visible user root for path resolution.
func (b *SharedBackend) UserRoot(ctx context.Context) (string, error) {
	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()

	if client == nil {
		return "", fmt.Errorf("boxshclient: backend not started")
	}

	_ = ctx
	return client.sessionConfig.Dst, nil
}

// IsSharedBackendError reports whether an error originated from the shared backend.
func IsSharedBackendError(err error) bool {
	if err == nil {
		return false
	}
	// Check if the error message contains boxshclient prefix.
	return len(err.Error()) > 12 && err.Error()[:12] == "boxshclient:"
}

func remapCwdToSessionRoot(srcRoot, dstRoot, workDir string) string {
	srcCwd := ResolveSandboxCwd(srcRoot, workDir)
	if srcCwd == srcRoot {
		return dstRoot
	}
	rel, err := filepath.Rel(srcRoot, srcCwd)
	if err != nil || rel == "." {
		return dstRoot
	}
	return filepath.Join(dstRoot, rel)
}

// ResolveManagedBoxshPath returns the path to the managed boxsh binary in annaHome.
func ResolveManagedBoxshPath(annaHome string) (string, error) {
	binDir := filepath.Join(annaHome, "bin")
	path := filepath.Join(binDir, "boxsh")

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("sandbox: stat managed boxsh binary: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("sandbox: managed boxsh path %q is a directory", path)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("sandbox: managed boxsh binary %q is not executable", path)
	}

	return path, nil
}
