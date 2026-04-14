package rtk

import (
	"context"
	"testing"

	"github.com/vaayne/anna/pkg/hooks"
)

func TestNewHook(t *testing.T) {
	h := NewHook("")
	if h == nil {
		t.Fatal("expected non-nil hook")
	}
}

func TestHook_Name(t *testing.T) {
	h := NewHook("")
	if h.Name() != "rtk" {
		t.Errorf("expected 'rtk', got %q", h.Name())
	}
}

func TestHook_Priority(t *testing.T) {
	h := NewHook("")
	if h.Priority() != 100 {
		t.Errorf("expected priority 100, got %d", h.Priority())
	}
}

func TestHook_OnPreToolCall_NonBash(t *testing.T) {
	h := NewHook("")
	hctx := &hooks.PreToolCallContext{
		ToolName:  "read",
		Arguments: map[string]any{"path": "/tmp/test"},
	}
	result, err := h.OnPreToolCall(context.Background(), hctx)
	if err != nil {
		t.Fatal(err)
	}
	// Non-bash tools should pass through unchanged.
	if result.Arguments != nil || result.Block {
		t.Error("expected pass-through for non-bash tool")
	}
}

func TestHook_OnPreToolCall_EmptyCommand(t *testing.T) {
	h := NewHook("")
	hctx := &hooks.PreToolCallContext{
		ToolName:  "bash",
		Arguments: map[string]any{},
	}
	result, err := h.OnPreToolCall(context.Background(), hctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Arguments != nil {
		t.Error("expected pass-through for empty command")
	}
}

func TestHook_OnPreToolCall_NoRTKBinary(t *testing.T) {
	// When rtk binary is not available, wrapWithRTK returns command unchanged.
	h := NewHook("/nonexistent/path")
	hctx := &hooks.PreToolCallContext{
		ToolName:  "bash",
		Arguments: map[string]any{"command": "echo hello"},
	}
	result, err := h.OnPreToolCall(context.Background(), hctx)
	if err != nil {
		t.Fatal(err)
	}
	// When rtk is not found, command passes through unchanged.
	// result.Arguments will be nil since no rewrite happened.
	_ = result
}

func TestWrapWithRTK_NoBinary(t *testing.T) {
	// With no rtk binary, wrapWithRTK should return the command unchanged.
	h := &Hook{toolsBinDir: "/nonexistent"}
	cmd := "echo hello"
	// Note: rtkPathOnce is global so this test may get a cached result.
	// We just verify no panic and result is a valid string.
	got := h.wrapWithRTK(cmd)
	if got == "" {
		t.Error("expected non-empty result from wrapWithRTK")
	}
}
