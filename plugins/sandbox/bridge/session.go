package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"strings"
	"sync"
	"time"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// Config configures the bridge factory.
type Config struct {
	// BindingDir is where the harness publishes <user_id>.json bindings.
	BindingDir string
	// UserID is the session principal whose binding selects the bridge.
	UserID string
	// GroupID is set for group sessions, which the eval backend refuses.
	GroupID string
}

// Factory creates sessions bound to one harness bridge.
type Factory struct {
	cfg Config
}

// NewFactory returns a bridge factory. Mount sources are ignored: the process
// view is the task container's own filesystem, not a projection of host paths.
func NewFactory(cfg Config) sandboxpkg.Factory { return &Factory{cfg: cfg} }

func (f *Factory) Name() string    { return "bridge" }
func (f *Factory) Available() bool { return true }

// Supported accepts any policy; confinement is the task container itself.
func (f *Factory) Supported(sandboxpkg.Policy) error { return nil }

// CreateSession loads the principal's binding, verifies the bridge answers with
// the same nonce, and rewrites the policy to the container's coordinates.
func (f *Factory) CreateSession(ctx context.Context, policy sandboxpkg.Policy) (sandboxpkg.Session, error) {
	if f.cfg.GroupID != "" {
		return nil, errors.New("bridge: group sessions are not supported by the eval backend")
	}
	binding, err := LoadBinding(f.cfg.BindingDir, f.cfg.UserID)
	if err != nil {
		return nil, err
	}
	c := &client{socket: binding.Socket, nonce: binding.Nonce}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := c.call(pingCtx, request{Op: "ping"}); err != nil {
		return nil, fmt.Errorf("bridge: bind check failed: %w", err)
	}

	home := binding.Home
	if home == "" {
		home = binding.WorkDir
	}
	tempDir := binding.TempDir
	if tempDir == "" {
		tempDir = "/tmp"
	}
	env := maps.Clone(policy.Env)
	if env == nil {
		env = map[string]string{}
	}
	if err := sandboxpkg.ApplyFilesystemEnv(env, sandboxpkg.FilesystemView{Home: home, TempDir: tempDir}); err != nil {
		return nil, err
	}
	if binding.Path != "" {
		env["PATH"] = binding.Path
		env[sandboxpkg.EnvRunnerPath] = binding.Path
	}
	policy.Env = env
	// The container is the confinement boundary; every absolute container path
	// is in scope. Mounts describe the data roots the runner may address.
	policy.Filesystem = sandboxpkg.FilesystemPolicy{
		WorkingDir: binding.WorkDir,
		Mounts:     []sandboxpkg.Mount{{SandboxPath: "/", Access: sandboxpkg.MountReadWrite}},
	}
	policy.Network.Mode = sandboxpkg.NetworkAllowAll

	var entropy [6]byte
	_, _ = rand.Read(entropy[:])
	s := &session{
		id:      "bridge-" + hex.EncodeToString(entropy[:]),
		client:  c,
		policy:  policy,
		tempDir: tempDir,
		done:    make(chan struct{}),
	}
	s.files = &fileAccess{s: s}
	sandboxpkg.LogSessionCreated(s.id, "bridge", policy)
	return s, nil
}

type session struct {
	id      string
	client  *client
	tempDir string
	done    chan struct{}
	files   *fileAccess

	mu     sync.RWMutex
	policy sandboxpkg.Policy
	closed bool
}

func (s *session) Policy() sandboxpkg.Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

func (s *session) WorkingDir() string           { return s.Policy().Filesystem.WorkingDir }
func (s *session) Files() sandboxpkg.FileAccess { return s.files }
func (s *session) Done() <-chan struct{}        { return s.done }

func (s *session) Alive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.closed
}

func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.done)
	sandboxpkg.LogSessionClosed(s.id, "bridge", "explicit_close")
	return nil
}

// RefreshEnv implements sandboxpkg.EnvRefresher so OAuth-derived variables can
// rotate mid-session exactly as on the other backends.
func (s *session) RefreshEnv(updates map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	env := maps.Clone(s.policy.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, updates)
	s.policy.Env = env
}

func (s *session) checkOpen() error {
	if !s.Alive() {
		return errors.New("bridge: session is closed")
	}
	return nil
}

