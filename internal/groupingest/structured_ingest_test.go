package groupingest_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/groupingest"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/providers"
)

const (
	groupGenerationTool     = "submit_group_fact_generation"
	groupEvaluationTool     = "submit_group_fact_evaluations"
	groupReconciliationTool = "submit_group_fact_reconciliation"
)

type scriptedGroupResponse struct {
	toolName string
	raw      string
	err      error
	wait     bool
}

func TestStructuredGroupReflectZeroCandidatesCheckpointsWindow(t *testing.T) {
	db, q := openTestDB(t)
	store := eventlog.NewStore(db)
	message := appendHuman(t, store, "g-zero", "alice", "The ticket is currently being investigated.")
	groupID := resolveGroup(t, store, "test", "g-zero", "")
	stream, calls := scriptedGroupStream(t, scriptedGroupResponse{
		toolName: groupGenerationTool,
		raw:      `{"candidates":[],"no_candidate_reason":"Only temporary ticket status was discussed."}`,
	})
	ing := newStructuredIngester(t, db, q, stream, groupingest.StructuredConfig{})

	if err := ing.ProcessGroup(context.Background(), groupID); err != nil {
		t.Fatalf("process group: %v", err)
	}
	assertGroupReflectCursor(t, q, groupID, message.Seq)
	facts, err := q.ListActiveGroupFacts(context.Background(), groupID)
	if err != nil {
		t.Fatalf("list active facts: %v", err)
	}
	if len(facts) != 0 || calls.Load() != 1 {
		t.Fatalf("facts=%d provider_calls=%d, want 0 and 1", len(facts), calls.Load())
	}
}

func TestStructuredGroupReflectCreatesAcceptedFact(t *testing.T) {
	db, q := openTestDB(t)
	store := eventlog.NewStore(db)
	message := appendHuman(t, store, "g-create", "alice", "All production releases permanently require two approvals.")
	groupID := resolveGroup(t, store, "test", "g-create", "")
	stream, calls := scriptedGroupStream(t,
		scriptedGroupResponse{
			toolName: groupGenerationTool,
			raw: `{"candidates":[{
				"subject":"group",
				"content":"Production releases require two approvals.",
				"evidence":[{"source":"All production releases permanently require two approvals.","reason":"A human explicitly stated a durable group rule."}],
				"expected_effect":"Future release coordination consistently applies the approval rule."
			}]}`,
		},
		scriptedGroupResponse{
			toolName: groupEvaluationTool,
			raw: `{"evaluations":[{
				"candidate_ref":"group-fact-0001",
				"scores":{"evidence_strength":4,"subject_fit":4,"durability":4,"future_utility":4,"atomicity":4},
				"rationale":"Explicit, durable, group-scoped collaboration rule."
			}]}`,
		},
		scriptedGroupResponse{
			toolName: groupReconciliationTool,
			raw: `{"operations":[{
				"operation":"create",
				"candidate_refs":["group-fact-0001"],
				"target_fact_ids":[],
				"new_content":"Production releases require two approvals.",
				"rationale":"No equivalent active Group Fact exists."
			}]}`,
		},
	)
	ing := newStructuredIngester(t, db, q, stream, groupingest.StructuredConfig{})

	if err := ing.ProcessGroup(context.Background(), groupID); err != nil {
		t.Fatalf("process group: %v", err)
	}
	assertGroupReflectCursor(t, q, groupID, message.Seq)
	facts, err := q.ListActiveGroupFacts(context.Background(), groupID)
	if err != nil {
		t.Fatalf("list active facts: %v", err)
	}
	if len(facts) != 1 || facts[0].Content != "Production releases require two approvals." {
		t.Fatalf("active facts = %#v", facts)
	}
	version, err := q.GetGroupMemoryVersion(context.Background(), groupID)
	if err != nil {
		t.Fatalf("get group version: %v", err)
	}
	logs, err := q.ListGroupFactChangelog(context.Background(), sqlc.ListGroupFactChangelogParams{
		GroupID:    groupID,
		LimitCount: 10,
	})
	if err != nil {
		t.Fatalf("list changelog: %v", err)
	}
	if version != 1 || len(logs) != 1 || calls.Load() != 3 {
		t.Fatalf("version=%d changelog=%d calls=%d, want 1, 1, 3", version, len(logs), calls.Load())
	}
}

