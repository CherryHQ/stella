package channel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/providers"
)

type nudgeClassifierFunc func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error)

func (f nudgeClassifierFunc) DecideNudge(ctx context.Context, req GroupNudgeRequest) (GroupNudgeDecision, error) {
	return f(ctx, req)
}

func staleNudgeFixture(t *testing.T) (dispatcherFixture, *GroupNudger, time.Time) {
	t.Helper()
	fx := newDispatcherFixture(t, "web", "{}")
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_message SET created_at = $1 WHERE id = $2`, now.Add(-10*time.Minute), fx.message.ID); err != nil {
		t.Fatal(err)
	}
	n := NewGroupNudger(fx.db, fx.d)
	n.now = func() time.Time { return now }
	return fx, n, now
}

func createLiveNudgeClaim(t *testing.T, fx dispatcherFixture) {
	t.Helper()
	claim := NewGroupClaimTools(fx.db).Tools()[0]
	if _, err := claim.Execute(claimContext(fx.groupID, "agent-1"), map[string]any{"key": "report", "note": "finish the report"}); err != nil {
		t.Fatalf("create live claim: %v", err)
	}
}

func countNudgeMessages(t *testing.T, fx dispatcherFixture) int {
	t.Helper()
	var count int
	if err := fx.db.QueryRow(context.Background(), `SELECT COUNT(*) FROM ctx_group_message WHERE group_id = $1 AND actor_type = 'system' AND actor_id = 'nudge'`, fx.groupID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestNudgeCASSingleWinner(t *testing.T) {
	fx, _, now := staleNudgeFixture(t)
	createLiveNudgeClaim(t, fx)
	classifier := nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{Stalled: true, Target: "agent-1", Reason: "finish the report"}, nil
	})
	first, second := NewGroupNudger(fx.db, fx.d), NewGroupNudger(fx.db, fx.d)
	first.now, second.now = func() time.Time { return now }, func() time.Time { return now }
	first.SetClassifier(classifier)
	second.SetClassifier(classifier)
	var wg sync.WaitGroup
	for _, nudger := range []*GroupNudger{first, second} {
		wg.Go(func() {
			if err := nudger.RunOnce(context.Background()); err != nil {
				t.Errorf("RunOnce: %v", err)
			}
		})
	}
	wg.Wait()
	if got := countNudgeMessages(t, fx); got != 1 {
		t.Fatalf("nudge messages = %d, want 1", got)
	}
}

func TestNudgeCooldownSurvivesRestart(t *testing.T) {
	fx, first, now := staleNudgeFixture(t)
	createLiveNudgeClaim(t, fx)
	classifier := nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{Stalled: true, Target: "agent-1", Reason: "finish the report"}, nil
	})
	first.SetClassifier(classifier)
	if err := first.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := NewGroupNudger(fx.db, fx.d)
	restarted.now = func() time.Time { return now.Add(30 * time.Minute) }
	restarted.SetClassifier(classifier)
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countNudgeMessages(t, fx); got != 1 {
		t.Fatalf("restart within cooldown nudge messages = %d, want 1", got)
	}
}

func TestNudgeSkipsGroupsWithoutOpenWork(t *testing.T) {
	fx, nudger, _ := staleNudgeFixture(t)
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-1", Content: "done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_message SET created_at = now() - interval '10 minutes' WHERE group_id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	if err := nudger.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countNudgeMessages(t, fx); got != 0 {
		t.Fatalf("nudge messages = %d, want 0", got)
	}
}

func TestNudgeClassifierClosedThreadDoesNotFallBack(t *testing.T) {
	fx, nudger, _ := staleNudgeFixture(t)
	createLiveNudgeClaim(t, fx)
	nudger.SetClassifier(nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{Stalled: false}, nil
	}))
	if err := nudger.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countNudgeMessages(t, fx); got != 0 {
		t.Fatalf("healthy closed thread fell back to nudge count %d", got)
	}
}

func TestNudgeCreatesOneTargetedWake(t *testing.T) {
	fx, nudger, _ := staleNudgeFixture(t)
	createLiveNudgeClaim(t, fx)
	nudger.SetClassifier(nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{Stalled: true, Target: "agent-1", Reason: "finish the report"}, nil
	}))
	if err := nudger.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var messageID string
	if err := fx.db.QueryRow(context.Background(), `SELECT id FROM ctx_group_message WHERE group_id = $1 AND actor_type = 'system'`, fx.groupID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	outbox, err := fx.q.GetGroupOutboxByMessage(context.Background(), messageID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.d.ProcessOutbox(context.Background(), outbox); err != nil {
		t.Fatal(err)
	}
	var agentID, kind string
	if err := fx.db.QueryRow(context.Background(), `SELECT agent_id, kind FROM ctx_group_dispatch WHERE group_message_id = $1`, messageID).Scan(&agentID, &kind); err != nil {
		t.Fatal(err)
	}
	if agentID != "agent-1" || kind != "nudge" {
		t.Fatalf("targeted dispatch = %s/%s, want agent-1/nudge", agentID, kind)
	}
}

// A nudge row is worthless unless the pool actually feeds on it: the wake feed
// filters kind='wake', so the nudge needs its own listing and must still pass
// triage, where the hard caps live.
func TestNudgeRowReachesThePoolAndTriages(t *testing.T) {
	fx, nudger, _ := staleNudgeFixture(t)
	ctx := context.Background()
	createLiveNudgeClaim(t, fx)
	nudger.SetClassifier(nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{Stalled: true, Target: "agent-1", Reason: "finish the report"}, nil
	}))
	if err := nudger.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var messageID string
	if err := fx.db.QueryRow(ctx, `SELECT id FROM ctx_group_message WHERE group_id = $1 AND actor_type = 'system'`, fx.groupID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	outbox, err := fx.q.GetGroupOutboxByMessage(ctx, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.d.ProcessOutbox(ctx, outbox); err != nil {
		t.Fatal(err)
	}

	pending, err := fx.q.ListPendingGroupNudges(ctx, sqlc.ListPendingGroupNudgesParams{Now: nullTime(time.Now().UTC()), LimitCount: 10})
	if err != nil {
		t.Fatalf("list pending nudges: %v", err)
	}
	if len(pending) != 1 || pending[0].AgentID != "agent-1" {
		t.Fatalf("pending nudges = %#v, want one for agent-1", pending)
	}
	claimed, ok, err := fx.d.claimDispatch(ctx, pending[0])
	if err != nil || !ok {
		t.Fatalf("claim nudge: ok=%v, err=%v", ok, err)
	}
	message, state, err := fx.d.messageAndState(ctx, claimed.GroupMessageID)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := DecodeGroupOutboxEnvelope(outbox.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	act, reason, degraded := fx.d.triageWake(ctx, claimed, message, state, envelope)
	if !act || reason != "nudge" || degraded {
		t.Fatalf("triage = act:%v reason:%q degraded:%v, want act/nudge", act, reason, degraded)
	}
}

func TestNudgeClockInjectedJourney(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_message SET created_at = $1 WHERE id = $2`, now.Add(-groupNudgeIdleMin), fx.message.ID); err != nil {
		t.Fatal(err)
	}
	createLiveNudgeClaim(t, fx)
	nudger := NewGroupNudger(fx.db, fx.d)
	nudger.now = func() time.Time { return now }
	nudger.SetClassifier(nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{Stalled: true, Target: "agent-1", Reason: "finish the report"}, nil
	}))
	if err := nudger.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countNudgeMessages(t, fx); got != 1 {
		t.Fatalf("first clock tick nudge messages = %d, want 1", got)
	}
	nudger.now = func() time.Time { return now.Add(time.Minute) }
	if err := nudger.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countNudgeMessages(t, fx); got != 1 {
		t.Fatalf("cooldown clock tick nudge messages = %d, want 1", got)
	}
}

