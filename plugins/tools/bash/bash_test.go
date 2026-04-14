package bash

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBashTool_Definition(t *testing.T) {
	tool := NewBashTool("", "")
	def := tool.Definition()
	if def.Name != "bash" {
		t.Errorf("expected name 'bash', got %q", def.Name)
	}
}

func TestBashTool_SimpleCommand(t *testing.T) {
	tool := NewBashTool("", "")
	result, err := tool.Execute(context.Background(), map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("expected 'hello' in output, got %q", result)
	}
}

func TestBashTool_ExitCodeError(t *testing.T) {
	tool := NewBashTool("", "")
	result, err := tool.Execute(context.Background(), map[string]any{
		"command": "exit 1",
	})
	if err == nil {
		t.Error("expected error for exit code 1")
	}
	if !strings.Contains(result, "[exit:1") {
		t.Errorf("expected exit code in output, got %q", result)
	}
}

func TestBashTool_Stderr(t *testing.T) {
	tool := NewBashTool("", "")
	result, _ := tool.Execute(context.Background(), map[string]any{
		"command": "echo err >&2; exit 1",
	})
	if !strings.Contains(result, "err") {
		t.Errorf("expected stderr in output, got %q", result)
	}
}

func TestBashTool_MissingCommand(t *testing.T) {
	tool := NewBashTool("", "")
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error when command is missing")
	}
}

func TestBashTool_MetadataFooter(t *testing.T) {
	tool := NewBashTool("", "")
	result, err := tool.Execute(context.Background(), map[string]any{
		"command": "echo test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "[exit:0") {
		t.Errorf("expected metadata footer in output, got %q", result)
	}
}

func TestBashTool_Timeout(t *testing.T) {
	tool := NewBashTool("", "")
	result, err := tool.Execute(context.Background(), map[string]any{
		"command": "sleep 2",
		"timeout": 1,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if !strings.Contains(result, "[Command timed out after 1s]") {
		t.Fatalf("expected timeout note, got %q", result)
	}
	if !strings.Contains(result, "[exit:124") {
		t.Fatalf("expected timeout exit metadata, got %q", result)
	}
}

func TestBashTool_TimeoutAllowsPartialOutput(t *testing.T) {
	tool := NewBashTool("", "")
	result, err := tool.Execute(context.Background(), map[string]any{
		"command": "echo before; sleep 2",
		"timeout": 1,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(result, "before") {
		t.Fatalf("expected partial output before timeout, got %q", result)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Microsecond, "500µs"},
		{5 * time.Millisecond, "5ms"},
		{1500 * time.Millisecond, "1.5s"},
		{90 * time.Second, "90s"},
	}
	for _, tc := range tests {
		got := formatDuration(tc.d)
		if got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestEnvWithToolsBin_EmptyDir(t *testing.T) {
	env := envWithToolsBin("")
	if len(env) == 0 {
		t.Error("expected non-empty env")
	}
}

func TestEnvWithToolsBin_WithDir(t *testing.T) {
	env := envWithToolsBin("/custom/bin")
	var pathFound bool
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") && strings.Contains(e, "/custom/bin") {
			pathFound = true
			break
		}
	}
	if !pathFound {
		t.Error("expected /custom/bin in PATH")
	}
}

func TestBashIntArg(t *testing.T) {
	tests := []struct {
		args map[string]any
		want int
	}{
		{args: map[string]any{"timeout": float64(3)}, want: 3},
		{args: map[string]any{"timeout": 2}, want: 2},
		{args: map[string]any{"timeout": int64(4)}, want: 4},
		{args: map[string]any{"timeout": "bad"}, want: 7},
		{args: map[string]any{}, want: 7},
	}
	for _, tc := range tests {
		if got := bashIntArg(tc.args, "timeout", 7); got != tc.want {
			t.Fatalf("bashIntArg(%v) = %d, want %d", tc.args, got, tc.want)
		}
	}
}
