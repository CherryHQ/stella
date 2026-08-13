package prompt

import (
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

func readPromptFile(host sandbox.Host, path string) (string, bool) {
	if host == nil {
		content, err := os.ReadFile(resolvePromptPath(path))
		return string(content), err == nil
	}
	content, err := host.Files().ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(content), true
}

func statPromptFile(host sandbox.Host, path string) (string, bool) {
	if host == nil {
		info, err := os.Stat(resolvePromptPath(path))
		return path, err == nil && !info.IsDir()
	}
	info, err := host.Files().Stat(path)
	if err != nil || info.IsDir {
		return "", false
	}
	return path, true
}

func readPromptDir(host sandbox.Host, path string) ([]sandbox.DirEntry, error) {
	if host != nil {
		return host.Files().ReadDir(path)
	}
	entries, err := os.ReadDir(resolvePromptPath(path))
	if err != nil {
		return nil, err
	}
	result := make([]sandbox.DirEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil {
			result = append(result, sandbox.DirEntry{Name: entry.Name(), IsDir: entry.IsDir(), Size: info.Size()})
		}
	}
	return result, nil
}

// resolvePromptPath anchors the no-Session fallback. Active Sessions always use
// their mediated filesystem capability.
func resolvePromptPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, path)
}