func TestNudgeFallbackNarrowAndCapped(t *testing.T) {
	fx, nudger, now := staleNudgeFixture(t)
	createLiveNudgeClaim(t, fx)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_claim SET lease_until = now() + interval '1 day' WHERE group_id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	nudger.SetClassifier(nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{}, errors.New("fast model unavailable")
	}))
	for i := range groupNudgeStreakMax {
		tick := now.Add(time.Duration(i) * 6 * time.Minute)
		nudger.now = func() time.Time { return tick }
		if err := nudger.RunOnce(context.Background()); err != nil {
			t.Fatalf("fallback tick %d: %v", i, err)
		}
	}
	tick := now.Add(24 * time.Minute)
	nudger.now = func() time.Time { return tick }
	if err := nudger.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countNudgeMessages(t, fx); got != groupNudgeStreakMax {
		t.Fatalf("fallback nudges = %d, want cap %d", got, groupNudgeStreakMax)
	}
	state, err := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	if state.NudgeStreakCount != groupNudgeStreakMax {
		t.Fatalf("fallback count = %d, want %d", state.NudgeStreakCount, groupNudgeStreakMax)
	}
	// The fallback must not nudge the same agent that was the latest speaker.
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-1", Content: "still working"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_message SET created_at = $1 WHERE group_id = $2 AND seq = (SELECT MAX(seq) FROM ctx_group_message WHERE group_id = $2)`, tick.Add(-10*time.Minute), fx.groupID); err != nil {
		t.Fatal(err)
	}
	nudger.now = func() time.Time { return tick.Add(60 * time.Minute) }
	if err := nudger.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countNudgeMessages(t, fx); got != groupNudgeStreakMax {
		t.Fatalf("fallback nudged latest owner: got %d, want %d", got, groupNudgeStreakMax)
	}
}

func TestGroupNudgeWorkerFailsClosedUntilBound(t *testing.T) {
	worker := NewGroupNudgeWorker()
	if err := worker.ValidateStartup(); err == nil {
		t.Fatal("unbound worker must fail startup")
	}
	if err := worker.Bind(&GroupNudger{}); err != nil {
		t.Fatalf("bind worker: %v", err)
	}
	if err := worker.ValidateStartup(); err != nil {
		t.Fatalf("bound worker startup validation: %v", err)
	}
}