func TestStructuredGroupReflectRejectedCandidateSkipsReconciliation(t *testing.T) {
	db, q := openTestDB(t)
	store := eventlog.NewStore(db)
	message := appendHuman(t, store, "g-reject", "alice", "We might use staging tomorrow.")
	groupID := resolveGroup(t, store, "test", "g-reject", "")
	stream, calls := scriptedGroupStream(t,
		scriptedGroupResponse{
			toolName: groupGenerationTool,
			raw: `{"candidates":[{
				"subject":"group",
				"content":"The group uses staging.",
				"evidence":[{"source":"We might use staging tomorrow.","reason":"A tentative statement mentions staging."}],
				"expected_effect":"May inform a future environment choice."
			}]}`,
		},
		scriptedGroupResponse{
			toolName: groupEvaluationTool,
			raw: `{"evaluations":[{
				"candidate_ref":"group-fact-0001",
				"scores":{"evidence_strength":3,"subject_fit":3,"durability":3,"future_utility":3,"atomicity":3},
				"rationale":"The statement is tentative and below the acceptance threshold."
			}]}`,
		},
	)
	ing := newStructuredIngester(t, db, q, stream, groupingest.StructuredConfig{})

	if err := ing.ProcessGroup(context.Background(), groupID); err != nil {
		t.Fatalf("process group: %v", err)
	}
	assertGroupReflectCursor(t, q, groupID, message.Seq)
	facts, err := q.ListActiveGroupFacts(context.Background(), groupID)
	if err != nil {
		t.Fatalf("list active facts: %v", err)
	}
	if len(facts) != 0 || calls.Load() != 2 {
		t.Fatalf("facts=%d provider_calls=%d, want 0 and 2", len(facts), calls.Load())
	}
}

func TestStructuredGroupReflectFailureDoesNotAdvanceCursor(t *testing.T) {
	db, q := openTestDB(t)
	store := eventlog.NewStore(db)
	appendHuman(t, store, "g-fail", "alice", "Production releases require two approvals.")
	groupID := resolveGroup(t, store, "test", "g-fail", "")
	stream, _ := scriptedGroupStream(t,
		scriptedGroupResponse{
			toolName: groupGenerationTool,
			raw: `{"candidates":[{
				"subject":"group",
				"content":"Production releases require two approvals.",
				"evidence":[{"source":"Production releases require two approvals.","reason":"Explicit durable rule."}],
				"expected_effect":"Applies the rule in future release work."
			}]}`,
		},
		scriptedGroupResponse{
			toolName: groupEvaluationTool,
			raw: `{"evaluations":[{
				"candidate_ref":"group-fact-0001",
				"scores":{"evidence_strength":4,"subject_fit":4,"durability":4,"future_utility":4,"atomicity":4},
				"rationale":"Durable group rule."
			}]}`,
		},
		scriptedGroupResponse{err: errors.New("reconciliation unavailable")},
	)
	ing := newStructuredIngester(t, db, q, stream, groupingest.StructuredConfig{})

	if err := ing.ProcessGroup(context.Background(), groupID); err == nil {
		t.Fatal("expected reconciliation failure")
	}
	assertNoGroupReflectCursor(t, q, groupID)
}

func TestStructuredGroupReflectSkipsOversizedEventAndContinues(t *testing.T) {
	db, q := openTestDB(t)
	store := eventlog.NewStore(db)
	oversized := appendHuman(t, store, "g-oversized", "alice", strings.Repeat("long article ", 20))
	last := appendHuman(t, store, "g-oversized", "bob", "ok")
	groupID := resolveGroup(t, store, "test", "g-oversized", "")
	stream, _ := scriptedGroupStream(t, scriptedGroupResponse{
		toolName: groupGenerationTool,
		raw:      `{"candidates":[],"no_candidate_reason":"No durable collaboration fact."}`,
	})
	ing := newStructuredIngester(t, db, q, stream, groupingest.StructuredConfig{
		FreshTokenBudget: 2,
		PriorTokenBudget: 2,
	})

	if err := ing.ProcessGroup(context.Background(), groupID); err != nil {
		t.Fatalf("process group: %v", err)
	}
	assertGroupReflectCursor(t, q, groupID, last.Seq)
	isError, err := q.IsIngestError(context.Background(), sqlc.IsIngestErrorParams{
		GroupID:  groupID,
		Pipeline: groupingest.PipelineGroupReflect,
		Seq:      oversized.Seq,
	})
	if err != nil {
		t.Fatalf("check ingest error: %v", err)
	}
	if !isError {
		t.Fatal("oversized event was not recorded in the dead-letter table")
	}
}

func TestStructuredGroupReflectCheckpointsOnlySkippedEventsWithoutLLM(t *testing.T) {
	db, q := openTestDB(t)
	store := eventlog.NewStore(db)
	oversized := appendHuman(t, store, "g-only-oversized", "alice", strings.Repeat("long article ", 20))
	groupID := resolveGroup(t, store, "test", "g-only-oversized", "")
	var calls atomic.Int32
	stream := func(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
		calls.Add(1)
		return nil, errors.New("LLM must not run without fresh evidence")
	}
	ing := newStructuredIngester(t, db, q, stream, groupingest.StructuredConfig{
		FreshTokenBudget: 2,
		PriorTokenBudget: 2,
	})

	if err := ing.ProcessGroup(context.Background(), groupID); err != nil {
		t.Fatalf("process group: %v", err)
	}
	assertGroupReflectCursor(t, q, groupID, oversized.Seq)
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", calls.Load())
	}
}

