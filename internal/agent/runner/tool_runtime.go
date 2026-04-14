package runner

import (
	"context"
	"io"

	"github.com/vaayne/anna/internal/sandbox"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func toolRuntimeFromHost(host sandbox.Host) pkgplugins.ToolRuntime {
	if host == nil {
		return nil
	}
	return sandboxToolRuntime{host: host}
}

type sandboxToolRuntime struct {
	host sandbox.Host
}

func (r sandboxToolRuntime) ReadFile(ctx context.Context, path string, offset, limit int) (pkgplugins.ReadFileResult, error) {
	result, err := r.host.ReadFile(ctx, path, offset, limit)
	return pkgplugins.ReadFileResult{Content: result.Content, Truncated: result.Truncated, NextOffset: result.NextOffset}, err
}

func (r sandboxToolRuntime) WriteFile(ctx context.Context, path string, content []byte) (pkgplugins.WriteFileResult, error) {
	result, err := r.host.WriteFile(ctx, path, content)
	return pkgplugins.WriteFileResult{BytesWritten: result.BytesWritten}, err
}

func (r sandboxToolRuntime) Stat(ctx context.Context, path string) (pkgplugins.StatResult, error) {
	result, err := r.host.Stat(ctx, path)
	return pkgplugins.StatResult{
		Exists:  result.Exists,
		IsDir:   result.IsDir,
		Size:    result.Size,
		Mode:    result.Mode,
		ModTime: result.ModTime,
	}, err
}

func (r sandboxToolRuntime) ListDir(ctx context.Context, path string) ([]pkgplugins.DirEntry, error) {
	entries, err := r.host.ListDir(ctx, path)
	if err != nil {
		return nil, err
	}
	out := make([]pkgplugins.DirEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, pkgplugins.DirEntry{Name: entry.Name, IsDir: entry.IsDir, Size: entry.Size})
	}
	return out, nil
}

func (r sandboxToolRuntime) MkdirAll(ctx context.Context, path string, perm uint32) error {
	return r.host.MkdirAll(ctx, path, perm)
}

func (r sandboxToolRuntime) Remove(ctx context.Context, path string, recursive bool) error {
	return r.host.Remove(ctx, path, recursive)
}

func (r sandboxToolRuntime) StartProcess(ctx context.Context, req pkgplugins.ProcessRequest) (pkgplugins.ProcessHandle, error) {
	process, err := r.host.StartProcess(ctx, sandbox.ProcessRequest{
		Path:    req.Path,
		Args:    req.Args,
		Cwd:     req.Cwd,
		Env:     req.Env,
		Timeout: req.Timeout,
	})
	if err != nil {
		return nil, err
	}
	return sandboxProcessHandle{process: process}, nil
}

func (r sandboxToolRuntime) ResolvePath(path string) (string, error) {
	return r.host.ResolvePath(path)
}

func (r sandboxToolRuntime) WorkingDir() string {
	return r.host.WorkingDir()
}

type sandboxProcessHandle struct {
	process sandbox.ProcessHandle
}

func (h sandboxProcessHandle) PID() int { return h.process.PID() }

func (h sandboxProcessHandle) Wait(ctx context.Context) (pkgplugins.ExecResult, error) {
	result, err := h.process.Wait(ctx)
	return pkgplugins.ExecResult{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode}, err
}

func (h sandboxProcessHandle) Stdin() io.WriteCloser { return h.process.Stdin() }
func (h sandboxProcessHandle) Stdout() io.ReadCloser { return h.process.Stdout() }
func (h sandboxProcessHandle) Stderr() io.ReadCloser { return h.process.Stderr() }
func (h sandboxProcessHandle) Close() error          { return h.process.Close() }
