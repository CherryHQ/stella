package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/ai"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/providers"
)

func TestClassifyCandidateText(t *testing.T) {
	tests := []struct {
		name    string
		content []ai.ContentBlock
		want    string
		ok      bool
	}{
		{name: "short text", content: []ai.ContentBlock{ai.TextContent{Text: "cancel"}}, want: "cancel", ok: true},
		{name: "trims whitespace", content: []ai.ContentBlock{ai.TextContent{Text: "  帮助  "}}, want: "帮助", ok: true},
		{name: "multiple blocks", content: []ai.ContentBlock{ai.TextContent{Text: "cancel"}, ai.TextContent{Text: "now"}}, ok: false},
		{name: "image block", content: []ai.ContentBlock{ai.ImageContent{Data: "x", MimeType: "image/png"}}, ok: false},
		{name: "newline rejected", content: []ai.ContentBlock{ai.TextContent{Text: "cancel\nplease"}}, ok: false},
		{name: "too many words", content: []ai.ContentBlock{ai.TextContent{Text: "please can you start a brand new chat"}}, ok: false},
		{name: "too many runes", content: []ai.ContentBlock{ai.TextContent{Text: "this message is definitely longer than the short intent gate allows"}}, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := classifyCandidateText(tt.content)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("text = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseIntentResponse(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Intent
		ok   bool
	}{
		{name: "json", raw: `{"action":"help"}`, want: IntentHelp, ok: true},
		{name: "bare token", raw: `abort`, want: IntentAbort, ok: true},
		{name: "fenced json", raw: "```json\n{\"action\":\"compact\"}\n```", want: IntentCompact, ok: true},
		{name: "invalid", raw: `{"action":"other"}`, want: IntentNone, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIntentResponse(tt.raw)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, ok = %v", err, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("intent = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLLMIntentClassifierClassify(t *testing.T) {
	classifier := NewLLMIntentClassifier(
		func(context.Context, string) (*config.Snapshot, error) {
			return &config.Snapshot{
				Provider:  "demo",
				ModelFast: "demo/fast-model",
				Providers: map[string]config.ProviderCreds{"demo": {Type: "openai", APIKey: "k", BaseURL: "https://example.com"}},
			}, nil
		},
		func(_ context.Context, providerType string, creds config.ProviderCreds) (providers.ProviderGetter, error) {
			if providerType != "openai" {
				t.Fatalf("providerType = %q, want openai", providerType)
			}
			if creds.APIKey != "k" {
				t.Fatalf("API key = %q, want k", creds.APIKey)
			}
			return stubProviderGetter{}, nil
		},
	)
	classifier.complete = func(_ context.Context, model ai.Model, ctx ai.Context, _ ai.CompleteOptions, _ providers.ProviderGetter) (ai.AssistantMessage, error) {
		if model.ID != "fast-model" || model.API != "openai" {
			t.Fatalf("model = %#v", model)
		}
		if len(ctx.Messages) != 1 {
			t.Fatalf("messages len = %d, want 1", len(ctx.Messages))
		}
		return ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: `{"action":"new"}`}}}, nil
	}

	got := classifier.Classify(context.Background(), "agent-1", []ai.ContentBlock{ai.TextContent{Text: "新会话"}})
	if got != IntentNew {
		t.Fatalf("intent = %q, want %q", got, IntentNew)
	}
}

func TestLLMIntentClassifierUsesProviderAliasCredsType(t *testing.T) {
	classifier := NewLLMIntentClassifier(
		func(context.Context, string) (*config.Snapshot, error) {
			return &config.Snapshot{
				Provider:  "primary",
				ModelFast: "openai/cheap-model",
				Providers: map[string]config.ProviderCreds{
					"primary": {Type: "openai", APIKey: "primary-key"},
				},
			}, nil
		},
		func(_ context.Context, providerType string, creds config.ProviderCreds) (providers.ProviderGetter, error) {
			if providerType != "openai" {
				t.Fatalf("providerType = %q, want openai alias", providerType)
			}
			if creds.Type != "primary" || creds.APIKey != "" {
				t.Fatalf("creds = %#v", creds)
			}
			return stubProviderGetter{}, nil
		},
	)
	classifier.complete = func(_ context.Context, model ai.Model, _ ai.Context, _ ai.CompleteOptions, _ providers.ProviderGetter) (ai.AssistantMessage, error) {
		if model.Provider != "openai" || model.API != "openai" {
			t.Fatalf("model = %#v", model)
		}
		return ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: `{"action":"help"}`}}}, nil
	}

	got := classifier.Classify(context.Background(), "agent-1", []ai.ContentBlock{ai.TextContent{Text: "help"}})
	if got != IntentHelp {
		t.Fatalf("intent = %q, want %q", got, IntentHelp)
	}
}

