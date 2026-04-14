package boxshclient

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewNormalizer(t *testing.T) {
	n := NewNormalizer()
	if n == nil {
		t.Fatal("NewNormalizer() returned nil")
	}
	if n.MaxOutputLen != 0 {
		t.Errorf("MaxOutputLen = %d, want 0 (default)", n.MaxOutputLen)
	}
	if !n.IncludeTimestamps {
		t.Error("IncludeTimestamps should be true by default")
	}
}

func TestNormalizeExec(t *testing.T) {
	n := NewNormalizer()

	result := &ExecResult{
		Stdout:   "hello world",
		Stderr:   "",
		ExitCode: 0,
	}
	elapsed := 100 * time.Millisecond

	norm := n.NormalizeExec(result, elapsed)

	if norm.IsError {
		t.Error("IsError should be false for exit code 0")
	}
	if !strings.Contains(norm.Content, "hello world") {
		t.Errorf("Content should contain stdout, got: %q", norm.Content)
	}
	if !strings.Contains(norm.Content, "exit:0") {
		t.Errorf("Content should contain exit code, got: %q", norm.Content)
	}
	if !strings.Contains(norm.Content, "100ms") {
		t.Errorf("Content should contain duration, got: %q", norm.Content)
	}

	// Check metadata.
	if norm.Metadata["exit_code"] != 0 {
		t.Errorf("Metadata exit_code = %v, want 0", norm.Metadata["exit_code"])
	}
	if norm.Metadata["elapsed_ms"] != int64(100) {
		t.Errorf("Metadata elapsed_ms = %v, want 100", norm.Metadata["elapsed_ms"])
	}
}

func TestNormalizeExecWithStderr(t *testing.T) {
	n := NewNormalizer()

	result := &ExecResult{
		Stdout:   "output",
		Stderr:   "error message",
		ExitCode: 1,
	}
	elapsed := 50 * time.Millisecond

	norm := n.NormalizeExec(result, elapsed)

	if !norm.IsError {
		t.Error("IsError should be true for non-zero exit code")
	}
	if !strings.Contains(norm.Content, "output") {
		t.Errorf("Content should contain stdout, got: %q", norm.Content)
	}
	if !strings.Contains(norm.Content, "error message") {
		t.Errorf("Content should contain stderr, got: %q", norm.Content)
	}
}

func TestNormalizeExecWithLargeOutput(t *testing.T) {
	n := NewNormalizer()
	n.MaxOutputLen = 100

	result := &ExecResult{
		Stdout:   strings.Repeat("a", 200),
		Stderr:   "",
		ExitCode: 0,
	}
	elapsed := time.Second

	norm := n.NormalizeExec(result, elapsed)

	if !strings.Contains(norm.Content, "[...truncated]") {
		t.Errorf("Content should indicate truncation, got: %q", norm.Content)
	}
}

func TestNormalizeExecWithoutTimestamps(t *testing.T) {
	n := NewNormalizer()
	n.IncludeTimestamps = false

	result := &ExecResult{
		Stdout:   "hello",
		Stderr:   "",
		ExitCode: 0,
	}
	elapsed := time.Second

	norm := n.NormalizeExec(result, elapsed)

	if strings.Contains(norm.Content, "exit:") {
		t.Errorf("Content should not contain exit code when timestamps disabled, got: %q", norm.Content)
	}
	if strings.Contains(norm.Content, "|") {
		t.Errorf("Content should not contain metadata footer when timestamps disabled, got: %q", norm.Content)
	}
}

func TestNormalizeRead(t *testing.T) {
	n := NewNormalizer()

	result := &ReadResult{
		Content:    "line1\nline2\nline3",
		TotalLines: 10,
		Truncated:  false,
	}

	norm := n.NormalizeRead(result, "/path/to/file.txt", 1)

	if norm.IsError {
		t.Error("IsError should be false for successful read")
	}
	if !strings.Contains(norm.Content, "line1") {
		t.Errorf("Content should contain file content, got: %q", norm.Content)
	}

	// Check metadata.
	if norm.Metadata["total_lines"] != 10 {
		t.Errorf("Metadata total_lines = %v, want 10", norm.Metadata["total_lines"])
	}
	if norm.Metadata["path"] != "/path/to/file.txt" {
		t.Errorf("Metadata path = %v, want /path/to/file.txt", norm.Metadata["path"])
	}
}

func TestNormalizeReadWithPaginationHint(t *testing.T) {
	n := NewNormalizer()

	result := &ReadResult{
		Content:    strings.Repeat("line\n", 20),
		TotalLines: 20,
		Truncated:  true,
	}

	norm := n.NormalizeRead(result, "/path/to/file.txt", 1)

	if !strings.Contains(norm.Content, "offset=") {
		t.Errorf("Content should contain pagination hint, got: %q", norm.Content)
	}
}

