package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
)

func integrationConfig(t *testing.T) GoRunnerConfig {
	t.Helper()
	skipWithoutAnthropicKey(t)
	model := os.Getenv("STELLA_GO_MODEL")
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}
	stellaHome := t.TempDir()
	workspace := t.TempDir()
	userRoot := workspace + "/users/1"
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return GoRunnerConfig{
		API:        "anthropic",
		Model:      model,
		APIKey:     os.Getenv("ANTHROPIC_API_KEY"),
		BaseURL:    os.Getenv("ANTHROPIC_BASE_URL"),
		StellaHome: stellaHome,
		AgentRoot:  workspace,
		UserRoot:   userRoot,
		Providers:  integrationProviderRegistryBuilder,
	}
}

func TestIntegrationSingleTurn(t *testing.T) {
	cfg := integrationConfig(t)
	r, err := NewGoRunner(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ch := r.Chat(ctx, nil, "Say exactly: hello world")

	var collected string
	for evt := range ch {
		if evt.Err != nil {
			t.Fatalf("stream error: %v", evt.Err)
		}
		collected += evt.Text
	}

	if collected == "" {
		t.Fatal("expected non-empty response")
	}
	lower := strings.ToLower(collected)
	if !strings.Contains(lower, "hello") {
		t.Errorf("response %q does not contain 'hello'", collected)
	}
}

func TestIntegrationMultiTurn(t *testing.T) {
	cfg := integrationConfig(t)
	r, err := NewGoRunner(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Turn 1: establish a fact.
	ch := r.Chat(ctx, nil, "Remember this number: 42. Just say OK.")
	var turn1Text string
	var history []ai.Message
	history = append(history, ai.UserMessage{Content: "Remember this number: 42. Just say OK."})
	for evt := range ch {
		if evt.Err != nil {
			t.Fatalf("turn 1 error: %v", evt.Err)
		}
		turn1Text += evt.Text
		if evt.Store != nil {
			history = append(history, evt.Store)
		}
	}
	if turn1Text == "" {
		t.Fatal("turn 1: expected non-empty response")
	}
	// Append the assistant's text reply to history.
	history = append(history, ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: turn1Text}}})

	// Turn 2: ask about the fact from turn 1.
	ch = r.Chat(ctx, history, "What number did I ask you to remember? Reply with just the number.")
	var turn2Text string
	for evt := range ch {
		if evt.Err != nil {
			t.Fatalf("turn 2 error: %v", evt.Err)
		}
		turn2Text += evt.Text
	}

	if !strings.Contains(turn2Text, "42") {
		t.Errorf("turn 2 response %q does not contain '42' — multi-turn context may be broken", turn2Text)
	}
}

func TestIntegrationSystemPrompt(t *testing.T) {
	cfg := integrationConfig(t)
	cfg.System = "You are a JSON API. Always respond with valid JSON objects only, no other text."
	r, err := NewGoRunner(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ch := r.Chat(ctx, nil, "What is 2+2?")

	var collected strings.Builder
	for evt := range ch {
		if evt.Err != nil {
			t.Fatalf("stream error: %v", evt.Err)
		}
		collected.WriteString(evt.Text)
	}

	trimmed := strings.TrimSpace(collected.String())
	if !strings.HasPrefix(trimmed, "{") {
		t.Errorf("expected JSON response starting with '{', got %q", trimmed)
	}
}

func TestIntegrationCustomBaseURL(t *testing.T) {
	skipWithoutAnthropicKey(t)
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		t.Skip("ANTHROPIC_BASE_URL not set, skipping custom base URL test")
	}

	cfg := integrationConfig(t)
	r, err := NewGoRunner(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ch := r.Chat(ctx, nil, "Say hi")

	var collected strings.Builder
	for evt := range ch {
		if evt.Err != nil {
			t.Fatalf("stream error: %v", evt.Err)
		}
		collected.WriteString(evt.Text)
	}

	if collected.Len() == 0 {
		t.Fatal("expected non-empty response from custom base URL")
	}
}
