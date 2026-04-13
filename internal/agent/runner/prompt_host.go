package runner

import (
	"context"
	"os"

	"github.com/vaayne/anna/internal/sandbox"
)

func readPromptFile(ctx context.Context, host sandbox.Host, path string) (string, bool) {
	if host != nil {
		result, err := host.ReadFile(ctx, path, 0, 0)
		if err == nil {
			return string(result.Content), true
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func statPromptFile(ctx context.Context, host sandbox.Host, path string) (string, bool) {
	if host != nil {
		stat, err := host.Stat(ctx, path)
		if err == nil && stat.Exists && !stat.IsDir {
			return path, true
		}
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, true
	}
	return "", false
}

func readPromptDir(ctx context.Context, host sandbox.Host, path string) ([]sandbox.DirEntry, error) {
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
