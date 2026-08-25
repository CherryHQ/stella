package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// FileView binds process-visible metadata and filesystem access to one selected
// Session generation. It does not keep that generation alive; if the backend
// dies during an operation, callers get an error instead of silently continuing
// the same composed operation against a replacement generation.
type FileView struct {
	Policy     Policy
	WorkingDir string
	Files      FileAccess
}

// FileViewSelector is the context-aware filesystem capability exposed by a
// Session wrapper that may need to recreate its current generation.
type FileViewSelector interface {
	SelectFileView(context.Context) (FileView, error)
}

// SelectFileView chooses one operational Session generation. Resilient sessions
// recreate a dead backend before taking the snapshot; raw sessions expose their
// current immutable metadata and filesystem capability directly.
func SelectFileView(ctx context.Context, session Session) (FileView, error) {
	if session == nil {
		return FileView{}, errors.New("sandbox: active session is required")
	}
	if selector, ok := session.(FileViewSelector); ok {
		return selector.SelectFileView(ctx)
	}
	return fileView(session), nil
}

func fileView(session Session) FileView {
	return FileView{
		Policy:     session.Policy(),
		WorkingDir: session.WorkingDir(),
		Files:      session.Files(),
	}
}

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

// FileTooLargeError reports that a backend refused to transfer a complete file.
// It deliberately carries no path so provider-internal filesystem coordinates
// cannot cross the sandbox boundary; the caller already has the public path.
type FileTooLargeError struct {
	Size  int64
	Limit int64
}

func (e *FileTooLargeError) Error() string {
	return fmt.Sprintf("sandbox: file is %d bytes, over the %d-byte transfer limit", e.Size, e.Limit)
}

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
	// ProjectTempFiles publishes an exact tree beneath the active Session's
	// process-visible temporary root and returns that visible absolute path.
	// Choosing the generation, temporary root, and FileAccess capability is one
	// operation so resilient recreation cannot combine coordinates from one
	// generation with files from another.
	ProjectTempFiles(path string, files []ProjectedFile) (string, error)
}

// NopSession returns a no-op session for tests that need only lifecycle or
// process behavior. Its filesystem capability fails closed; filesystem tests
// must use a real provider session or inject an explicit FileAccess fake.
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
func (s *nopSession) Files() FileAccess  { return deniedFileAccess{} }
func (s *nopSession) WorkingDir() string { return "" }

type deniedFileAccess struct{}

func (deniedFileAccess) ReadFile(string) ([]byte, error)             { return nil, fs.ErrPermission }
func (deniedFileAccess) ReadDir(string) ([]DirEntry, error)          { return nil, fs.ErrPermission }
func (deniedFileAccess) Stat(string) (FileInfo, error)               { return FileInfo{}, fs.ErrPermission }
func (deniedFileAccess) WriteFile(string, []byte, fs.FileMode) error { return fs.ErrPermission }
func (deniedFileAccess) ProjectFiles(string, []ProjectedFile) error  { return fs.ErrPermission }
func (deniedFileAccess) ProjectTempFiles(string, []ProjectedFile) (string, error) {
	return "", fs.ErrPermission
}
