package runner

import (
	"context"
	"errors"

	"github.com/vaayne/anna/internal/sandbox"
)

func readPromptFile(ctx context.Context, host sandbox.Host, path string) (string, bool) {
	if host == nil {
		return "", false
	}
	result, err := host.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return "", false
	}
	return string(result.Content), true
}

func statPromptFile(ctx context.Context, host sandbox.Host, path string) (string, bool) {
	if host == nil {
		return "", false
	}
	stat, err := host.Stat(ctx, path)
	if err != nil || !stat.Exists || stat.IsDir {
		return "", false
	}
	return path, true
}

func readPromptDir(ctx context.Context, host sandbox.Host, path string) ([]sandbox.DirEntry, error) {
	if host == nil {
		return nil, errors.New("prompt context requires sandbox host")
	}
	return host.ListDir(ctx, path)
}
