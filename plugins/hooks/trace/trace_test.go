package trace

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

func TestLoadConfig_Defaults(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_SERVICE_NAME", "")
	cfg := loadConfig()
	if cfg.Enabled {
		t.Error("expected Enabled=false when no endpoint set")
	}
	if cfg.ServiceName != "stella" {
		t.Errorf("expected default service name 'stella', got %q", cfg.ServiceName)
	}
	if cfg.SampleRate != 1.0 {
		t.Errorf("expected SampleRate=1.0, got %f", cfg.SampleRate)
	}
}

func TestLoadConfig_WithEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	cfg := loadConfig()
	if !cfg.Enabled {
		t.Error("expected Enabled=true when endpoint is set")
	}
}

func TestLoadConfig_CustomServiceName(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "my-service")
	cfg := loadConfig()
	if cfg.ServiceName != "my-service" {
		t.Errorf("expected service name 'my-service', got %q", cfg.ServiceName)
	}
}

func TestSessionKey(t *testing.T) {
	got := sessionKey("agent-1", "sess-1")
	if got != "agent-1:sess-1" {
		t.Errorf("unexpected session key: %q", got)
	}
}

func TestSummarizeArgs_Bash(t *testing.T) {
	got := summarizeArgs("bash", map[string]any{"command": "ls -la"})
	if got != "ls -la" {
		t.Errorf("expected 'ls -la', got %q", got)
	}
}

func TestSummarizeArgs_BashLong(t *testing.T) {
	longCmd := make([]byte, 300)
	for i := range longCmd {
		longCmd[i] = 'x'
	}
	got := summarizeArgs("bash", map[string]any{"command": string(longCmd)})
	if len(got) > 210 { // 200 chars + "..."
		t.Errorf("expected truncated output, got len=%d", len(got))
	}
}

func TestSummarizeArgs_ReadTool(t *testing.T) {
	got := summarizeArgs("read", map[string]any{"path": "/tmp/test.txt"})
	if got != "/tmp/test.txt" {
		t.Errorf("expected file path, got %q", got)
	}
}

func TestSummarizeArgs_Unknown(t *testing.T) {
	got := summarizeArgs("unknown", map[string]any{"key": "value"})
	if got == "" {
		t.Error("expected non-empty summary for unknown tool")
	}
}

func TestNewHook_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h, err := newHook()
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("expected non-nil hook")
	}
	if h.otelEnabled() {
		t.Error("expected OTel disabled when no endpoint set")
	}
}

func TestHook_NameAndPriority(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h, _ := newHook()
	if h.Name() != "trace" {
		t.Errorf("expected name 'trace', got %q", h.Name())
	}
	if h.Priority() != 0 {
		t.Errorf("expected priority 0, got %d", h.Priority())
	}
}

func TestHook_Close_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h, _ := newHook()
	if err := h.Close(); err != nil {
		t.Fatalf("unexpected error on close: %v", err)
	}
}

func TestHook_OnPreAgentCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h, _ := newHook()
	// Should not panic.
	h.OnPreAgentCall(context.Background(), &hooks.PreAgentCallContext{
		HookMeta: hooks.HookMeta{SessionID: "s1", AgentID: "a1", UserID: 1},
		Channel:  "cli",
	})
}

func TestHook_OnPostAgentCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h, _ := newHook()
	h.OnPostAgentCall(context.Background(), &hooks.PostAgentCallContext{
		HookMeta: hooks.HookMeta{SessionID: "s1"},
		Duration: time.Second,
	})
}

func TestHook_OnPreToolCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h, _ := newHook()
	result, err := h.OnPreToolCall(context.Background(), &hooks.PreToolCallContext{
		HookMeta:  hooks.HookMeta{SessionID: "s1"},
		ToolName:  "bash",
		Arguments: map[string]any{"command": "echo hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Block {
		t.Error("expected not blocked")
	}
}

func TestHook_OnPostToolCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h, _ := newHook()
	h.OnPostToolCall(context.Background(), &hooks.PostToolCallContext{
		HookMeta: hooks.HookMeta{SessionID: "s1"},
		ToolName: "bash",
		Result:   "output",
		Duration: 10 * time.Millisecond,
	})
}

func TestHook_OnPreLLMCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h, _ := newHook()
	result, err := h.OnPreLLMCall(context.Background(), &hooks.PreLLMCallContext{
		HookMeta: hooks.HookMeta{SessionID: "s1"},
		Model:    "claude-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Context != nil {
		t.Error("expected nil context when OTel disabled")
	}
}

func TestHook_OnPostLLMCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h, _ := newHook()
	h.OnPostLLMCall(context.Background(), &hooks.PostLLMCallContext{
		HookMeta: hooks.HookMeta{SessionID: "s1"},
		Model:    "claude-3",
		Usage:    ai.Usage{InputTokens: 100, OutputTokens: 50},
		Duration: time.Second,
	})
}

func TestHook_OnPostMemoryCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h, _ := newHook()
	h.OnPostMemoryCall(context.Background(), &hooks.PostMemoryCallContext{
		Op:       hooks.MemoryOpAppend,
		Duration: 5 * time.Millisecond,
	})
}
