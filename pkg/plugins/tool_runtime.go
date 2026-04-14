package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ToolRuntime is the host runtime surface exposed to tools.
// Implementations decide whether access is local, sandboxed, or remote.
type ToolRuntime interface {
	ReadFile(ctx context.Context, path string, offset, limit int) (ReadFileResult, error)
	WriteFile(ctx context.Context, path string, content []byte) (WriteFileResult, error)
	Stat(ctx context.Context, path string) (StatResult, error)
	ListDir(ctx context.Context, path string) ([]DirEntry, error)
	MkdirAll(ctx context.Context, path string, perm uint32) error
	Remove(ctx context.Context, path string, recursive bool) error
	StartProcess(ctx context.Context, req ProcessRequest) (ProcessHandle, error)
	ResolvePath(path string) (string, error)
	WorkingDir() string
}

type ReadFileResult struct {
	Content    []byte
	Truncated  bool
	NextOffset int
}

type WriteFileResult struct {
	BytesWritten int
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

type ProcessRequest struct {
	Path    string
	Args    []string
	Cwd     string
	Env     map[string]string
	Timeout time.Duration
}

type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type ProcessHandle interface {
	PID() int
	Wait(ctx context.Context) (ExecResult, error)
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Close() error
}

// NewLocalToolRuntime returns an unrestricted local runtime for CLI and tests.
func NewLocalToolRuntime(workingDir string) ToolRuntime {
	if workingDir == "" {
		workingDir = "."
	}
	return localToolRuntime{workingDir: workingDir}
}

type localToolRuntime struct {
	workingDir string
}

func (r localToolRuntime) ReadFile(_ context.Context, path string, offset, limit int) (ReadFileResult, error) {
	resolved, err := r.ResolvePath(path)
	if err != nil {
		return ReadFileResult{}, err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return ReadFileResult{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(data) {
		return ReadFileResult{Content: []byte{}, NextOffset: len(data)}, nil
	}
	end := len(data)
	truncated := false
	if limit > 0 && offset+limit < end {
		end = offset + limit
		truncated = true
	}
	return ReadFileResult{Content: data[offset:end], Truncated: truncated, NextOffset: end}, nil
}

func (r localToolRuntime) WriteFile(_ context.Context, path string, content []byte) (WriteFileResult, error) {
	resolved, err := r.ResolvePath(path)
	if err != nil {
		return WriteFileResult{}, err
	}
	if err := os.WriteFile(resolved, content, 0o644); err != nil {
		return WriteFileResult{}, err
	}
	return WriteFileResult{BytesWritten: len(content)}, nil
}

func (r localToolRuntime) Stat(_ context.Context, path string) (StatResult, error) {
	resolved, err := r.ResolvePath(path)
	if err != nil {
		return StatResult{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StatResult{Exists: false}, nil
		}
		return StatResult{}, err
	}
	return StatResult{
		Exists:  true,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		Mode:    uint32(info.Mode()),
		ModTime: info.ModTime(),
	}, nil
}

func (r localToolRuntime) ListDir(_ context.Context, path string) ([]DirEntry, error) {
	resolved, err := r.ResolvePath(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		item := DirEntry{Name: entry.Name(), IsDir: entry.IsDir()}
		if info, err := entry.Info(); err == nil {
			item.Size = info.Size()
		}
		out = append(out, item)
	}
	return out, nil
}

func (r localToolRuntime) MkdirAll(_ context.Context, path string, perm uint32) error {
	resolved, err := r.ResolvePath(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(resolved, os.FileMode(perm))
}

func (r localToolRuntime) Remove(_ context.Context, path string, recursive bool) error {
	resolved, err := r.ResolvePath(path)
	if err != nil {
		return err
	}
	if recursive {
		return os.RemoveAll(resolved)
	}
	return os.Remove(resolved)
}

func (r localToolRuntime) StartProcess(ctx context.Context, req ProcessRequest) (ProcessHandle, error) {
	cwd := req.Cwd
	if cwd == "" {
		cwd = r.workingDir
	}
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	cmd := exec.CommandContext(ctx, req.Path, req.Args...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	return &localProcessHandle{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr, cancel: cancel}, nil
}

func (r localToolRuntime) ResolvePath(path string) (string, error) {
	if path == "" {
		path = "."
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Abs(filepath.Join(r.workingDir, path))
}

func (r localToolRuntime) WorkingDir() string { return r.workingDir }

type localProcessHandle struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	cancel context.CancelFunc
}

func (h *localProcessHandle) PID() int {
	if h == nil || h.cmd == nil || h.cmd.Process == nil {
		return 0
	}
	return h.cmd.Process.Pid
}

func (h *localProcessHandle) Wait(ctx context.Context) (ExecResult, error) {
	if h == nil || h.cmd == nil {
		return ExecResult{}, nil
	}
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		if h.stdout != nil {
			_, _ = io.Copy(&stdout, h.stdout)
		}
		if h.stderr != nil {
			_, _ = io.Copy(&stderr, h.stderr)
		}
		done <- h.cmd.Wait()
	}()
	select {
	case <-ctx.Done():
		h.cancel()
		return ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}, ctx.Err()
	case err := <-done:
		exitCode := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
			err = nil
		}
		return ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, err
	}
}

func (h *localProcessHandle) Stdin() io.WriteCloser { return h.stdin }
func (h *localProcessHandle) Stdout() io.ReadCloser { return h.stdout }
func (h *localProcessHandle) Stderr() io.ReadCloser { return h.stderr }
func (h *localProcessHandle) Close() error {
	if h == nil {
		return nil
	}
	h.cancel()
	if h.cmd != nil && h.cmd.Process != nil {
		return h.cmd.Process.Kill()
	}
	return nil
}
