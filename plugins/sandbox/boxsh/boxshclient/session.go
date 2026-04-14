package boxshclient

import (
	"fmt"
	"os"
	"path/filepath"
)

// SessionManager handles the lifecycle of sandbox sessions including sandbox
// root selection, ephemeral directory creation, and cleanup.
type SessionManager struct {
	baseDir string // Base directory for ephemeral session directories
}

// SessionOptions configures a sandbox session.
type SessionOptions struct {
	// SandboxRoot is the writable root mounted into the sandbox.
	SandboxRoot string

	// WorkDir is the working directory for tool execution.
	WorkDir string

	// Sandbox contains the network policy configuration.
	Sandbox NetworkConfig
}

// SessionInfo holds the resolved session configuration.
type SessionInfo struct {
	// Src is the source workspace (read-only lower layer).
	Src string

	// Dst is the ephemeral destination upperdir (read-write layer).
	Dst string

	// Cwd is the working directory inside the sandbox.
	Cwd string

	// NetworkMode is the effective network mode.
	NetworkMode string

	// NetworkAllowlist contains allowed hosts/CIDRs for whitelist mode.
	NetworkAllowlist []string
}

// NewSessionManager creates a new session manager with the given base directory.
// If baseDir is empty, uses AnnaHome/cache/sandbox/sessions.
func NewSessionManager(baseDir string) (*SessionManager, error) {
	if baseDir == "" {
		annaHome := DefaultAnnaHome()
		baseDir = filepath.Join(annaHome, "cache", "sandbox", "sessions")
	}

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("session manager: create base dir: %w", err)
	}

	return &SessionManager{baseDir: baseDir}, nil
}

// CreateSession creates a new sandbox session with an ephemeral upperdir.
// The caller is responsible for calling CleanupSession to remove the ephemeral directory.
func (m *SessionManager) CreateSession(opts SessionOptions) (*SessionInfo, error) {
	src := opts.SandboxRoot
	if src == "" {
		return nil, fmt.Errorf("session manager: sandbox_root is required")
	}

	// Validate that src is an absolute path and exists.
	if !filepath.IsAbs(src) {
		return nil, fmt.Errorf("session manager: sandbox root must be absolute: %q", src)
	}

	info, err := os.Stat(src)
	if err != nil {
		return nil, fmt.Errorf("session manager: stat sandbox root %q: %w", src, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("session manager: sandbox root %q is not a directory", src)
	}

	// Create ephemeral session directory.
	dst, err := m.createSessionDir()
	if err != nil {
		return nil, err
	}

	// Resolve working directory.
	cwd := m.resolveCwd(src, opts.WorkDir)

	// Determine network mode.
	networkMode := opts.Sandbox.ModeOrDefault()

	session := &SessionInfo{
		Src:              src,
		Dst:              dst,
		Cwd:              cwd,
		NetworkMode:      networkMode,
		NetworkAllowlist: opts.Sandbox.Allowlist,
	}

	return session, nil
}

// CleanupSession removes the ephemeral session directory.
func (m *SessionManager) CleanupSession(session *SessionInfo) error {
	if session == nil || session.Dst == "" {
		return nil
	}
	return CleanupSessionDir(session.Dst)
}

// createSessionDir creates a new ephemeral session directory.
func (m *SessionManager) createSessionDir() (string, error) {
	sessionDir, err := os.MkdirTemp(m.baseDir, "boxsh-session-*")
	if err != nil {
		return "", fmt.Errorf("session manager: create session dir: %w", err)
	}
	return sessionDir, nil
}

// resolveCwd determines the effective working directory inside the sandbox.
func (m *SessionManager) resolveCwd(sandboxRoot, workDir string) string {
	return ResolveSandboxCwd(sandboxRoot, workDir)
}

// BuildSessionConfig builds a boxsh SessionConfig from SessionInfo.
func BuildSessionConfig(info *SessionInfo) SessionConfig {
	return SessionConfig{
		Src:              info.Src,
		Dst:              info.Dst,
		Cwd:              info.Cwd,
		NetworkMode:      info.NetworkMode,
		NetworkAllowlist: info.NetworkAllowlist,
	}
}

// ValidateSandboxPath checks that a path is within the allowed sandbox boundaries.
// This is a defensive check used before passing paths to the boxsh client.
func ValidateSandboxPath(sandboxRoot, path string) error {
	if sandboxRoot == "" {
		return fmt.Errorf("sandbox root is required")
	}

	// Clean and resolve paths.
	cleanRoot := filepath.Clean(sandboxRoot)
	cleanPath := filepath.Clean(path)

	// Ensure both are absolute.
	if !filepath.IsAbs(cleanRoot) {
		return fmt.Errorf("sandbox root must be absolute: %q", sandboxRoot)
	}
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(cleanRoot, cleanPath)
	}

	// Check that path is under the sandbox root.
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return fmt.Errorf("path validation error: %w", err)
	}

	if rel == ".." || len(rel) > 2 && rel[:3] == "../" {
		return fmt.Errorf("path %q is outside sandbox root %q", path, sandboxRoot)
	}

	return nil
}
