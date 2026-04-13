package sandbox

import (
	"context"
	"io"
	"time"
)

// Session is a sandbox-managed execution boundary.
// It provides a constrained Host surface for all tool execution within a session.
//
// Session semantics are shared across all backends:
//   - Single Host per Session. Concurrent tool calls share the same Host.
//   - Cross-call state (cwd, env vars, temp files) is visible within the session lifetime.
//   - Context cancellation propagates to in-flight operations.
//   - Close() guarantees resource cleanup regardless of session state.
//   - Backend failures make the session unusable; Alive() returns false.
type Session interface {
	// Host returns the constrained host surface for this session.
	// All tool execution must use this Host for mediated operations.
	Host() Host

	// Policy returns the session's effective policy.
	Policy() Policy

	// Close shuts down the session and cleans up resources.
	// Guarantees cleanup of all session resources.
	// Safe to call multiple times; returns error on first failure.
	Close() error

	// Alive reports whether the session is healthy and usable.
	// Returns false after the backend process fails or Close() is called.
	Alive() bool

	// Done returns a channel that closes when the session terminates.
	// This can be used to detect session liveness loss.
	Done() <-chan struct{}
}

// Host provides mediated access to host resources within a sandbox session.
// All local tool execution must use Host methods instead of direct os/exec/net/http calls.
type Host interface {
	// ==================== Filesystem Operations ====================

	// ReadFile reads file content with optional offset/limit for pagination.
	// Returns truncated flag if limit was reached before EOF.
	ReadFile(ctx context.Context, path string, offset, limit int) (ReadResult, error)

	// WriteFile writes content to a file, creating or truncating as needed.
	WriteFile(ctx context.Context, path string, content []byte) (WriteResult, error)

	// EditFile applies text edits to a file atomically.
	EditFile(ctx context.Context, path string, edits []Edit) (EditResult, error)

	// Stat returns file metadata without reading content.
	Stat(ctx context.Context, path string) (StatResult, error)

	// ListDir returns directory entries (not recursive).
	ListDir(ctx context.Context, path string) ([]DirEntry, error)

	// MkdirAll creates a directory and all necessary parents.
	MkdirAll(ctx context.Context, path string, perm uint32) error

	// Remove removes a file or directory. If recursive is true, removes directories recursively.
	Remove(ctx context.Context, path string, recursive bool) error

	// Rename moves a file or directory from oldPath to newPath.
	Rename(ctx context.Context, oldPath, newPath string) error

	// CreateTemp creates a temporary file in the specified directory.
	// The file is owned by the session and should be cleaned up on session close.
	CreateTemp(ctx context.Context, dir, pattern string) (TempFile, error)

	// ==================== Process Execution ====================

	// Exec executes a shell command with the given options.
	// This provides bash-like execution suitable for core tool parity.
	Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error)

	// StartProcess spawns a process with argv-oriented arguments.
	// This is designed for stdio transports such as local MCP servers.
	// The returned ProcessHandle must be closed when done.
	StartProcess(ctx context.Context, req ProcessRequest) (ProcessHandle, error)

	// ==================== Network Operations ====================

	// HTTPRequest performs an HTTP request/response roundtrip.
	HTTPRequest(ctx context.Context, opts HTTPOptions) (HTTPResult, error)

	// OpenHTTPStream opens a streaming HTTP connection for SSE/StreamableHTTP.
	// The returned HTTPStream must be closed when done.
	OpenHTTPStream(ctx context.Context, opts HTTPOptions) (HTTPStream, error)

	// ==================== Path Operations ====================

	// ResolvePath converts a possibly relative path to an absolute path
	// within the session's filesystem constraints.
	ResolvePath(path string) (string, error)

	// WorkingDir returns the session's working directory.
	WorkingDir() string
}

// ==================== Request/Result Types ====================

// ReadResult is returned by Host.ReadFile.
type ReadResult struct {
	Content    []byte
	Truncated  bool
	NextOffset int
}

// WriteResult is returned by Host.WriteFile.
type WriteResult struct {
	BytesWritten int
}

// Edit describes a text replacement operation.
type Edit struct {
	OldText string
	NewText string
}

// EditResult is returned by Host.EditFile.
type EditResult struct {
	AppliedEdits int
}

// StatResult is returned by Host.Stat.
type StatResult struct {
	Exists  bool
	IsDir   bool
	Size    int64
	Mode    uint32
	ModTime time.Time
}

// DirEntry represents a single directory entry.
type DirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

// TempFile represents a temporary file created by Host.CreateTemp.
type TempFile interface {
	// Path returns the absolute path to the temp file.
	Path() string

	// Write writes data to the temp file.
	Write([]byte) (int, error)

	// Close closes and optionally cleans up the temp file.
	// Implementations may remove the file on close.
	Close() error
}

// ExecOptions configures shell command execution.
type ExecOptions struct {
	// Cwd sets the working directory for execution. Empty uses session working dir.
	Cwd string

	// Env sets environment variables for the process. These override session env.
	Env map[string]string

	// Timeout for execution. Zero means use session policy timeout.
	Timeout time.Duration
}

// ExecResult is returned by Host.Exec.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ProcessRequest configures argv-oriented process spawning.
type ProcessRequest struct {
	// Path is the executable path.
	Path string

	// Args are the command arguments (not including argv[0]).
	Args []string

	// Cwd sets the working directory. Empty uses session working dir.
	Cwd string

	// Env sets environment variables. These override session env.
	Env map[string]string

	// Timeout for process execution. Zero means use session policy timeout.
	Timeout time.Duration
}

// ProcessHandle provides access to a spawned process.
type ProcessHandle interface {
	// PID returns the process ID (may be 0 if unknown).
	PID() int

	// Wait blocks until the process exits and returns the result.
	Wait(ctx context.Context) (ExecResult, error)

	// Stdin returns the process stdin writer.
	Stdin() io.WriteCloser

	// Stdout returns the process stdout reader.
	Stdout() io.ReadCloser

	// Stderr returns the process stderr reader.
	Stderr() io.ReadCloser

	// Close terminates the process and releases resources.
	Close() error
}

// HTTPOptions configures HTTP requests.
type HTTPOptions struct {
	// Method is the HTTP method (GET, POST, etc.).
	Method string

	// URL is the request URL.
	URL string

	// Header contains request headers.
	Header map[string]string

	// Body is the request body for POST/PUT/PATCH.
	Body []byte

	// Timeout for the request. Zero means use session policy timeout.
	Timeout time.Duration
}

// HTTPResult is returned by Host.HTTPRequest.
type HTTPResult struct {
	StatusCode int
	Header     map[string][]string
	Body       []byte
}

// HTTPStream provides streaming HTTP access for SSE and StreamableHTTP.
type HTTPStream interface {
	// Header returns response headers (available after headers received).
	Header() map[string][]string

	// Reader returns the body reader for streaming content.
	Reader() io.ReadCloser

	// Close terminates the stream and releases resources.
	Close() error
}

// ==================== Session Lifecycle Helpers ====================

// NopSession returns a no-op session for testing.
// It provides a minimal implementation that does not enforce any constraints.
func NopSession() Session {
	return &nopSession{
		policy: Policy{
			Relaxed: true,
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
