package sandbox

import (
	"context"
	"io"
	"time"
)

// Session is a sandbox-managed execution boundary.
type Session interface {
	Host() Host
	Policy() Policy
	Close() error
	Alive() bool
	Done() <-chan struct{}
}

// Host provides mediated access to host resources within a sandbox session.
type Host interface {
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
	HTTPRequest(ctx context.Context, opts HTTPOptions) (HTTPResult, error)
	OpenHTTPStream(ctx context.Context, opts HTTPOptions) (HTTPStream, error)
	ResolvePath(path string) (string, error)
	WorkingDir() string
}

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

type HTTPOptions struct {
	Method  string
	URL     string
	Header  map[string]string
	Body    []byte
	Timeout time.Duration
}

type HTTPResult struct {
	StatusCode int
	Header     map[string][]string
	Body       []byte
}

type HTTPStream interface {
	Header() map[string][]string
	Reader() io.ReadCloser
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

func (s *nopSession) Host() Host            { return nil }
func (s *nopSession) Policy() Policy        { return s.policy }
func (s *nopSession) Alive() bool           { return !s.closed }
func (s *nopSession) Done() <-chan struct{} { return s.done }
func (s *nopSession) Close() error {
	s.closed = true
	close(s.done)
	return nil
}