func TestLLMIntentClassifierSkipsWhenModelFastUnset(t *testing.T) {
	called := false
	classifier := NewLLMIntentClassifier(
		func(context.Context, string) (*config.Snapshot, error) {
			return &config.Snapshot{Provider: "demo", Model: "demo/strong-model", Providers: map[string]config.ProviderCreds{"demo": {Type: "openai", APIKey: "k"}}}, nil
		},
		func(context.Context, string, config.ProviderCreds) (providers.ProviderGetter, error) {
			called = true
			return stubProviderGetter{}, nil
		},
	)
	classifier.complete = func(context.Context, ai.Model, ai.Context, ai.CompleteOptions, providers.ProviderGetter) (ai.AssistantMessage, error) {
		called = true
		return ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: `{"action":"help"}`}}}, nil
	}

	got := classifier.Classify(context.Background(), "agent-1", []ai.ContentBlock{ai.TextContent{Text: "help"}})
	if got != IntentNone {
		t.Fatalf("intent = %q, want %q", got, IntentNone)
	}
	if called {
		t.Fatal("expected classifier to skip provider/model work when model_fast is unset")
	}
}

func TestLLMIntentClassifierFallsBackToNone(t *testing.T) {
	classifier := NewLLMIntentClassifier(
		func(context.Context, string) (*config.Snapshot, error) {
			return &config.Snapshot{Provider: "demo", ModelFast: "demo/fast-model", Providers: map[string]config.ProviderCreds{"demo": {Type: "openai", APIKey: "k"}}}, nil
		},
		func(context.Context, string, config.ProviderCreds) (providers.ProviderGetter, error) {
			return stubProviderGetter{}, nil
		},
	)
	classifier.complete = func(context.Context, ai.Model, ai.Context, ai.CompleteOptions, providers.ProviderGetter) (ai.AssistantMessage, error) {
		return ai.AssistantMessage{}, errors.New("boom")
	}

	got := classifier.Classify(context.Background(), "agent-1", []ai.ContentBlock{ai.TextContent{Text: "cancel"}})
	if got != IntentNone {
		t.Fatalf("intent = %q, want %q", got, IntentNone)
	}
}

func TestLLMIntentClassifierTimeoutFallsBackToNone(t *testing.T) {
	classifier := NewLLMIntentClassifier(
		func(context.Context, string) (*config.Snapshot, error) {
			return &config.Snapshot{Provider: "demo", ModelFast: "demo/fast-model", Providers: map[string]config.ProviderCreds{"demo": {Type: "openai", APIKey: "k"}}}, nil
		},
		func(context.Context, string, config.ProviderCreds) (providers.ProviderGetter, error) {
			return stubProviderGetter{}, nil
		},
	)
	classifier.timeout = 20 * time.Millisecond
	classifier.complete = func(ctx context.Context, _ ai.Model, _ ai.Context, _ ai.CompleteOptions, _ providers.ProviderGetter) (ai.AssistantMessage, error) {
		<-ctx.Done()
		return ai.AssistantMessage{}, ctx.Err()
	}

	start := time.Now()
	got := classifier.Classify(context.Background(), "agent-1", []ai.ContentBlock{ai.TextContent{Text: "help"}})
	if got != IntentNone {
		t.Fatalf("intent = %q, want %q", got, IntentNone)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("classification exceeded fallback budget")
	}
}

func TestCoordinatorHandleResolvedIncomingIntentRouting(t *testing.T) {
	coordinator := &Coordinator{queue: newSessionQueue(), intentClassifier: stubIntentClassifier(IntentHelp)}
	resp, handled, stream, err := coordinator.handleResolvedIncoming(context.Background(), &ResolvedChat{SessionKey: "sess-1", AgentID: "agent-1"}, incomingText("帮助"), "", "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !handled || stream != nil {
		t.Fatalf("handled = %v stream = %#v", handled, stream)
	}
	if resp == "" {
		t.Fatal("expected help response")
	}
}

func TestCoordinatorHandleResolvedIncomingAbortIntent(t *testing.T) {
	coordinator := &Coordinator{queue: newSessionQueue(), intentClassifier: stubIntentClassifier(IntentAbort)}
	slot := coordinator.queue.getOrCreate("sess-1")
	slot.mu.Lock()
	slot.activeCancel = func() {}
	slot.mu.Unlock()
	defer coordinator.queue.release(slot)

	resp, handled, stream, err := coordinator.handleResolvedIncoming(context.Background(), &ResolvedChat{SessionKey: "sess-1", AgentID: "agent-1"}, incomingText("取消"), "", "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !handled || stream != nil {
		t.Fatalf("handled = %v stream = %#v", handled, stream)
	}
	if resp != "Aborted." {
		t.Fatalf("resp = %q, want %q", resp, "Aborted.")
	}
}

type stubProviderGetter struct{}

func (stubProviderGetter) Get(string) (providers.ProviderAdapter, bool) { return nil, false }

type stubIntentClassifier Intent

func (s stubIntentClassifier) Classify(context.Context, string, []ai.ContentBlock) Intent {
	return Intent(s)
}

func incomingText(text string) pkgchannel.IncomingMessage {
	return pkgchannel.IncomingMessage{Content: []ai.ContentBlock{ai.TextContent{Text: text}}}
}