func (s *session) Exec(ctx context.Context, command string, opts sandboxpkg.ExecOptions) (sandboxpkg.ExecResult, error) {
	if err := s.checkOpen(); err != nil {
		return sandboxpkg.ExecResult{}, err
	}
	policy := s.Policy()
	env := maps.Clone(policy.Env)
	maps.Copy(env, opts.Env)
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = policy.Timeout
	}
	cwd := opts.Cwd
	if cwd == "" {
		cwd = policy.Filesystem.WorkingDir
	}
	resp, err := s.client.call(ctx, request{
		Op: "exec", Command: command, Cwd: cwd, Env: env,
		TimeoutSec: int(timeout / time.Second),
	})
	if err != nil {
		return sandboxpkg.ExecResult{}, err
	}
	return sandboxpkg.ExecResult{
		Stdout: resp.Stdout, Stderr: resp.Stderr, ExitCode: resp.ReturnCode, TimedOut: resp.TimedOut,
	}, nil
}

// StartProcess is not supported by the first bridge version: no core tool needs
// a long-lived process, and Harbor's environment exec is request/response.
// Ceiling: implement over a streaming transport if eval tasks require it.
func (s *session) StartProcess(context.Context, sandboxpkg.ProcessRequest) (sandboxpkg.ProcessHandle, error) {
	return nil, errors.New("bridge: StartProcess is not supported by the eval backend")
}

// fileAccess forwards every file operation to the bridge. Paths are container
// paths; relative names resolve against the session working directory.
type fileAccess struct{ s *session }

func (f *fileAccess) resolve(name string) string {
	if path.IsAbs(name) {
		return path.Clean(name)
	}
	return path.Join(f.s.WorkingDir(), name)
}

func (f *fileAccess) call(op string, req request) (response, error) {
	if err := f.s.checkOpen(); err != nil {
		return response{}, err
	}
	req.Op = op
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	resp, err := f.s.client.call(ctx, req)
	if err != nil {
		return resp, mapFileCallError(resp, req, err)
	}
	return resp, nil
}

func mapFileCallError(resp response, req request, err error) error {
	switch resp.Code {
	case codeNotFound:
		return &fs.PathError{Op: req.Op, Path: req.Path, Err: fs.ErrNotExist}
	case codeIsDir:
		return &fs.PathError{Op: req.Op, Path: req.Path, Err: errors.New("is a directory")}
	case codeConflict:
		return fmt.Errorf("%w: %s", sandboxpkg.ErrProjectionConflict, resp.Error)
	case codeTooLarge:
		if resp.Size > 0 && resp.Limit > 0 {
			return &sandboxpkg.FileTooLargeError{Size: resp.Size, Limit: resp.Limit}
		}
	}
	return err
}

func (f *fileAccess) ReadFile(name string) ([]byte, error) {
	resp, err := f.call("read_file", request{Path: f.resolve(name)})
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (f *fileAccess) ReadDir(name string) ([]sandboxpkg.DirEntry, error) {
	resp, err := f.call("read_dir", request{Path: f.resolve(name)})
	if err != nil {
		return nil, err
	}
	out := make([]sandboxpkg.DirEntry, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		out = append(out, sandboxpkg.DirEntry{Name: e.Name, IsDir: e.IsDir, Size: e.Size})
	}
	return out, nil
}

func (f *fileAccess) Stat(name string) (sandboxpkg.FileInfo, error) {
	resp, err := f.call("stat", request{Path: f.resolve(name)})
	if err != nil {
		return sandboxpkg.FileInfo{}, err
	}
	return sandboxpkg.FileInfo{IsDir: resp.IsDir, Size: resp.Size}, nil
}

func (f *fileAccess) WriteFile(name string, content []byte, mode fs.FileMode) error {
	_, err := f.call("write_file", request{Path: f.resolve(name), Data: content, Mode: uint32(mode.Perm())})
	return err
}

func (f *fileAccess) ProjectFiles(name string, files []sandboxpkg.ProjectedFile) error {
	return f.project(f.resolve(name), files)
}

func (f *fileAccess) ProjectTempFiles(name string, files []sandboxpkg.ProjectedFile) (string, error) {
	if path.IsAbs(name) || !fs.ValidPath(name) || name == "." {
		return "", fmt.Errorf("bridge: invalid temp projection name %q", name)
	}
	target := path.Join(f.s.tempDir, name)
	if err := f.project(target, files); err != nil {
		return "", err
	}
	return target, nil
}

func (f *fileAccess) project(target string, files []sandboxpkg.ProjectedFile) error {
	req := request{Path: target}
	for _, file := range files {
		if !fs.ValidPath(file.Path) || file.Path == "." || strings.HasPrefix(file.Path, "/") {
			return fmt.Errorf("bridge: invalid projection file %q", file.Path)
		}
		req.Files = append(req.Files, projFile{Path: file.Path, Data: file.Content, Mode: uint32(file.Mode.Perm())})
	}
	_, err := f.call("project", req)
	return err
}