func TestLLMGroupNudgeClassifierUsesSystemFastTier(t *testing.T) {
	fx, _, _ := staleNudgeFixture(t)
	var loadedAgent string
	classifier := NewLLMGroupNudgeClassifier(fx.db,
		func(_ context.Context, agentID string) (*config.Snapshot, error) {
			loadedAgent = agentID
			return &config.Snapshot{Provider: "demo", ModelFast: "demo/fast-model", Providers: map[string]config.ProviderCreds{"demo": {Type: "openai", APIKey: "k"}}}, nil
		},
		func(_ context.Context, providerType string, _ config.ProviderCreds) (providers.StreamFunc, error) {
			if providerType != "openai" {
				t.Fatalf("provider type = %q, want openai", providerType)
			}
			return stubStreamFunc, nil
		},
	)
	classifier.complete = func(_ context.Context, model ai.Model, input ai.Context, _ ai.CompleteOptions, _ providers.StreamFunc) (ai.AssistantMessage, error) {
		if model.ID != "fast-model" {
			t.Fatalf("model = %#v, want fast tier", model)
		}
		if len(input.Messages) != 1 {
			t.Fatalf("input messages = %d, want 1", len(input.Messages))
		}
		return ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: `{"stalled":true,"target":"agent-1","reason":"finish report"}`}}}, nil
	}
	decision, err := classifier.DecideNudge(context.Background(), GroupNudgeRequest{GroupID: fx.groupID, LastAuthor: "user-1", LastAuthorType: "human", Silence: 5 * time.Minute, Recent: []string{"please finish"}, Claims: []string{"agent-1: finish report"}})
	if err != nil {
		t.Fatal(err)
	}
	if loadedAgent != "agent-1" || !decision.Stalled || decision.Target != "agent-1" {
		t.Fatalf("system fast decision = %#v, loaded agent = %q", decision, loadedAgent)
	}
}

// The classifier path shares the streak cap with the outage fallback: every
// nudge costs the named agent a full turn, and a claim lease can outlive a
// day's worth of 45-minute cooldowns.
func TestNudgeClassifierPathSharesTheStreakCap(t *testing.T) {
	fx, nudger, now := staleNudgeFixture(t)
	createLiveNudgeClaim(t, fx)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_claim SET lease_until = now() + interval '1 day' WHERE group_id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	nudger.SetClassifier(nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{Stalled: true, Target: "agent-1", Reason: "finish the report"}, nil
	}))
	for i := range groupNudgeStreakMax + 2 {
		tick := now.Add(time.Duration(i) * (groupNudgeCooldown + time.Minute))
		nudger.now = func() time.Time { return tick }
		if err := nudger.RunOnce(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if got := countNudgeMessages(t, fx); got != groupNudgeStreakMax {
		t.Fatalf("classifier nudges = %d, want cap %d", got, groupNudgeStreakMax)
	}
	// A real message means the group moved: the streak starts over.
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-1", Content: "any progress?"}); err != nil {
		t.Fatal(err)
	}
	state, err := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	if state.NudgeStreakCount != 0 {
		t.Fatalf("streak after a real message = %d, want 0", state.NudgeStreakCount)
	}
}

// A nudge is never superseded, so the wake that was already in flight can post
// the very reply the nudge asks for. Running it anyway posts the work twice.
func TestNudgeSilentWhenTargetAlreadyPosted(t *testing.T) {
	fx, nudger, _ := staleNudgeFixture(t)
	ctx := context.Background()
	createLiveNudgeClaim(t, fx)
	nudger.SetClassifier(nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{Stalled: true, Target: "agent-1", Reason: "finish the report"}, nil
	}))
	if err := nudger.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var messageID string
	if err := fx.db.QueryRow(ctx, `SELECT id FROM ctx_group_message WHERE group_id = $1 AND actor_type = 'system'`, fx.groupID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	outbox, err := fx.q.GetGroupOutboxByMessage(ctx, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.d.ProcessOutbox(ctx, outbox); err != nil {
		t.Fatal(err)
	}
	// The in-flight wake lands first.
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(ctx, fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-1", Content: "report is done"}); err != nil {
		t.Fatal(err)
	}
	pending, err := fx.q.ListPendingGroupNudges(ctx, sqlc.ListPendingGroupNudgesParams{Now: nullTime(time.Now().UTC()), LimitCount: 10})
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending nudges = %#v, err=%v", pending, err)
	}
	claimed, ok, err := fx.d.claimDispatch(ctx, pending[0])
	if err != nil || !ok {
		t.Fatalf("claim nudge: ok=%v, err=%v", ok, err)
	}
	message, state, err := fx.d.messageAndState(ctx, claimed.GroupMessageID)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := DecodeGroupOutboxEnvelope(outbox.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	act, reason, degraded := fx.d.triageWake(ctx, claimed, message, state, envelope)
	if act || reason != "nudge_moot" || degraded {
		t.Fatalf("triage = act:%v reason:%q degraded:%v, want silent/nudge_moot", act, reason, degraded)
	}
}
