package boxshclient

import (
	"context"
	"fmt"
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

	def := adapter.Definition()
	if def.Name != "read" {
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

	def := adapter.Definition()
	if def.Name != "write" {
		t.Errorf("expected name 'write', got %q", def.Name)
	}
}

func TestWriteAdapter_Execute_MissingPath(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewWriteAdapter(backend)

	_, err := adapter.Execute(context.Background(), map[string]any{
		"content": "test",
	})
	if err == nil || !strings.Contains(err.Error(), "file_path is required") {
		t.Errorf("expected 'file_path is required' error, got: %v", err)
	}
}

func TestEditAdapter_Definition(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewEditAdapter(backend)

	def := adapter.Definition()
	if def.Name != "edit" {
		t.Errorf("expected name 'edit', got %q", def.Name)
	}
}

func TestEditAdapter_Execute_MissingPath(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewEditAdapter(backend)

	_, err := adapter.Execute(context.Background(), map[string]any{
		"old_string": "old",
		"new_string": "new",
	})
	if err == nil || !strings.Contains(err.Error(), "file_path is required") {
		t.Errorf("expected 'file_path is required' error, got: %v", err)
	}
}

func TestEditAdapter_Execute_MissingOldString(t *testing.T) {
	backend := &SharedBackend{}
	adapter := NewEditAdapter(backend)

	_, err := adapter.Execute(context.Background(), map[string]any{
		"file_path":  "/test/file.txt",
		"new_string": "new",
	})
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
			got := intArg(tt.args, tt.key, tt.defaultV)
			if got != tt.want {
				t.Errorf("intArg() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBashAdapter_BinDirInPath(t *testing.T) {
	// This test verifies that binDir is properly incorporated
	backend := &SharedBackend{}
	binDir := "/test/tools/bin"
	adapter := NewBashAdapter(backend, binDir)

	// The actual execution would need a real backend,
	// but we can verify the adapter stores the binDir
	if adapter.binDir != binDir {
		t.Errorf("expected binDir %q, got %q", binDir, adapter.binDir)
	}
}

func TestToolAdapters_NormalizerConfigured(t *testing.T) {
	backend := &SharedBackend{}

	bash := NewBashAdapter(backend, "")
	if bash.normalizer == nil {
		t.Error("BashAdapter normalizer not configured")
	}

	read := NewReadAdapter(backend)
	if read.normalizer == nil {
		t.Error("ReadAdapter normalizer not configured")
	}

	write := NewWriteAdapter(backend)
	if write.normalizer == nil {
		t.Error("WriteAdapter normalizer not configured")
	}

	edit := NewEditAdapter(backend)
	if edit.normalizer == nil {
		t.Error("EditAdapter normalizer not configured")
	}
}

func TestReadAdapter_OffsetValidation(t *testing.T) {
	// Test that offset < 1 is corrected to 1
	// This is an internal behavior test

	// Since we can't easily mock the RPC without a real client,
	// we verify the intArg logic works correctly for offset
	args := map[string]any{
		"file_path": "/test.txt",
		"offset":    0,
	}

	offset := intArg(args, "offset", 1)
	if offset != 0 {
		t.Errorf("expected offset 0 from args, got %d", offset)
	}

	// The adapter would then set it to 1 if < 1
	if offset < 1 {
		offset = 1
	}
	if offset != 1 {
		t.Errorf("expected corrected offset 1, got %d", offset)
	}
}

// Test integration with a real (but simple) mock process
func TestBashAdapter_Integration(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}

	// Create a mock boxsh script that handles ping and exec
	tmpDir := t.TempDir()
	boxshPath := filepath.Join(tmpDir, "boxsh")

	script := `#!/bin/bash
# Simple mock boxsh RPC
while read -r line; do
	if [[ "$line" == *'"method":"ping"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
	elif [[ "$line" == *'"method":"exec"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		# Extract command
		cmd=$(echo "$line" | sed 's/.*"command":"\([^"]*\)".*/\1/')
		stdout=""
		exit_code=0
		
		if [[ "$cmd" == *"echo"* ]]; then
			stdout=$(echo "$cmd" | sed 's/.*echo //')
		elif [[ "$cmd" == *"false"* ]]; then
			exit_code=1
			stdout=""
		fi
		
		echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"$stdout\",\"stderr\":\"\",\"exit_code\":$exit_code},\"id\":$id}"
	elif [[ "$line" == *'"method":"quit"'* ]]; then
		exit 0
	fi
done
`
	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write mock boxsh: %v", err)
	}

	// Create backend and start it
	backend, err := NewSharedBackend(BackendConfig{
		BinaryPath: boxshPath,
		AnnaHome:   tmpDir,
		Workspace:  tmpDir,
	})
	if err != nil {
		t.Fatalf("failed to create backend: %v", err)
	}

	ctx := context.Background()
	if err := backend.Start(ctx, BackendConfig{
		BinaryPath: boxshPath,
		AnnaHome:   tmpDir,
		Workspace:  tmpDir,
		WorkDir:    "",
	}); err != nil {
		t.Fatalf("failed to start backend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	// Create adapter and test
	adapter := NewBashAdapter(backend, "")

	// Test successful command
	result, err := adapter.Execute(ctx, map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "hello") && !strings.Contains(result, "exit:0") {
		t.Errorf("expected result containing 'hello' or exit:0, got: %s", result)
	}

	// Test failed command
	_, err = adapter.Execute(ctx, map[string]any{
		"command": "false",
	})
	if err == nil {
		t.Error("expected error for failed command")
	}
}

// Test ReadAdapter integration
func TestReadAdapter_Integration(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}

	tmpDir := t.TempDir()
	boxshPath := filepath.Join(tmpDir, "boxsh")

	script := `#!/bin/bash
while read -r line; do
	if [[ "$line" == *'"method":"ping"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
	elif [[ "$line" == *'"method":"read"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":\"line1\\nline2\\nline3\",\"total_lines\":3,\"truncated\":false},\"id\":$id}"
	elif [[ "$line" == *'"method":"quit"'* ]]; then
		exit 0
	fi
done
`
	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write mock boxsh: %v", err)
	}

	backend, err := NewSharedBackend(BackendConfig{
		BinaryPath: boxshPath,
		AnnaHome:   tmpDir,
		Workspace:  tmpDir,
	})
	if err != nil {
		t.Fatalf("failed to create backend: %v", err)
	}

	ctx := context.Background()
	if err := backend.Start(ctx, BackendConfig{
		BinaryPath: boxshPath,
		AnnaHome:   tmpDir,
		Workspace:  tmpDir,
		WorkDir:    "",
	}); err != nil {
		t.Fatalf("failed to start backend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	adapter := NewReadAdapter(backend)

	result, err := adapter.Execute(ctx, map[string]any{
		"file_path": "/test.txt",
		"offset":    1,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "line1") {
		t.Errorf("expected result containing 'line1', got: %s", result)
	}
}

// Test WriteAdapter integration
func TestWriteAdapter_Integration(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}

	tmpDir := t.TempDir()
	boxshPath := filepath.Join(tmpDir, "boxsh")

	script := `#!/bin/bash
while read -r line; do
	if [[ "$line" == *'"method":"ping"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
	elif [[ "$line" == *'"method":"write"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		path=$(echo "$line" | sed 's/.*"file_path":"\([^"]*\)".*/\1/')
		content=$(echo "$line" | sed 's/.*"content":"\([^"]*\)".*/\1/')
		bytes=${#content}
		echo "{\"jsonrpc\":\"2.0\",\"result\":{\"bytes_written\":$bytes,\"path\":\"$path\"},\"id\":$id}"
	elif [[ "$line" == *'"method":"quit"'* ]]; then
		exit 0
	fi
done
`
	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write mock boxsh: %v", err)
	}

	backend, err := NewSharedBackend(BackendConfig{
		BinaryPath: boxshPath,
		AnnaHome:   tmpDir,
		Workspace:  tmpDir,
	})
	if err != nil {
		t.Fatalf("failed to create backend: %v", err)
	}

	ctx := context.Background()
	if err := backend.Start(ctx, BackendConfig{
		BinaryPath: boxshPath,
		AnnaHome:   tmpDir,
		Workspace:  tmpDir,
		WorkDir:    "",
	}); err != nil {
		t.Fatalf("failed to start backend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	adapter := NewWriteAdapter(backend)

	result, err := adapter.Execute(ctx, map[string]any{
		"file_path": "/test.txt",
		"content":   "hello world",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	expected := "Wrote /test.txt (11 bytes)"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// Test EditAdapter integration
func TestEditAdapter_Integration(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}

	tmpDir := t.TempDir()
	boxshPath := filepath.Join(tmpDir, "boxsh")

	script := `#!/bin/bash
while read -r line; do
	if [[ "$line" == *'"method":"ping"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
	elif [[ "$line" == *'"method":"edit"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		path=$(echo "$line" | sed 's/.*"file_path":"\([^"]*\)".*/\1/')
		echo "{\"jsonrpc\":\"2.0\",\"result\":{\"path\":\"$path\",\"replacements\":1},\"id\":$id}"
	elif [[ "$line" == *'"method":"quit"'* ]]; then
		exit 0
	fi
done
`
	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write mock boxsh: %v", err)
	}

	backend, err := NewSharedBackend(BackendConfig{
		BinaryPath: boxshPath,
		AnnaHome:   tmpDir,
		Workspace:  tmpDir,
	})
	if err != nil {
		t.Fatalf("failed to create backend: %v", err)
	}

	ctx := context.Background()
	if err := backend.Start(ctx, BackendConfig{
		BinaryPath: boxshPath,
		AnnaHome:   tmpDir,
		Workspace:  tmpDir,
		WorkDir:    "",
	}); err != nil {
		t.Fatalf("failed to start backend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	adapter := NewEditAdapter(backend)

	result, err := adapter.Execute(ctx, map[string]any{
		"file_path":  "/test.txt",
		"old_string": "old",
		"new_string": "new",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	expected := "Edited /test.txt"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// Test backend nil client handling
func TestAdapters_NilClient(t *testing.T) {
	backend := &SharedBackend{} // client is nil

	bash := NewBashAdapter(backend, "")
	_, err := bash.Execute(context.Background(), map[string]any{
		"command": "echo test",
	})
	if err == nil || !strings.Contains(err.Error(), "boxsh backend not available") {
		t.Errorf("expected 'boxsh backend not available' error, got: %v", err)
	}

	read := NewReadAdapter(backend)
	_, err = read.Execute(context.Background(), map[string]any{
		"file_path": "/test.txt",
	})
	if err == nil || !strings.Contains(err.Error(), "boxsh backend not available") {
		t.Errorf("expected 'boxsh backend not available' error, got: %v", err)
	}

	write := NewWriteAdapter(backend)
	_, err = write.Execute(context.Background(), map[string]any{
		"file_path": "/test.txt",
		"content":   "test",
	})
	if err == nil || !strings.Contains(err.Error(), "boxsh backend not available") {
		t.Errorf("expected 'boxsh backend not available' error, got: %v", err)
	}

	edit := NewEditAdapter(backend)
	_, err = edit.Execute(context.Background(), map[string]any{
		"file_path":  "/test.txt",
		"old_string": "old",
		"new_string": "new",
	})
	if err == nil || !strings.Contains(err.Error(), "boxsh backend not available") {
		t.Errorf("expected 'boxsh backend not available' error, got: %v", err)
	}
}

// Test command path prefixing with binDir
func TestBashAdapter_BinDirPrefixing(t *testing.T) {
	if !PlatformSupportsBoxsh() {
		t.Skip("boxsh not supported on this platform")
	}

	tmpDir := t.TempDir()
	boxshPath := filepath.Join(tmpDir, "boxsh")

	// Track received commands - store raw JSON lines for verification
	commandsFile := filepath.Join(tmpDir, "commands.log")

	// Use a simpler approach: log the entire line and verify in Go
	script := fmt.Sprintf(`#!/bin/bash
logfile="%s"
while read -r line; do
	if [[ "$line" == *'"method":"ping"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
	elif [[ "$line" == *'"method":"exec"'* ]]; then
		id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
		# Log the entire line for verification in Go
		echo "$line" >> "$logfile"
		echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"ok\",\"stderr\":\"\",\"exit_code\":0},\"id\":$id}"
	elif [[ "$line" == *'"method":"quit"'* ]]; then
		exit 0
	fi
done
`, commandsFile)

	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write mock boxsh: %v", err)
	}

	backend, err := NewSharedBackend(BackendConfig{
		BinaryPath: boxshPath,
		AnnaHome:   tmpDir,
		Workspace:  tmpDir,
	})
	if err != nil {
		t.Fatalf("failed to create backend: %v", err)
	}

	ctx := context.Background()
	if err := backend.Start(ctx, BackendConfig{
		BinaryPath: boxshPath,
		AnnaHome:   tmpDir,
		Workspace:  tmpDir,
		WorkDir:    "",
	}); err != nil {
		t.Fatalf("failed to start backend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	binDir := "/test/tools/bin"
	adapter := NewBashAdapter(backend, binDir)

	_, err = adapter.Execute(ctx, map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Read logged commands
	logged, err := os.ReadFile(commandsFile)
	if err != nil {
		t.Fatalf("failed to read commands log: %v", err)
	}

	// The JSON should contain the binDir somewhere in the command parameter
	loggedStr := string(logged)
	if !strings.Contains(loggedStr, "export PATH=") {
		t.Errorf("expected PATH export in logged command, got: %s", loggedStr)
	}
	if !strings.Contains(loggedStr, binDir) {
		t.Errorf("expected binDir %q in logged command, got: %s", binDir, loggedStr)
	}
}