func TestNormalizeReadWithTruncation(t *testing.T) {
	n := NewNormalizer()

	result := &ReadResult{
		Content:    strings.Repeat("a", 100000),
		TotalLines: 1000,
		Truncated:  true,
	}

	norm := n.NormalizeRead(result, "/path/to/file.txt", 1)

	if !strings.Contains(norm.Content, "[Content truncated") {
		t.Errorf("Content should indicate truncation, got: %q", norm.Content)
	}
}

func TestNormalizeWrite(t *testing.T) {
	n := NewNormalizer()

	result := &WriteResult{
		Path:         "/path/to/file.txt",
		BytesWritten: 100,
	}

	norm := n.NormalizeWrite(result)

	if norm.IsError {
		t.Error("IsError should be false for successful write")
	}
	if !strings.Contains(norm.Content, "/path/to/file.txt") {
		t.Errorf("Content should contain path, got: %q", norm.Content)
	}
	if !strings.Contains(norm.Content, "100 bytes") {
		t.Errorf("Content should contain byte count, got: %q", norm.Content)
	}

	// Check metadata.
	if norm.Metadata["bytes_written"] != 100 {
		t.Errorf("Metadata bytes_written = %v, want 100", norm.Metadata["bytes_written"])
	}
}

func TestNormalizeEdit(t *testing.T) {
	n := NewNormalizer()

	result := &EditResult{
		Path:         "/path/to/file.txt",
		Replacements: 1,
	}

	norm := n.NormalizeEdit(result)

	if norm.IsError {
		t.Error("IsError should be false for successful edit")
	}
	if !strings.Contains(norm.Content, "Edited /path/to/file.txt") {
		t.Errorf("Content should contain edit message, got: %q", norm.Content)
	}

	// Check metadata.
	if norm.Metadata["replacements"] != 1 {
		t.Errorf("Metadata replacements = %v, want 1", norm.Metadata["replacements"])
	}
}

func TestNormalizeEditMultipleReplacements(t *testing.T) {
	n := NewNormalizer()

	result := &EditResult{
		Path:         "/path/to/file.txt",
		Replacements: 3,
	}

	norm := n.NormalizeEdit(result)

	if !strings.Contains(norm.Content, "(3 replacements)") {
		t.Errorf("Content should contain replacement count, got: %q", norm.Content)
	}
}

func TestNormalizeError(t *testing.T) {
	n := NewNormalizer()

	err := errors.New("something went wrong")
	norm := n.NormalizeError(err, "read")

	if !norm.IsError {
		t.Error("IsError should be true")
	}
	if !strings.Contains(norm.Content, "read:") {
		t.Errorf("Content should contain tool name, got: %q", norm.Content)
	}
	if !strings.Contains(norm.Content, "something went wrong") {
		t.Errorf("Content should contain error message, got: %q", norm.Content)
	}

	// Check metadata.
	if norm.Metadata["tool"] != "read" {
		t.Errorf("Metadata tool = %v, want read", norm.Metadata["tool"])
	}
	if norm.Metadata["error_type"] != "execution_error" {
		t.Errorf("Metadata error_type = %v, want execution_error", norm.Metadata["error_type"])
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{1 * time.Microsecond, "1µs"},
		{100 * time.Microsecond, "100µs"},
		{1 * time.Millisecond, "1ms"},
		{100 * time.Millisecond, "100ms"},
		{1 * time.Second, "1.0s"},
		{1500 * time.Millisecond, "1.5s"},
		{60 * time.Second, "60s"},
		{90 * time.Second, "90s"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatDuration(tt.d)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestFormatMetadataFooter(t *testing.T) {
	footer := formatMetadataFooter(1, 1500*time.Millisecond)
	expected := "\n[exit:1 | 1.5s]"
	if footer != expected {
		t.Errorf("formatMetadataFooter = %q, want %q", footer, expected)
	}
}

// Test that normalization preserves compatible output for all tool types.
func TestNormalizationCompatibility(t *testing.T) {
	n := NewNormalizer()

	// Test that all normalize methods return valid results.
	t.Run("exec", func(t *testing.T) {
		result := n.NormalizeExec(&ExecResult{ExitCode: 0}, time.Second)
		if result.Content == "" {
			t.Error("Exec normalization returned empty content")
		}
	})

	t.Run("read", func(t *testing.T) {
		result := n.NormalizeRead(&ReadResult{Content: "test"}, "/file.txt", 1)
		if result.Content == "" {
			t.Error("Read normalization returned empty content")
		}
	})

	t.Run("write", func(t *testing.T) {
		result := n.NormalizeWrite(&WriteResult{Path: "/file.txt", BytesWritten: 4})
		if result.Content == "" {
			t.Error("Write normalization returned empty content")
		}
	})

	t.Run("edit", func(t *testing.T) {
		result := n.NormalizeEdit(&EditResult{Path: "/file.txt", Replacements: 1})
		if result.Content == "" {
			t.Error("Edit normalization returned empty content")
		}
	})

	t.Run("error", func(t *testing.T) {
		result := n.NormalizeError(errors.New("test"), "tool")
		if result.Content == "" {
			t.Error("Error normalization returned empty content")
		}
		if !result.IsError {
			t.Error("Error normalization should set IsError=true")
		}
	})
}
