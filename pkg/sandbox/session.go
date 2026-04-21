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

	// Host file-system and process surface (previously Host interface)
	ReadFile(ctx context.Context, path string, offset, limit int) (ReadResult, error)
	WriteFile(ctx context.Context, path string, content []byte) (WriteResult, error)
	EditFile(ctx context.Context, path string, edits []Edit) (EditResult, error)
	Stat(ctx context.Context, path string) (StatResult, error)
	ListDir(ctx context.Context, path string) ([]DirEntry, error)
	MkdirAll(ctx context.Context, path string, perm uint32) error
	Remove(ctx context.Context, path string, recursive bool) error
	Rename(ctx context.Context, oldPath, newPath string) error
	CreateTemp(ctx context.Context, dir, pattern string) (TempFile, error)
	Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error)
	StartProcess(ctx context.Context, req ProcessRequest) (ProcessHandle, error)
	ResolvePath(path string) (string, error)
	WorkingDir() string
}

// Host is an alias for Session kept for internal use by the runner and core tools.
// New code should use Session directly.
type Host = Session

type ReadResult struct {
	Content    []byte
	Truncated  bool
	NextOffset int
}

type WriteResult struct {
	BytesWritten int
}

type Edit struct {
	OldText string
	NewText string
}

type EditResult struct {
	AppliedEdits int
}

type StatResult struct {
	Exists  bool
	IsDir   bool
	Size    int64
	Mode    uint32
	ModTime time.Time
}

type DirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

type TempFile interface {
	Path() string
	Write([]byte) (int, error)
	Close() error
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

// NopSession returns a no-op session for testing.
func NopSession() Session {
	return &nopSession{
		policy: Policy{
			Filesystem: FilesystemPolicy{
				AllowEscapes: true,
			},
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

// Host file/process no-ops — nopSession is used in tests that don't exercise these paths.
func (s *nopSession) ReadFile(_ context.Context, _ string, _, _ int) (ReadResult, error) {
	return ReadResult{}, nil
}

func (s *nopSession) WriteFile(_ context.Context, _ string, _ []byte) (WriteResult, error) {
	return WriteResult{}, nil
}

func (s *nopSession) EditFile(_ context.Context, _ string, _ []Edit) (EditResult, error) {
	return EditResult{}, nil
}

func (s *nopSession) Stat(_ context.Context, _ string) (StatResult, error) {
	return StatResult{}, nil
}
func (s *nopSession) ListDir(_ context.Context, _ string) ([]DirEntry, error) { return nil, nil }
func (s *nopSession) MkdirAll(_ context.Context, _ string, _ uint32) error    { return nil }
func (s *nopSession) Remove(_ context.Context, _ string, _ bool) error        { return nil }
func (s *nopSession) Rename(_ context.Context, _, _ string) error             { return nil }
func (s *nopSession) CreateTemp(_ context.Context, _, _ string) (TempFile, error) {
	return nil, nil
}

func (s *nopSession) Exec(_ context.Context, _ string, _ ExecOptions) (ExecResult, error) {
	return ExecResult{}, nil
}

func (s *nopSession) StartProcess(_ context.Context, _ ProcessRequest) (ProcessHandle, error) {
	return nil, nil
}
func (s *nopSession) ResolvePath(path string) (string, error) { return path, nil }
func (s *nopSession) WorkingDir() string                      { return "" }
