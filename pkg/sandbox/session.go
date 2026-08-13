package sandbox

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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

	// Files returns mediated access to the process-visible filesystem. Physical
	// provider paths never cross this boundary.
	Files() FileAccess
	WorkingDir() string
}

// Host is an alias for Session kept for internal use by the runner and core tools.
// New code should use Session directly.
type Host = Session

// EnvRefresher is implemented by sessions whose injected environment can be
// updated after creation, so a caller can rotate a credential (e.g. an expiring
// OAuth access token) without tearing down and recreating the session. Updates
// are applied atomically and are visible to every subsequent Exec/StartProcess.
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

// DirEntry is the provider-neutral directory metadata used by prompt and tool
// callers without exposing an os.DirEntry backed by a physical provider path.
type DirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

// FileInfo is the metadata core tools need without exposing an os.FileInfo
// backed by a provider path.
type FileInfo struct {
	IsDir bool
	Size  int64
}

// ProjectedFile is one file in an exact-at-publication, no-replace, disposable
// Session projection.
type ProjectedFile struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

// ErrProjectionConflict reports that an exact projection path already exists
// with a different tree, mode, or content.
var ErrProjectionConflict = errors.New("sandbox: projection conflicts with exact files")

// FileAccess is the narrow data-filesystem capability used by prompt
// construction, core tools, and exact per-Session projections. Paths use the
// same coordinates as WorkingDir and Policy.Env. Provider runtime/image paths
// may be executable by a process without belonging to this authorized data
// view; cross-mount symlinks fail closed rather than changing capabilities.
type FileAccess interface {
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]DirEntry, error)
	Stat(path string) (FileInfo, error)
	WriteFile(path string, content []byte, mode fs.FileMode) error
	ProjectFiles(path string, files []ProjectedFile) error
}

// NopSession returns an identity-view no-op session for testing. Its FileAccess
// delegates directly to the host filesystem and is not a sandbox boundary.
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
func (s *nopSession) Files() FileAccess  { return directFileAccess{} }
func (s *nopSession) WorkingDir() string { return "" }

type directFileAccess struct{}

func (directFileAccess) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (directFileAccess) ReadDir(path string) ([]DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil {
			out = append(out, DirEntry{Name: entry.Name(), IsDir: entry.IsDir(), Size: info.Size()})
		}
	}
	return out, nil
}

func (directFileAccess) Stat(path string) (FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{IsDir: info.IsDir(), Size: info.Size()}, nil
}

func (directFileAccess) WriteFile(path string, content []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, mode)
}

func (directFileAccess) ProjectFiles(string, []ProjectedFile) error {
	return os.ErrPermission
}
