package tracehook

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

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

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		secret string // substring that must NOT survive
	}{
		{"bearer", "curl -H 'Authorization: Bearer sk-abc123def456'", "sk-abc123def456"},
		{"apikey assign", "export API_KEY=secret123", "secret123"},
		{"token colon", "token: ghp_xyz0123456789abcd", "ghp_xyz0123456789abcd"},
		{"password", "PGPASSWORD=hunter2 psql", "hunter2"},
		{"url creds", "postgres://user:pass@host:5432/db", ":pass@"},
		{"json apikey", `{"api_key":"sk-abc123def456ghi"}`, "sk-abc123def456ghi"},
		{"basic auth", "Authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA=="},
		{"bare token", "use sk-proj-abcdef1234567890 here", "sk-proj-abcdef1234567890"},
		{"cookie", "Cookie: session=deadbeefcafe1234", "deadbeefcafe1234"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactSecrets(c.in)
			if strings.Contains(got, c.secret) {
				t.Errorf("redactSecrets(%q) = %q, still leaks %q", c.in, got, c.secret)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("redactSecrets(%q) = %q, missing [REDACTED] marker", c.in, got)
			}
		})
	}
}

func TestRedactSecrets_Clean(t *testing.T) {
	in := "ls -la /tmp/output.log"
	if got := redactSecrets(in); got != in {
		t.Errorf("redactSecrets(%q) = %q, want unchanged", in, got)
	}
}

func TestNew_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h := New(false, false)
	if h == nil {
		t.Fatal("expected non-nil hook")
	}
	if h.otelEnabled() {
		t.Error("expected OTel disabled when no endpoint set")
	}
}

func TestHook_NameAndPriority(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h := New(false, false)
	if h.Name() != "trace" {
		t.Errorf("expected name 'trace', got %q", h.Name())
	}
	if h.Priority() != 0 {
		t.Errorf("expected priority 0, got %d", h.Priority())
	}
}

func TestHook_Close_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h := New(false, false)
	if err := h.Close(); err != nil {
		t.Fatalf("unexpected error on close: %v", err)
	}
}

func TestHook_OnPreAgentCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h := New(false, false)
	// Should not panic.
	h.OnPreAgentCall(context.Background(), &hooks.PreAgentCallContext{
		HookMeta: hooks.HookMeta{SessionID: "s1", AgentID: "a1", UserID: "1"},
		Channel:  "cli",
	})
}

func TestHook_OnPostAgentCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h := New(false, false)
	h.OnPostAgentCall(context.Background(), &hooks.PostAgentCallContext{
		HookMeta: hooks.HookMeta{SessionID: "s1"},
		Duration: time.Second,
	})
}

func TestHook_OnPreToolCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h := New(false, false)
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
	h := New(false, false)
	h.OnPostToolCall(context.Background(), &hooks.PostToolCallContext{
		HookMeta: hooks.HookMeta{SessionID: "s1"},
		ToolName: "bash",
		Result:   "output",
		Duration: 10 * time.Millisecond,
	})
}

func TestHook_OnPreLLMCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h := New(false, false)
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
	h := New(false, false)
	h.OnPostLLMCall(context.Background(), &hooks.PostLLMCallContext{
		HookMeta: hooks.HookMeta{SessionID: "s1"},
		Model:    "claude-3",
		Usage:    ai.Usage{InputTokens: 100, OutputTokens: 50},
		Duration: time.Second,
	})
}

func TestHook_OnPostMemoryCall_NoOtel(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	h := New(false, false)
	h.OnPostMemoryCall(context.Background(), &hooks.PostMemoryCallContext{
		Op:       hooks.MemoryOpAppend,
		Duration: 5 * time.Millisecond,
	})
}
