package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/providers"
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
		{name: "leading system prefix is ignored", content: []ai.ContentBlock{ai.TextContent{Text: "[System: Be concise]"}, ai.TextContent{Text: "取消"}}, want: "取消", ok: true},
		{name: "multiple prefixes then user text", content: []ai.ContentBlock{ai.TextContent{Text: "[System: one]"}, ai.TextContent{Text: "[System: two]"}, ai.TextContent{Text: "新会话"}}, want: "新会话", ok: true},
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

func TestIsIntentSystemPrefix(t *testing.T) {
	if !isIntentSystemPrefix(ai.TextContent{Text: "[System: Be concise]"}) {
		t.Fatal("expected system prefix to be detected")
	}
	if isIntentSystemPrefix(ai.TextContent{Text: "[system: lowercase]"}) {
		t.Fatal("did not expect lowercase prefix to match")
	}
	if isIntentSystemPrefix(ai.TextContent{Text: "cancel"}) {
		t.Fatal("did not expect regular user text to match")
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
		func(_ context.Context, providerType string, creds config.ProviderCreds) (providers.StreamFunc, error) {
			if providerType != "openai" {
				t.Fatalf("providerType = %q, want openai", providerType)
			}
			if creds.APIKey != "k" {
				t.Fatalf("API key = %q, want k", creds.APIKey)
			}
			return stubStreamFunc, nil
		},
	)
	classifier.complete = func(_ context.Context, model ai.Model, ctx ai.Context, _ ai.CompleteOptions, _ providers.StreamFunc) (ai.AssistantMessage, error) {
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
		func(_ context.Context, providerType string, creds config.ProviderCreds) (providers.StreamFunc, error) {
			if providerType != "openai" {
				t.Fatalf("providerType = %q, want openai alias", providerType)
			}
			if creds.Type != "primary" || creds.APIKey != "" {
				t.Fatalf("creds = %#v", creds)
			}
			return stubStreamFunc, nil
		},
	)
	classifier.complete = func(_ context.Context, model ai.Model, _ ai.Context, _ ai.CompleteOptions, _ providers.StreamFunc) (ai.AssistantMessage, error) {
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
		func(context.Context, string, config.ProviderCreds) (providers.StreamFunc, error) {
			called = true
			return stubStreamFunc, nil
		},
	)
	classifier.complete = func(context.Context, ai.Model, ai.Context, ai.CompleteOptions, providers.StreamFunc) (ai.AssistantMessage, error) {
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
		func(context.Context, string, config.ProviderCreds) (providers.StreamFunc, error) {
			return stubStreamFunc, nil
		},
	)
	classifier.complete = func(context.Context, ai.Model, ai.Context, ai.CompleteOptions, providers.StreamFunc) (ai.AssistantMessage, error) {
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
		func(context.Context, string, config.ProviderCreds) (providers.StreamFunc, error) {
			return stubStreamFunc, nil
		},
	)
	classifier.timeout = 20 * time.Millisecond
	classifier.complete = func(ctx context.Context, _ ai.Model, _ ai.Context, _ ai.CompleteOptions, _ providers.StreamFunc) (ai.AssistantMessage, error) {
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

// TestCoordinatorNewIntentFallsThroughToAgentTurn pins the split between typed
// commands and guessed intent: `/new` is consent, a short phrase is not. A
// classified "new" must reach the agent, which asks before resetting anything.
func TestCoordinatorNewIntentFallsThroughToAgentTurn(t *testing.T) {
	ctx := context.Background()
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}

	coordinator := &Coordinator{queue: newSessionQueue(), intentClassifier: stubIntentClassifier(IntentNew)}
	resp, handled, _, _ := coordinator.handleResolvedIncoming(ctx, rc, incomingText("新会话"), "", "")
	if handled || resp != "" {
		t.Fatalf("a classified new intent must not be answered by the coordinator: resp=%q handled=%v", resp, handled)
	}

	after, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if after.ID != before.ID {
		t.Fatal("a classified new intent must not rotate the session on its own")
	}
}

// TestCoordinatorCompactIntentStillFastPaths: compaction is non-destructive, so
// guessing it wrong costs nothing and it keeps its shortcut.
func TestCoordinatorCompactIntentStillFastPaths(t *testing.T) {
	ctx := context.Background()
	rc := newCompactTestChat(t, "", auth.User{ID: "user-1", Role: auth.RoleUser})
	before, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}

	coordinator := &Coordinator{queue: newSessionQueue(), intentClassifier: stubIntentClassifier(IntentCompact), agentAccess: newRotationAgentAccess(true)}
	resp, handled, stream, err := coordinator.handleResolvedIncoming(ctx, rc, incomingText("压缩会话"), "", "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !handled || stream != nil || resp != "Session compacted." {
		t.Fatalf("compact intent should still fast-path: resp=%q handled=%v stream=%#v", resp, handled, stream)
	}

	after, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession after compact: %v", err)
	}
	if after.ID != before.ID {
		t.Fatal("compaction must keep the same session")
	}
}

// TestIntentPromptKeepsNewAndCompactDistinct guards the classifier prompt against
// drifting back to "new: same as compact", which is what made `/new` a lie.
func TestIntentPromptKeepsNewAndCompactDistinct(t *testing.T) {
	if strings.Contains(intentClassifierPrompt, "same as") {
		t.Fatal("the prompt must not equate new and compact")
	}
	for _, phrase := range []string{`"新会话" -> {"action":"new"}`, `"压缩会话" -> {"action":"compact"}`, `"start over" -> {"action":"new"}`} {
		if !strings.Contains(intentClassifierPrompt, phrase) {
			t.Fatalf("the prompt lost its bilingual example %q", phrase)
		}
	}
}

var stubStreamFunc providers.StreamFunc = func(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, nil
}

type stubIntentClassifier Intent

func (s stubIntentClassifier) Classify(context.Context, string, []ai.ContentBlock) Intent {
	return Intent(s)
}

func incomingText(text string) pkgchannel.IncomingMessage {
	return pkgchannel.IncomingMessage{Content: []ai.ContentBlock{ai.TextContent{Text: text}}}
}
