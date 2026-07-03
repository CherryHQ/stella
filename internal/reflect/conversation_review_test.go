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
	if strings.Contains(capturedSystem, "submit_fact_generation") ||
		strings.Contains(capturedSystem, "submit_skill_generation") {
		t.Fatalf("scheduled review should not use candidate prompts, got %q", capturedSystem)
	}
	if wm.marks[sess.ID].IsZero() {
		t.Fatal("expected legacy review watermark to advance")
	}
	if len(wm.lineMarks) != 0 {
		t.Fatalf("scheduled review should not set candidate line watermarks, got %#v", wm.lineMarks)
	}
}

func TestReviewConversationCandidatesEntryPointUsesCandidateRunners(t *testing.T) {
	svc, target, wm, freshAt := newCandidatePipelineTestService(t)
	stream := sequentialCaptureStream(t,
		[]ai.ToolCall{
			rawToolCall("submit_fact_generation", `{"candidates":[{
				"subject":"world",
				"content":"Candidate reflect uses generated candidates before reconciliation.",
				"evidence":[{"source_type":"user_message","source":"[user] candidate reflect before reconciliation","reason":"The user stated the architecture boundary."}],
				"expected_effect":"Future reflect implementation keeps #532 separate from #531 writes.",
				"handoff_hints":{"knowledge_search_query_hint":"candidate reflect reconciliation boundary"}
			}]}`),
		},
		[]ai.ToolCall{
			rawToolCall("submit_fact_evaluations", `{"evaluations":[{
				"candidate_ref":"fact-0001",
				"scores":{"evidence_strength":4,"subject_fit":4,"durability":4,"future_utility":4,"atomicity":4},
				"rationale":"clear"
			}]}`),
		},
		[]ai.ToolCall{
			rawToolCall("submit_skill_generation", `{"candidates":[{
				"learning":{"summary":"Keep candidate generation separate from writes.","reusable_delta":"This avoids replacing old reflect before reconciliation exists."},
				"evidence":[{"signal_type":"explicit_instruction","source":"[user] complete #532 before replacing old reflect","reason":"The instruction affects reflect implementation workflow."}],
				"applicability":{"trigger_examples":["Changing reflect architecture"],"non_trigger_examples":["One-off answer"]},
				"procedure":{"steps":["Generate candidates","Evaluate candidates","Hand accepted candidates to #531"],"verification":["Run reflect tests"]},
				"handoff_hints":{"search_query_hint":"reflect candidate generation write boundary"}
			}]}`),
		},
		[]ai.ToolCall{
			rawToolCall("submit_skill_evaluations", `{"evaluations":[{
				"candidate_ref":"skill-0001",
				"scores":{"evidence_strength":4,"reusable_value":4,"baseline_separation":4,"procedure_actionability":4,"applicability_clarity":3,"verification_quality":3},
				"rationale":"clear"
			}]}`),
		},
	)
	svc.providers = func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
		return stream, nil
	}

	result, err := svc.reviewConversationCandidates(context.Background(), testSnapshot(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FactAccepted) != 1 || len(result.SkillAccepted) != 1 {
		t.Fatalf("expected accepted fact and skill candidates, got %#v", result)
	}
	if !wm.lineMark(target.session.ID, reflectLineFact).Equal(freshAt) {
		t.Fatal("fact watermark should advance after candidate review")
	}
	if !wm.lineMark(target.session.ID, reflectLineSkill).Equal(freshAt) {
		t.Fatal("skill watermark should advance after candidate review")
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
