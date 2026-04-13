package agent

import (
	"context"
	"path/filepath"

	"github.com/vaayne/anna/internal/sandbox"
)

type hostPathInfo struct {
	Exists bool
	IsDir  bool
}

func resolvePresetHost(ctx context.Context, host sandbox.Host, path string) (sandbox.Host, func(), error) {
	if host != nil {
		return host, func() {}, nil
	}
	workingDir := path
	if workingDir == "" {
		workingDir = "."
	}
	if ext := filepath.Ext(workingDir); ext != "" {
		workingDir = filepath.Dir(workingDir)
	}
	session, err := sandbox.GlobalRegistry().CreateRelaxedSession(ctx, sandbox.Policy{
		Backend: "local",
		Filesystem: sandbox.FilesystemPolicy{
			WorkingDir:   workingDir,
			AllowEscapes: true,
		},
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkAllowAll},
	})
	if err != nil {
		return nil, func() {}, err
	}
	return session.Host(), func() { _ = session.Close() }, nil
}

func statHostPath(ctx context.Context, host sandbox.Host, path string) (hostPathInfo, error) {
	host, closeHost, err := resolvePresetHost(ctx, host, path)
	if err != nil {
		return hostPathInfo{}, err
	}
	defer closeHost()
	stat, err := host.Stat(ctx, path)
	if err != nil {
		return hostPathInfo{}, err
	}
	return hostPathInfo{Exists: stat.Exists, IsDir: stat.IsDir}, nil
}

func readHostDir(ctx context.Context, host sandbox.Host, path string) ([]sandbox.DirEntry, error) {
	host, closeHost, err := resolvePresetHost(ctx, host, path)
	if err != nil {
		return nil, err
	}
	defer closeHost()
	return host.ListDir(ctx, path)
}

func readHostFile(ctx context.Context, host sandbox.Host, path string) ([]byte, error) {
	host, closeHost, err := resolvePresetHost(ctx, host, path)
	if err != nil {
		return nil, err
	}
	defer closeHost()
	result, err := host.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return nil, err
	}
	return result.Content, nil
}