func TestStructuredGroupReflectRunOnceIsolatesGroupFailure(t *testing.T) {
	db, q := openTestDB(t)
	store := eventlog.NewStore(db)
	appendHuman(t, store, "g-bad", "alice", "FAIL THIS GROUP")
	good := appendHuman(t, store, "g-good", "bob", "Only a temporary status.")
	badID := resolveGroup(t, store, "test", "g-bad", "")
	goodID := resolveGroup(t, store, "test", "g-good", "")
	stream := func(ctx context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		input := groupStreamInput(aiCtx)
		if strings.Contains(input, "FAIL THIS GROUP") {
			return nil, errors.New("group-specific provider failure")
		}
		return completedGroupToolStream(groupGenerationTool, `{"candidates":[],"no_candidate_reason":"No durable collaboration fact."}`), nil
	}
	ing := newStructuredIngester(t, db, q, stream, groupingest.StructuredConfig{})

	if err := ing.RunOnce(context.Background()); err == nil {
		t.Fatal("expected the run to report the isolated group failure")
	}
	assertNoGroupReflectCursor(t, q, badID)
	assertGroupReflectCursor(t, q, goodID, good.Seq)
}

func TestStructuredGroupReflectPendingGroupsRotateByOldestCursor(t *testing.T) {
	db, q := openTestDB(t)
	store := eventlog.NewStore(db)
	appendHuman(t, store, "g-never", "alice", "pending without a cursor")
	appendHuman(t, store, "g-oldest", "bob", "pending with the oldest cursor")
	appendHuman(t, store, "g-newer", "carol", "pending with a newer cursor")
	neverID := resolveGroup(t, store, "test", "g-never", "")
	oldestID := resolveGroup(t, store, "test", "g-oldest", "")
	newerID := resolveGroup(t, store, "test", "g-newer", "")
	ctx := context.Background()

	for _, groupID := range []string{oldestID, newerID} {
		if err := q.UpsertIngestCursor(ctx, sqlc.UpsertIngestCursorParams{
			GroupID:  groupID,
			Pipeline: groupingest.PipelineGroupReflect,
			LastSeq:  0,
		}); err != nil {
			t.Fatalf("seed cursor for %s: %v", groupID, err)
		}
	}
	if _, err := db.Exec(ctx, `
		UPDATE ctx_group_ingest_cursor
		SET updated_at = CASE group_id
			WHEN $1 THEN '2026-01-01T00:00:00Z'::timestamptz
			WHEN $2 THEN '2026-01-02T00:00:00Z'::timestamptz
		END
		WHERE pipeline = $3 AND group_id IN ($1, $2)
	`, oldestID, newerID, groupingest.PipelineGroupReflect); err != nil {
		t.Fatalf("set cursor ages: %v", err)
	}

	assertPendingGroupOrder(t, q, []string{neverID, oldestID, newerID})

	// A successful window refreshes updated_at, so this previously unprocessed
	// group moves behind groups that have waited longer on the next run.
	if err := q.UpsertIngestCursor(ctx, sqlc.UpsertIngestCursorParams{
		GroupID:  neverID,
		Pipeline: groupingest.PipelineGroupReflect,
		LastSeq:  0,
	}); err != nil {
		t.Fatalf("create cursor for previously unprocessed group: %v", err)
	}
	assertPendingGroupOrder(t, q, []string{oldestID, newerID, neverID})
}

func TestStructuredGroupReflectWindowTimeoutDoesNotAdvanceCursor(t *testing.T) {
	db, q := openTestDB(t)
	store := eventlog.NewStore(db)
	appendHuman(t, store, "g-timeout", "alice", "A durable statement.")
	groupID := resolveGroup(t, store, "test", "g-timeout", "")
	stream, _ := scriptedGroupStream(t, scriptedGroupResponse{wait: true})
	ing := newStructuredIngester(t, db, q, stream, groupingest.StructuredConfig{
		WindowTimeout: 20 * time.Millisecond,
	})

	if err := ing.ProcessGroup(context.Background(), groupID); err == nil {
		t.Fatal("expected window timeout")
	}
	assertNoGroupReflectCursor(t, q, groupID)
}

