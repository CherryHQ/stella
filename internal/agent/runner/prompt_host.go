package runner

import (
	"context"
	"os"

	"github.com/vaayne/anna/internal/sandbox"
)

func readPromptFile(ctx context.Context, host sandbox.Host, path string) (string, bool) {
	if host != nil {
		result, err := host.ReadFile(ctx, path, 0, 0)
		if err != nil {
			return "", false
		}
		return string(result.Content), true
	}
	// Fallback: plain OS read (non-runner prompt rendering path).
	content, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(content), true
}

func statPromptFile(ctx context.Context, host sandbox.Host, path string) (string, bool) {
	if host != nil {
		stat, err := host.Stat(ctx, path)
		if err != nil || !stat.Exists || stat.IsDir {
			return "", false
		}
		return path, true
	}
	// Fallback: plain OS stat (non-runner prompt rendering path).
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

func readPromptDir(ctx context.Context, host sandbox.Host, path string) ([]sandbox.DirEntry, error) {
	if host != nil {
		return host.ListDir(ctx, path)
	}
	// Fallback: plain OS readdir (non-runner prompt rendering path).
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]sandbox.DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, sandbox.DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  info.Size(),
		})
	}
	return result, nil
}
