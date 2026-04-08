package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	pluginproviders "github.com/vaayne/anna/plugins/providers"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

func skipWithoutAnthropicKey(t *testing.T) {
	t.Helper()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping integration test")
	}
}

func TestIntegrationPoolWithGoRunner(t *testing.T) {
	skipWithoutAnthropicKey(t)

	model := os.Getenv("ANNA_GO_MODEL")
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}

	factory := func(ctx context.Context, _ runner.RunnerParams) (runner.Runner, error) {
		return runner.NewGoRunner(ctx, runner.GoRunnerConfig{
			API:       "anthropic",
			Model:     model,
			APIKey:    os.Getenv("ANTHROPIC_API_KEY"),
			BaseURL:   os.Getenv("ANTHROPIC_BASE_URL"),
			CoreTools: integrationCoreToolsBuilder,
			Providers: integrationProviderRegistryBuilder,
		})
	}

	pool := NewPool(factory, testMemoryProvider(t))
	defer func() { _ = pool.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sessionID := "integration-test"

	// Turn 1: establish context.
	stream := pool.Chat(ctx, sessionID, "My favorite color is blue. Just say OK.")
	var turn1 string
	for evt := range stream {
		if evt.Err != nil {
			t.Fatalf("turn 1 error: %v", evt.Err)
		}
		turn1 += evt.Text
	}
	if turn1 == "" {
		t.Fatal("turn 1: expected non-empty response")
	}

	// Verify history accumulated via pool.History().
	history := pool.History(sessionID)
	// At minimum: 1 user_message + 1 assistant_message.
	if len(history) < 2 {
		t.Errorf("history length = %d, want >= 2", len(history))
	}

	// Turn 2: reference prior context.
	stream = pool.Chat(ctx, sessionID, "What is my favorite color? Reply with just the color.")
	var turn2 string
	for evt := range stream {
		if evt.Err != nil {
			t.Fatalf("turn 2 error: %v", evt.Err)
		}
		turn2 += evt.Text
	}

	lower := strings.ToLower(turn2)
	if !strings.Contains(lower, "blue") {
		t.Errorf("turn 2 response %q does not contain 'blue' — pool history may not be working", turn2)
	}
}

func integrationProviderRegistryBuilder(api, apiKey, baseURL string) (*providers.Registry, error) {
	return pluginproviders.BuildRegistry(api, pluginproviders.ProviderConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
	})
}

func integrationCoreToolsBuilder(plugintools.BuildContext) []tools.Tool { return nil }
