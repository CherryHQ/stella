package prompt

import (
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

func readPromptFile(host sandbox.Host, path string) (string, bool) {
	resolved := resolvePromptPath(host, path)
	content, err := os.ReadFile(resolved)
	if err != nil {
		return "", false
	}
	return string(content), true
}

func statPromptFile(host sandbox.Host, path string) (string, bool) {
	resolved := resolvePromptPath(host, path)
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

func readPromptDir(host sandbox.Host, path string) ([]sandbox.DirEntry, error) {
	resolved := resolvePromptPath(host, path)
	entries, err := os.ReadDir(resolved)
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

// resolvePromptPath resolves a path through the session (if present) or falls
// back to the raw path. Relative paths without a session are joined against the
// working directory.
func resolvePromptPath(host sandbox.Host, path string) string {
	if host != nil {
		resolved, err := host.ResolvePath(path)
		if err == nil {
			return resolved
		}
	}
	if filepath.IsAbs(path) {
		return path
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, path)
}
