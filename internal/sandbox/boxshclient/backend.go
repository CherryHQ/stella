package boxshclient

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/sandbox"
)

// SharedBackend provides a shared boxsh session for all core tools.
// It manages the lifecycle of a single boxsh --rpc process that is
// reused by bash, read, write, and edit operations.
type SharedBackend struct {
	client       *Client
	binaryPath   string
	sessionDir   string // ephemeral DST directory
	cleanupOnce  sync.Once
	mu           sync.RWMutex
}

// BackendConfig configures the shared backend.
type BackendConfig struct {
	// BinaryPath is the path to the boxsh binary. If empty, uses managed binary.
	BinaryPath string

	// AnnaHome is the Anna home directory (used for binary resolution).
	AnnaHome string

	// Workspace is the agent workspace directory.
	Workspace string

	// UserDataDir is the per-user data directory (for user sessions).
	UserDataDir string

	// Sandbox contains the sandbox network configuration.
	Sandbox config.SandboxConfig

	// WorkDir is the working directory for tool execution.
	// This is resolved relative to the sandbox root.
	WorkDir string

	// SessionBaseDir is the base directory for ephemeral session directories.
	// Defaults to AnnaHome/cache/sandbox/sessions.
	SessionBaseDir string
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
			annaHome = config.AnnaHome()
		}
		path, err := sandbox.ResolveManagedBoxshPath(annaHome)
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
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client != nil {
		return fmt.Errorf("boxshclient: backend already started")
	}

	// Determine sandbox root (SRC).
	src := sandbox.SandboxRoot(sandbox.PreflightConfig{
		Workspace:   cfg.Workspace,
		UserDataDir: cfg.UserDataDir,
	})

	// Create ephemeral session directory (DST).
	sessionBaseDir := cfg.SessionBaseDir
	if sessionBaseDir == "" {
		annaHome := cfg.AnnaHome
		if annaHome == "" {
			annaHome = config.AnnaHome()
		}
		sessionBaseDir = filepath.Join(annaHome, "cache", "sandbox", "sessions")
	}

	sessionDir, err := CreateSessionDir(sessionBaseDir)
	if err != nil {
		return fmt.Errorf("boxshclient: create session dir: %w", err)
	}
	b.sessionDir = sessionDir

	// Resolve working directory inside the sandbox.
	// The effective sandbox root for the session is the overlay of SRC -> DST.
	// Paths resolve against DST first, then fall back to SRC.
	cwd := ResolveSandboxCwd(src, cfg.WorkDir)

	// Build session configuration.
	sessionCfg := SessionConfig{
		Src:              src,
		Dst:              sessionDir,
		Cwd:              cwd,
		NetworkMode:      cfg.Sandbox.NetworkMode(),
		NetworkAllowlist: cfg.Sandbox.Network.Allowlist,
	}

	// Create and start the client.
	client := New(b.binaryPath, sessionCfg)
	if err := client.Start(ctx); err != nil {
		_ = CleanupSessionDir(sessionDir)
		b.sessionDir = ""
		return fmt.Errorf("boxshclient: start client: %w", err)
	}

	b.client = client
	return nil
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
	b.mu.Unlock()

	var errs []error

	if client != nil {
		if err := client.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	// Cleanup session directory once.
	b.cleanupOnce.Do(func() {
		if sessionDir != "" {
			if err := CleanupSessionDir(sessionDir); err != nil {
				errs = append(errs, err)
			}
		}
	})

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

// SandboxRoot returns the source sandbox root (SRC).
func (b *SharedBackend) SandboxRoot(ctx context.Context) (string, error) {
	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()

	if client == nil {
		return "", fmt.Errorf("boxshclient: backend not started")
	}

	// The sandbox root is the SRC directory.
	// We can determine this by stat-ing a known path or returning cached value.
	// For now, we return the session configuration's Src field via reflection
	// on the client configuration.
	return client.sessionConfig.Src, nil
}

// IsSharedBackendError reports whether an error originated from the shared backend.
func IsSharedBackendError(err error) bool {
	if err == nil {
		return false
	}
	// Check if the error message contains boxshclient prefix.
	return len(err.Error()) > 12 && err.Error()[:12] == "boxshclient:"
}
