package reflect

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

func TestReviewConversationUsesLegacyCombinedPrompt(t *testing.T) {
	fake := memorytest.New()
	wm := newFakeWatermarks()
	var capturedSystem string
	svc := &Service{
		memory: fake,
		wm:     wm,
		log:    testLogger(),
		providers: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			return captureSystemStream(&capturedSystem), nil
		},
	}

	ctx := context.Background()
	freshAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	sess := memory.Session{ID: "s1", AgentID: "a", UserID: "u1"}
	if err := fake.Bootstrap(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := fake.Append(ctx, sess, ai.UserMessage{Content: "fresh learning", Timestamp: freshAt}); err != nil {
		t.Fatal(err)
	}

	err := svc.reviewConversation(ctx, testSnapshot(), reviewTarget{session: sess, privateOneToOne: true})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(capturedSystem, "You are a self-improvement agent") ||
		!strings.Contains(capturedSystem, "## Existing skills") {
		t.Fatalf("expected legacy combined prompt, got %q", capturedSystem)
	}
	if strings.Contains(capturedSystem, "submit_fact_candidate") ||
		strings.Contains(capturedSystem, "submit_skill_candidate") {
		t.Fatalf("scheduled review should not use candidate prompts, got %q", capturedSystem)
	}
	if wm.marks[sess.ID].IsZero() {
		t.Fatal("expected legacy review watermark to advance")
	}
	if len(wm.lineMarks) != 0 {
		t.Fatalf("scheduled review should not set candidate line watermarks, got %#v", wm.lineMarks)
	}
}

func TestReviewConversationCandidatesEntryPointFailsClosedWhenUnwired(t *testing.T) {
	svc, target, wm, _ := newCandidatePipelineTestService(t)

	_, err := svc.reviewConversationCandidates(context.Background(), testSnapshot(), target)
	if err == nil {
		t.Fatal("expected candidate entry point to fail closed until line runners are wired")
	}
	if !wm.lineMark(target.session.ID, reflectLineFact).IsZero() {
		t.Fatal("fact watermark should not advance when candidate line is unwired")
	}
	if !wm.lineMark(target.session.ID, reflectLineSkill).IsZero() {
		t.Fatal("skill watermark should not advance when candidate line is unwired")
	}
}

func captureSystemStream(system *string) providers.StreamFunc {
	return func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		*system = aiCtx.System
		stream := providers.NewChannelEventStream(2)
		stream.Emit(ai.EventTextDelta{Text: "Nothing to save."})
		stream.Finish(nil)
		return stream, nil
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
