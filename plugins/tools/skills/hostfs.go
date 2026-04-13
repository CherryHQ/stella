package skills

import (
	"context"
	"os"
	"path/filepath"

	"github.com/vaayne/anna/internal/sandbox"
)

type skillPathInfo struct {
	Exists bool
	IsDir  bool
}

func statSkillPath(ctx context.Context, host sandbox.Host, path string) (skillPathInfo, error) {
	if host != nil {
		stat, err := host.Stat(ctx, path)
		if err == nil {
			return skillPathInfo{Exists: stat.Exists, IsDir: stat.IsDir}, nil
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return skillPathInfo{}, nil
		}
		return skillPathInfo{}, err
	}
	return skillPathInfo{Exists: true, IsDir: info.IsDir()}, nil
}

func entryIsRegular(ctx context.Context, host sandbox.Host, path string) bool {
	info, err := statSkillPath(ctx, host, path)
	return err == nil && info.Exists && !info.IsDir
}

func readSkillDir(ctx context.Context, host sandbox.Host, path string) ([]sandbox.DirEntry, error) {
	if host != nil {
		entries, err := host.ListDir(ctx, path)
		if err == nil {
			return entries, nil
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]sandbox.DirEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, sandbox.DirEntry{Name: entry.Name(), IsDir: entry.IsDir()})
	}
	return result, nil
}

func readSkillFile(ctx context.Context, host sandbox.Host, path string) ([]byte, error) {
	if host != nil {
		result, err := host.ReadFile(ctx, path, 0, 0)
		if err == nil {
			return result.Content, nil
		}
	}
	return os.ReadFile(path)
}

func writeSkillFile(ctx context.Context, host sandbox.Host, path string, data []byte, perm os.FileMode) error {
	if host == nil {
		return atomicWriteFile(path, data, perm)
	}
	if err := host.MkdirAll(ctx, filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := host.WriteFile(ctx, path, data)
	return err
}
