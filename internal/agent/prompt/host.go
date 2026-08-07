package prompt

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

const promptContextMaxBytes = 256 * 1024

// promptFilesystem returns the injected runner Filesystem. A non-nil session is
// authoritative: failure never falls back to host I/O. Host=nil is a separate
// compatibility API for previews, tests, and operator-local callers; it is never
// an active runner fallback.
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
		r, info, err := filesystem.Read(ctx, path, sandbox.ReadOptions{MaxBytes: promptContextMaxBytes})
		if err != nil || r == nil || info.IsDir || !info.Mode.IsRegular() || info.Size < 0 || info.Size > promptContextMaxBytes {
			if r != nil {
				_ = r.Close()
			}
			return "", false
		}
		// LimitReader independently enforces the contract even if an injected
		// filesystem ignores MaxBytes. A prompt file must be exactly the regular
		// file described by its returned FileInfo.
		content, readErr := io.ReadAll(io.LimitReader(r, promptContextMaxBytes+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || len(content) > promptContextMaxBytes || int64(len(content)) != info.Size {
			return "", false
		}
		return string(content), true
	}
	// Host=nil is a separate compatibility API for previews, tests, and
	// operator-local callers. It is never an active runner fallback.
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
		if host == nil {
			return ""
		}
		projector, ok := host.(sandbox.FilesystemWorkingDirectoryProjector)
		if !ok {
			return ""
		}
		workingDir, projected := projector.FilesystemWorkingDirectory()
		if !projected || !isWorkspacePath(workingDir) {
			return ""
		}
		return workingDir
	}
	// Host=nil is a separate compatibility API for previews, tests, and
	// operator-local callers. It is never an active runner fallback.
	if filepath.IsAbs(projectRoot) {
		return projectRoot
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, projectRoot)
}

func isWorkspacePath(value string) bool {
	return sandbox.IsCanonicalFilesystemPath(value) && (value == sandbox.PathWorkspace || strings.HasPrefix(value, sandbox.PathWorkspace+"/"))
}
