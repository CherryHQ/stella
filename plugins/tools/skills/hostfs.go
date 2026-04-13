package skills

import (
	"context"
	"path/filepath"

	"github.com/vaayne/anna/internal/sandbox"
)

type skillPathInfo struct {
	Exists bool
	IsDir  bool
}

func resolveSkillHost(ctx context.Context, host sandbox.Host, path string) (sandbox.Host, func(), error) {
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

func statSkillPath(ctx context.Context, host sandbox.Host, path string) (skillPathInfo, error) {
	host, closeHost, err := resolveSkillHost(ctx, host, path)
	if err != nil {
		return skillPathInfo{}, err
	}
	defer closeHost()
	stat, err := host.Stat(ctx, path)
	if err != nil {
		return skillPathInfo{}, err
	}
	return skillPathInfo{Exists: stat.Exists, IsDir: stat.IsDir}, nil
}

func entryIsRegular(ctx context.Context, host sandbox.Host, path string) bool {
	info, err := statSkillPath(ctx, host, path)
	return err == nil && info.Exists && !info.IsDir
}

func readSkillDir(ctx context.Context, host sandbox.Host, path string) ([]sandbox.DirEntry, error) {
	host, closeHost, err := resolveSkillHost(ctx, host, path)
	if err != nil {
		return nil, err
	}
	defer closeHost()
	return host.ListDir(ctx, path)
}

func readSkillFile(ctx context.Context, host sandbox.Host, path string) ([]byte, error) {
	host, closeHost, err := resolveSkillHost(ctx, host, path)
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

func writeSkillFile(ctx context.Context, host sandbox.Host, path string, data []byte, _ uint32) error {
	host, closeHost, err := resolveSkillHost(ctx, host, path)
	if err != nil {
		return err
	}
	defer closeHost()
	if err := host.MkdirAll(ctx, filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err = host.WriteFile(ctx, path, data)
	return err
}
