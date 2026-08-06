package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	providerapi "github.com/CherryHQ/stella/pkg/providers"
	anthropicprovider "github.com/CherryHQ/stella/plugins/providers/anthropic"
	openaiprovider "github.com/CherryHQ/stella/plugins/providers/openai"
	openairesponseprovider "github.com/CherryHQ/stella/plugins/providers/openai-response"
)

func skipWithoutAPIKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("STELLA_API_KEY")
	if key == "" {
		t.Skip("STELLA_API_KEY not set, skipping integration test")
	}
	return key
}

func TestIntegrationToolUseAllProviders(t *testing.T) {
	skipWithoutAPIKey(t)
	baseURL := os.Getenv("STELLA_BASE_URL")
	if baseURL == "" {
		baseURL = "https://cc2.vaayne.com"
	}

	model := os.Getenv("STELLA_TEST_MODEL")
	if model == "" {
		model = "gpt-5.4"
	}

	type providerCase struct {
		name    string
		baseURL string
	}

	providers := []providerCase{
		{name: "anthropic", baseURL: baseURL},
		{name: "openai", baseURL: baseURL + "/v1"},
		{name: "openai-response", baseURL: baseURL + "/v1"},
	}

	toolDef := ai.ToolDefinition{
		Name:        "get_weather",
		Description: "Get the current weather for a city. Always call this tool when asked about weather.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{
					"type":        "string",
					"description": "The city name to get weather for.",
				},
			},
			"required": []string{"city"},
		},
	}

	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			var adapter providerapi.ProviderAdapter
			switch p.name {
			case "anthropic":
				adapter = anthropicprovider.New(anthropicprovider.Config{BaseURL: p.baseURL})
			case "openai":
				adapter = openaiprovider.New(openaiprovider.Config{BaseURL: p.baseURL})
			case "openai-response":
				adapter = openairesponseprovider.New(openairesponseprovider.Config{BaseURL: p.baseURL})
			default:
				t.Fatalf("unknown provider %s", p.name)
			}
			stream := providerapi.AdapterStreamFunc(adapter)

			var toolCalled atomic.Bool
			var capturedCity string

			tools := agent.ToolSet{
				"get_weather": func(ctx context.Context, call ai.ToolCall) ([]ai.ContentBlock, error) {
					toolCalled.Store(true)
					city, _ := call.Arguments["city"].(string)
					capturedCity = city
					return []ai.ContentBlock{ai.TextContent{Text: fmt.Sprintf("Weather in %s: 22°C, sunny", city)}}, nil
				},
			}

			runner, err := agent.NewRunner(agent.RunnerConfig{
				Stream:          stream,
				Model:           ai.Model{API: p.name, Name: model},
				Tools:           tools,
				ToolDefinitions: []ai.ToolDefinition{toolDef},
			},
				agent.WithSystem("You are a helpful assistant. When asked about weather, always use the get_weather tool. Be concise."),
			)
			if err != nil {
				t.Fatalf("NewRunner error: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			messages := []ai.Message{
				ai.UserMessage{Content: "What's the weather in Tokyo?"},
			}

			history, err := runner.RunWithActiveStart(ctx, messages, 0, nil)
			if err != nil {
				t.Fatalf("runner.RunWithActiveStart error: %v", err)
			}

			// Verify tool was called.
			if !toolCalled.Load() {
				t.Fatal("get_weather tool was never called")
			}

			// Verify arguments were parsed correctly.
			if capturedCity == "" {
				t.Fatal("tool was called but city argument was empty — argument accumulation likely broken")
			}
			if !strings.Contains(strings.ToLower(capturedCity), "tokyo") {
				t.Errorf("expected city to contain 'tokyo', got %q", capturedCity)
			}

			// Verify history has the expected shape: user, assistant(tool_call), tool_result, assistant(text).
			if len(history) < 4 {
				t.Errorf("expected at least 4 messages in history, got %d", len(history))
			}

			// Verify final assistant message has text content.
			lastMsg := history[len(history)-1]
			assistantMsg, ok := lastMsg.(ai.AssistantMessage)
			if !ok {
				t.Fatalf("expected last message to be AssistantMessage, got %T", lastMsg)
			}
			var finalText string
			for _, block := range assistantMsg.Content {
				if tc, ok := block.(ai.TextContent); ok {
					finalText += tc.Text
				}
			}
			if finalText == "" {
				t.Error("final assistant message has no text content")
			}

			// Log for debugging.
			t.Logf("provider=%s city=%q final_text=%q history_len=%d",
				p.name, capturedCity, truncate(finalText, 100), len(history))
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
