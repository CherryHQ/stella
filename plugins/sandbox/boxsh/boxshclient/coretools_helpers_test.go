package boxshclient

import (
	"context"
	"path/filepath"
	"strings"
)

func testResolveBackendPath(ctx context.Context, backend *SharedBackend, path string) (string, error) {
	root, err := backend.SandboxRoot(ctx)
	if err != nil {
		return "", err
	}
	backend.mu.RLock()
	srcRoot := backend.sessionSrc
	backend.mu.RUnlock()

	if !filepath.IsAbs(path) {
		return filepath.Join(root, path), nil
	}
	if testIsWithinRoot(root, path) {
		return path, nil
	}
	if srcRoot != "" && testIsWithinRoot(srcRoot, path) {
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return "", err
		}
		return filepath.Join(root, rel), nil
	}
	trimmed := strings.Trim(path, string(filepath.Separator))
	if trimmed == "" || len(strings.Split(trimmed, string(filepath.Separator))) <= 2 {
		return filepath.Join(root, strings.TrimPrefix(path, string(filepath.Separator))), nil
	}
	if err := ValidateSandboxPath(root, path); err != nil {
		return "", err
	}
	return path, nil
}

func testIsWithinRoot(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func testExec(ctx context.Context, backend *SharedBackend, command string, timeout int) (*ExecResult, error) {
	return backend.Client().Exec(ctx, ExecParams{Command: command, Timeout: timeout})
}

func testRead(ctx context.Context, backend *SharedBackend, path string, offset, limit int) (*ReadResult, error) {
	resolved, err := testResolveBackendPath(ctx, backend, path)
	if err != nil {
		return nil, err
	}
	return backend.Client().Read(ctx, ReadParams{FilePath: resolved, Offset: offset, Limit: limit})
}

func testWrite(ctx context.Context, backend *SharedBackend, path, content string) (*WriteResult, error) {
	resolved, err := testResolveBackendPath(ctx, backend, path)
	if err != nil {
		return nil, err
	}
	return backend.Client().Write(ctx, WriteParams{FilePath: resolved, Content: content})
}

func testEdit(ctx context.Context, backend *SharedBackend, path, oldText, newText string) (*EditResult, error) {
	resolved, err := testResolveBackendPath(ctx, backend, path)
	if err != nil {
		return nil, err
	}
	return backend.Client().Edit(ctx, EditParams{FilePath: resolved, Edits: []EditSpec{{OldText: oldText, NewText: newText}}})
}
