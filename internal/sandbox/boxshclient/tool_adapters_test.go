package boxshclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBashAdapter_Definition(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewBashAdapter(backend, "")
	def := adapter.Definition()
	if def.Name != "bash" {
		t.Errorf("expected name 'bash', got %q", def.Name)
	}
	if def.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestBashAdapter_Execute_MissingCommand(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewBashAdapter(backend, "")
	_, err := adapter.Execute(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Errorf("expected 'command is required' error, got: %v", err)
	}
}

func TestReadAdapter_Definition(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewReadAdapter(backend)
	if def := adapter.Definition(); def.Name != "read" {
		t.Errorf("expected name 'read', got %q", def.Name)
	}
}

func TestReadAdapter_Execute_MissingPath(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewReadAdapter(backend)
	_, err := adapter.Execute(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "file_path is required") {
		t.Errorf("expected 'file_path is required' error, got: %v", err)
	}
}

func TestWriteAdapter_Definition(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewWriteAdapter(backend)
	if def := adapter.Definition(); def.Name != "write" {
		t.Errorf("expected name 'write', got %q", def.Name)
	}
}

func TestWriteAdapter_Execute_MissingPath(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewWriteAdapter(backend)
	_, err := adapter.Execute(context.Background(), map[string]any{"content": "test"})
	if err == nil || !strings.Contains(err.Error(), "file_path is required") {
		t.Errorf("expected 'file_path is required' error, got: %v", err)
	}
}

func TestEditAdapter_Definition(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewEditAdapter(backend)
	if def := adapter.Definition(); def.Name != "edit" {
		t.Errorf("expected name 'edit', got %q", def.Name)
	}
}

func TestEditAdapter_Execute_MissingPath(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewEditAdapter(backend)
	_, err := adapter.Execute(context.Background(), map[string]any{"old_string": "old", "new_string": "new"})
	if err == nil || !strings.Contains(err.Error(), "file_path is required") {
		t.Errorf("expected 'file_path is required' error, got: %v", err)
	}
}

func TestEditAdapter_Execute_MissingOldString(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewEditAdapter(backend)
	_, err := adapter.Execute(context.Background(), map[string]any{"file_path": "/test/file.txt", "new_string": "new"})
	if err == nil || !strings.Contains(err.Error(), "old_string is required") {
		t.Errorf("expected 'old_string is required' error, got: %v", err)
	}
}

func TestIntArg(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]any
		key      string
		defaultV int
		want     int
	}{
		{"missing", map[string]any{}, "key", 5, 5},
		{"float64", map[string]any{"key": 3.0}, "key", 5, 3},
		{"int", map[string]any{"key": 7}, "key", 5, 7},
		{"wrong type", map[string]any{"key": "hello"}, "key", 5, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := intArg(tt.args, tt.key, tt.defaultV); got != tt.want {
				t.Errorf("intArg() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBashAdapter_BinDirInPath(t *testing.T) {
	backend := &SharedBackend{}
	binDir := "/test/tools/bin"
	adapter := NewBashAdapter(backend, binDir)
	if adapter.binDir != binDir {
		t.Errorf("expected binDir %q, got %q", binDir, adapter.binDir)
	}
}

func TestToolAdapters_NormalizerConfigured(t *testing.T) {
	backend := &SharedBackend{}
	if NewBashAdapter(backend, "").normalizer == nil {
		t.Error("BashAdapter normalizer not configured")
	}
	if NewReadAdapter(backend).normalizer == nil {
		t.Error("ReadAdapter normalizer not configured")
	}
	if NewWriteAdapter(backend).normalizer == nil {
		t.Error("WriteAdapter normalizer not configured")
	}
	if NewEditAdapter(backend).normalizer == nil {
		t.Error("EditAdapter normalizer not configured")
	}
}

func TestReadAdapter_OffsetValidation(t *testing.T) {
	args := map[string]any{"file_path": "/test.txt", "offset": 0}
	offset := intArg(args, "offset", 1)
	if offset != 0 {
		t.Errorf("expected offset 0 from args, got %d", offset)
	}
	if offset < 1 {
		offset = 1
	}
	if offset != 1 {
		t.Errorf("expected corrected offset 1, got %d", offset)
	}
}

func TestAdapters_NilClient(t *testing.T) {
	backend := &SharedBackend{}
	if _, err := NewBashAdapter(backend, "").Execute(context.Background(), map[string]any{"command": "echo test"}); err == nil || !strings.Contains(err.Error(), "boxsh backend not available") {
		t.Errorf("expected backend unavailable error, got: %v", err)
	}
	if _, err := NewReadAdapter(backend).Execute(context.Background(), map[string]any{"file_path": "/test.txt"}); err == nil || !strings.Contains(err.Error(), "boxsh backend not available") {
		t.Errorf("expected backend unavailable error, got: %v", err)
	}
	if _, err := NewWriteAdapter(backend).Execute(context.Background(), map[string]any{"file_path": "/test.txt", "content": "test"}); err == nil || !strings.Contains(err.Error(), "boxsh backend not available") {
		t.Errorf("expected backend unavailable error, got: %v", err)
	}
	if _, err := NewEditAdapter(backend).Execute(context.Background(), map[string]any{"file_path": "/test.txt", "old_string": "old", "new_string": "new"}); err == nil || !strings.Contains(err.Error(), "boxsh backend not available") {
		t.Errorf("expected backend unavailable error, got: %v", err)
	}
}

func TestToolAdapters_IntegrationWithRealBoxsh(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}
	ctx := context.Background()
	annaHome := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "test.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	backend, err := NewSharedBackend(BackendConfig{AnnaHome: annaHome, BinaryPath: "boxsh", Workspace: workspace})
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	if err := backend.Start(ctx, BackendConfig{AnnaHome: annaHome, BinaryPath: "boxsh", Workspace: workspace}); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}
	defer func() { _ = backend.Close() }()

	bash := NewBashAdapter(backend, "")
	read := NewReadAdapter(backend)
	write := NewWriteAdapter(backend)
	edit := NewEditAdapter(backend)

	bashResult, err := bash.Execute(ctx, map[string]any{"command": "cat test.txt"})
	if err != nil || !strings.Contains(bashResult, "line1") {
		t.Fatalf("bash result = %q err=%v", bashResult, err)
	}

	readResult, err := read.Execute(ctx, map[string]any{"file_path": "test.txt", "offset": 1})
	if err != nil || !strings.Contains(readResult, "line1") {
		t.Fatalf("read result = %q err=%v", readResult, err)
	}

	writeResult, err := write.Execute(ctx, map[string]any{"file_path": "new.txt", "content": "hello world"})
	if err != nil || !strings.Contains(writeResult, "Wrote") {
		t.Fatalf("write result = %q err=%v", writeResult, err)
	}

	editResult, err := edit.Execute(ctx, map[string]any{"file_path": "new.txt", "old_string": "hello", "new_string": "goodbye"})
	if err != nil || !strings.Contains(editResult, "Edited") {
		t.Fatalf("edit result = %q err=%v", editResult, err)
	}

	verify, err := read.Execute(ctx, map[string]any{"file_path": "new.txt"})
	if err != nil || !strings.Contains(verify, "goodbye world") {
		t.Fatalf("verify result = %q err=%v", verify, err)
	}
}

func TestBashAdapter_BinDirPrefixing(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}
	ctx := context.Background()
	annaHome := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace")
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	toolPath := filepath.Join(binDir, "hello-tool")
	if err := os.WriteFile(toolPath, []byte("#!/bin/sh\nprintf prefixed-ok\n"), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	backend, err := NewSharedBackend(BackendConfig{AnnaHome: annaHome, BinaryPath: "boxsh", Workspace: workspace})
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	if err := backend.Start(ctx, BackendConfig{AnnaHome: annaHome, BinaryPath: "boxsh", Workspace: workspace}); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}
	defer func() { _ = backend.Close() }()

	result, err := NewBashAdapter(backend, binDir).Execute(ctx, map[string]any{"command": "hello-tool"})
	if err != nil || !strings.Contains(result, "prefixed-ok") {
		t.Fatalf("result = %q err=%v", result, err)
	}
}
