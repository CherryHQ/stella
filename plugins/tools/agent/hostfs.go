package agent

import (
	"context"
	"os"

	"github.com/vaayne/anna/internal/sandbox"
)

type hostPathInfo struct {
	Exists bool
	IsDir  bool
}

func statHostPath(ctx context.Context, host sandbox.Host, path string) (hostPathInfo, error) {
	if host != nil {
		stat, err := host.Stat(ctx, path)
		if err == nil {
			return hostPathInfo{Exists: stat.Exists, IsDir: stat.IsDir}, nil
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return hostPathInfo{}, nil
		}
		return hostPathInfo{}, err
	}
	return hostPathInfo{Exists: true, IsDir: info.IsDir()}, nil
}

func readHostDir(ctx context.Context, host sandbox.Host, path string) ([]sandbox.DirEntry, error) {
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

func readHostFile(ctx context.Context, host sandbox.Host, path string) ([]byte, error) {
	if host != nil {
		result, err := host.ReadFile(ctx, path, 0, 0)
		if err == nil {
			return result.Content, nil
		}
	}
	return os.ReadFile(path)
}
