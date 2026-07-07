package sandbox

import (
	"context"
	"io"
	"time"
)

// Session is the plugin-facing sandbox surface: lifecycle + mediated host access.
// It combines what was previously the Session lifecycle interface and the Host
// file/process interface into a single type so plugins receive one coherent value.
type Session interface {
	// Lifecycle
	Policy() Policy
	Close() error
	Alive() bool
	Done() <-chan struct{}

	// Host process surface
	Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error)
	StartProcess(ctx context.Context, req ProcessRequest) (ProcessHandle, error)

	// Path resolution — use os.* with the resolved path for file I/O.
	// ResolvePath validates read access; use ResolveWritePath for write operations.
	ResolvePath(path string) (string, error)
	// ResolveWritePath is like ResolvePath but additionally rejects paths in
	// read-only mounts.
	ResolveWritePath(path string) (string, error)
	WorkingDir() string
}

// Host is an alias for Session kept for internal use by the runner and core tools.
// New code should use Session directly.
type Host = Session

// EnvRefresher is implemented by sessions whose injected environment can be
// updated after creation, so a caller can rotate a credential (e.g. an expiring
// STELLA_TOKEN) without tearing down and recreating the session. Updates are
// applied atomically and are visible to every subsequent Exec/StartProcess.
// Sessions that don't run real processes (e.g. the no-op session) may omit it.
type EnvRefresher interface {
	RefreshEnv(updates map[string]string)
}

type ExecOptions struct {
	Cwd     string
	Env     map[string]string
	Timeout time.Duration
}

type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type ProcessRequest struct {
	Path    string
	Args    []string
	Cwd     string
	Env     map[string]string
	Timeout time.Duration
}

type ProcessHandle interface {
	PID() int
	Wait(ctx context.Context) (ExecResult, error)
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Close() error
}

// DirEntry is retained for use by prompt_host.go which reads directories via os.ReadDir
// and needs a uniform entry type shared with sandbox callers.
type DirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

// NopSession returns a no-op session for testing.
func NopSession() Session {
	return &nopSession{
		policy: Policy{
			Network: NetworkPolicy{
				Mode: NetworkAllowAll,
			},
		},
		done: make(chan struct{}),
	}
}

type nopSession struct {
	policy Policy
	done   chan struct{}
	closed bool
}

func (s *nopSession) Policy() Policy        { return s.policy }
func (s *nopSession) Alive() bool           { return !s.closed }
func (s *nopSession) Done() <-chan struct{} { return s.done }
func (s *nopSession) Close() error {
	s.closed = true
	close(s.done)
	return nil
}

func (s *nopSession) Exec(_ context.Context, _ string, _ ExecOptions) (ExecResult, error) {
	return ExecResult{}, nil
}

func (s *nopSession) StartProcess(_ context.Context, _ ProcessRequest) (ProcessHandle, error) {
	return nil, nil
}
func (s *nopSession) ResolvePath(path string) (string, error)      { return path, nil }
func (s *nopSession) ResolveWritePath(path string) (string, error) { return path, nil }
func (s *nopSession) WorkingDir() string                           { return "" }
