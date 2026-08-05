package prompt

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

const promptContextMaxBytes = 256 * 1024

// promptFilesystem returns the injected runner Filesystem. A non-nil session is
// authoritative: failure never falls back to host I/O. Prompt rendering outside
// an active runner has no host and retains its temporary host-only fallback.
func promptFilesystem(host sandbox.Host) (sandbox.Filesystem, func(), bool) {
	if host == nil {
		return nil, func() {}, false
	}
	withFS, ok := host.(sandbox.FilesystemSession)
	if !ok {
		return nil, func() {}, true
	}
	filesystem, err := withFS.Filesystem()
	// An injected session is authoritative: an error, or a nil Filesystem with a
	// nil error, never falls back to host I/O. Returning injected=true with a nil
	// Filesystem makes the caller skip context loading rather than dereference it.
	if err != nil || filesystem == nil {
		return nil, func() {}, true
	}
	return filesystem, func() { _ = filesystem.Close() }, true
}

func readPromptFile(ctx context.Context, filesystem sandbox.Filesystem, path string) (string, bool) {
	if filesystem != nil {
		r, _, err := filesystem.Read(ctx, path, sandbox.ReadOptions{MaxBytes: promptContextMaxBytes})
		if err != nil {
			return "", false
		}
		defer func() { _ = r.Close() }()
		content, err := io.ReadAll(r)
		if err != nil {
			return "", false
		}
		return string(content), true
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(content), true
}

func statPromptFile(ctx context.Context, filesystem sandbox.Filesystem, path string) (string, bool) {
	if filesystem != nil {
		info, err := filesystem.Stat(ctx, path)
		if err != nil || info.IsDir {
			return "", false
		}
		return path, true
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

func readPromptDir(ctx context.Context, filesystem sandbox.Filesystem, path string) ([]sandbox.DirEntry, error) {
	if filesystem != nil {
		return filesystem.List(ctx, path)
	}
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
		result = append(result, sandbox.DirEntry{Name: e.Name(), IsDir: e.IsDir(), Size: info.Size(), Mode: info.Mode()})
	}
	return result, nil
}

func promptPath(host sandbox.Host, projectRoot string, filesystem sandbox.Filesystem) string {
	if filesystem != nil {
		return host.WorkingDir()
	}
	if filepath.IsAbs(projectRoot) {
		return projectRoot
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, projectRoot)
}
