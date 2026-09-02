package reflect

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

type scopedReviewHistoryProvider struct {
	memory.Provider
	t        *testing.T
	messages []memory.ReviewMessage
	calls    int
}

func (p *scopedReviewHistoryProvider) LoadReviewHistory(ctx context.Context, _ string) ([]memory.ReviewMessage, error) {
	p.t.Helper()
	if got := authz.UserIDFromContext(ctx); got != "u1" {
		p.t.Fatalf("review user context = %q, want u1", got)
	}
	if got := authz.AgentIDFromContext(ctx); got != "a" {
		p.t.Fatalf("review agent context = %q, want a", got)
	}
	if got := memory.ChangeSourceFromContext(ctx); got != memory.SourceReflect {
		p.t.Fatalf("review change source = %q, want reflect", got)
	}
	p.calls++
	return append([]memory.ReviewMessage(nil), p.messages...), nil
}

func TestReviewConversationStructuredScopesTracedReviewHistory(t *testing.T) {
	at := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	inner := &scopedReviewHistoryProvider{
		Provider: memorytest.New(),
		t:        t,
		messages: []memory.ReviewMessage{{
			ID: "message-1", FirstSeq: 1, LastSeq: 1,
			Message: ai.UserMessage{Content: "Remember that I prefer concise replies.", Timestamp: at},
		}},
	}
	svc := &Service{
		memory: memory.WithTracing(inner, nil),
		wm:     newFakeWatermarks(),
		log:    testLogger(),
		providers: func(string, string, string) (providers.StreamFunc, error) {
			return captureCandidateStreamBySystem(map[string][]ai.ToolCall{
				"fact candidate generator":  {rawToolCall("submit_fact_generation", `{"candidates":[],"no_candidate_reason":"identity regression test"}`)},
				"skill candidate generator": {rawToolCall("submit_skill_generation", `{"candidates":[],"no_candidate_reason":"identity regression test"}`)},
			}), nil
		},
	}
	target := reviewTarget{
		session:         memory.Session{ID: "s1", AgentID: "a", UserID: "u1"},
		privateOneToOne: true,
	}

	if err := svc.reviewConversationStructured(context.Background(), testSnapshot(), target); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 1 {
		t.Fatalf("LoadReviewHistory calls = %d, want 1", inner.calls)
	}
}

func captureCandidateStreamBySystem(callsBySystem map[string][]ai.ToolCall) providers.StreamFunc {
	return func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		for marker, calls := range callsBySystem {
			if !strings.Contains(aiCtx.System, marker) {
				continue
			}
			stream := providers.NewChannelEventStream(16)
			for _, call := range calls {
				stream.Emit(ai.EventToolCallDelta{ID: call.ID, Name: call.Name, Arguments: call.Arguments["raw"].(string)})
			}
			stream.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			stream.Finish(nil)
			return stream, nil
		}
		return nil, fmt.Errorf("unexpected candidate system prompt %q", aiCtx.System)
	}
}

func testSnapshot() *config.Snapshot {
	return &config.Snapshot{
		AgentID:   "a",
		Provider:  "test",
		ModelFast: "test/model-fast",
		APIKey:    "token",
		Providers: map[string]config.ProviderCreds{
			"test": {Type: "test", APIKey: "token"},
		},
	}
}