func TestStructuredGroupReflectSoftBudgetStopsBeforeNextWindow(t *testing.T) {
	db, q := openTestDB(t)
	store := eventlog.NewStore(db)
	first := appendHuman(t, store, "g-budget", "alice", "one")
	appendHuman(t, store, "g-budget", "bob", "two")
	groupID := resolveGroup(t, store, "test", "g-budget", "")
	now := time.Unix(1_700_000_000, 0)
	var nowMu sync.Mutex
	stream, calls := scriptedGroupStream(t, scriptedGroupResponse{
		toolName: groupGenerationTool,
		raw:      `{"candidates":[],"no_candidate_reason":"No durable collaboration fact."}`,
	})
	ing := newStructuredIngester(t, db, q, stream, groupingest.StructuredConfig{
		FreshTokenBudget: 1,
		PriorTokenBudget: 1,
		RunSoftBudget:    30 * time.Minute,
		Now: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return now
		},
		Reviewer: groupingest.CandidateReviewer{
			Stream: stream,
			Model:  ai.Model{ID: "test-model"},
			OnGenerated: func(int) {
				nowMu.Lock()
				now = now.Add(31 * time.Minute)
				nowMu.Unlock()
			},
		},
	})

	if err := ing.ProcessGroup(context.Background(), groupID); err != nil {
		t.Fatalf("process group: %v", err)
	}
	assertGroupReflectCursor(t, q, groupID, first.Seq)
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", calls.Load())
	}
}

func newStructuredIngester(
	t *testing.T,
	db *pgxpool.Pool,
	q *sqlc.Queries,
	stream providers.StreamFunc,
	overrides groupingest.StructuredConfig,
) *groupingest.StructuredIngester {
	t.Helper()
	provider, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new LCM provider: %v", err)
	}
	overrides.DB = db
	overrides.Q = q
	overrides.FactStore = provider
	if overrides.Reviewer.Stream == nil {
		overrides.Reviewer = groupingest.CandidateReviewer{
			Stream: stream,
			Model:  ai.Model{ID: "test-model"},
		}
	}
	overrides.Reconciler = groupingest.ReconciliationRunner{
		Stream: stream,
		Model:  ai.Model{ID: "test-model"},
	}
	ing, err := groupingest.NewStructured(overrides)
	if err != nil {
		t.Fatalf("new structured ingester: %v", err)
	}
	return ing
}

func assertPendingGroupOrder(t *testing.T, q *sqlc.Queries, want []string) {
	t.Helper()
	got, err := q.ListGroupsWithPendingIngest(context.Background(), groupingest.PipelineGroupReflect)
	if err != nil {
		t.Fatalf("list pending groups: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("pending groups = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].GroupID != want[i] {
			t.Fatalf("pending group %d = %s, want %s", i, got[i].GroupID, want[i])
		}
	}
}

func scriptedGroupStream(
	t *testing.T,
	responses ...scriptedGroupResponse,
) (providers.StreamFunc, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	stream := func(ctx context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		index := int(calls.Add(1)) - 1
		if index >= len(responses) {
			t.Fatalf("unexpected provider request %d with input %q", index, groupStreamInput(aiCtx))
		}
		response := responses[index]
		if response.wait {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if response.err != nil {
			return nil, response.err
		}
		if len(aiCtx.Tools) != 1 || aiCtx.Tools[0].Name != response.toolName {
			t.Fatalf("request %d tool = %#v, want %q", index, aiCtx.Tools, response.toolName)
		}
		return completedGroupToolStream(response.toolName, response.raw), nil
	}
	return stream, &calls
}

func completedGroupToolStream(name, raw string) providers.AssistantEventStream {
	stream := providers.NewChannelEventStream(2)
	stream.Emit(ai.EventToolCallDelta{
		ID:        "call-" + name,
		Name:      name,
		Arguments: raw,
	})
	stream.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
	stream.Finish(nil)
	return stream
}

func groupStreamInput(aiCtx ai.Context) string {
	if len(aiCtx.Messages) == 0 {
		return ""
	}
	user, ok := aiCtx.Messages[0].(ai.UserMessage)
	if !ok {
		return ""
	}
	text, _ := user.Content.(string)
	return text
}

func assertGroupReflectCursor(t *testing.T, q *sqlc.Queries, groupID string, want int64) {
	t.Helper()
	cursor, err := q.GetIngestCursor(context.Background(), sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: groupingest.PipelineGroupReflect,
	})
	if err != nil {
		t.Fatalf("get Group Reflect cursor: %v", err)
	}
	if cursor.LastSeq != want {
		t.Fatalf("Group Reflect cursor = %d, want %d", cursor.LastSeq, want)
	}
}

func assertNoGroupReflectCursor(t *testing.T, q *sqlc.Queries, groupID string) {
	t.Helper()
	_, err := q.GetIngestCursor(context.Background(), sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: groupingest.PipelineGroupReflect,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Group Reflect cursor error = %v, want pgx.ErrNoRows", err)
	}
}
