package channel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
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

func TestNudgeSkipsAppendWhenClassifierSnapshotChanged(t *testing.T) {
	fx, nudger, _ := staleNudgeFixture(t)
	nudger.SetClassifier(nudgeClassifierFunc(func(ctx context.Context, _ GroupNudgeRequest) (GroupNudgeDecision, error) {
		if _, err := eventlog.NewStore(fx.db).AppendToGroup(ctx, fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "new activity"}); err != nil {
			t.Fatal(err)
		}
		return GroupNudgeDecision{Stalled: true, Target: "agent-1", Reason: "finish the report"}, nil
	}))
	if err := nudger.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countNudgeMessages(t, fx); got != 0 {
		t.Fatalf("nudge messages = %d, want none after classifier snapshot changed", got)
	}
}

func TestNudgeCooldownSurvivesRestart(t *testing.T) {
	fx, first, now := staleNudgeFixture(t)
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
	if err := fx.d.processOutbox(context.Background(), outbox); err != nil {
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
	if err := fx.d.processOutbox(ctx, outbox); err != nil {
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
	message, state, err := fx.d.messageAndState(ctx, fx.d.q, claimed.GroupMessageID)
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

// A classifier outage skips the tick instead of guessing a target: the
// candidate stays eligible and is re-asked once the classifier recovers.
func TestNudgeClassifierOutageSkipsTick(t *testing.T) {
	fx, nudger, _ := staleNudgeFixture(t)
	nudger.SetClassifier(nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{}, errors.New("fast model unavailable")
	}))
	if err := nudger.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countNudgeMessages(t, fx); got != 0 {
		t.Fatalf("outage nudges = %d, want 0", got)
	}
	nudger.SetClassifier(nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{Stalled: true, Target: "agent-1", Reason: "finish the report"}, nil
	}))
	if err := nudger.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countNudgeMessages(t, fx); got != 1 {
		t.Fatalf("recovered nudges = %d, want 1", got)
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
	decision, err := classifier.DecideNudge(context.Background(), GroupNudgeRequest{GroupID: fx.groupID, LastAuthor: "user-1", LastAuthorType: "human", Silence: 5 * time.Minute, Recent: []string{"please finish"}})
	if err != nil {
		t.Fatal(err)
	}
	if loadedAgent != "agent-1" || !decision.Stalled || decision.Target != "agent-1" {
		t.Fatalf("system fast decision = %#v, loaded agent = %q", decision, loadedAgent)
	}
}

// The nudge classifier degrades on a half-configured fast model exactly like
// the intent classifier: no provider is built, so a legacy-bad stored ref costs
// nothing per pass instead of one doomed round trip every tick.
func TestLLMGroupNudgeClassifierSkipsHalfConfiguredFastModel(t *testing.T) {
	fx, _, _ := staleNudgeFixture(t)
	classifier := NewLLMGroupNudgeClassifier(fx.db,
		func(context.Context, string) (*config.Snapshot, error) {
			return &config.Snapshot{Provider: "demo", ModelFast: "demo/", Providers: map[string]config.ProviderCreds{"demo": {Type: "openai", APIKey: "k"}}}, nil
		},
		func(context.Context, string, config.ProviderCreds) (providers.StreamFunc, error) {
			t.Fatal("an empty model id must not reach the provider")
			return nil, nil
		},
	)
	classifier.complete = func(context.Context, ai.Model, ai.Context, ai.CompleteOptions, providers.StreamFunc) (ai.AssistantMessage, error) {
		t.Fatal("an empty model id must not reach a completion")
		return ai.AssistantMessage{}, nil
	}
	_, err := classifier.DecideNudge(context.Background(), GroupNudgeRequest{GroupID: fx.groupID, LastAuthor: "user-1", LastAuthorType: "human", Silence: 5 * time.Minute})
	if !errors.Is(err, errNoFastModel) {
		t.Fatalf("err = %v, want errNoFastModel", err)
	}
}

// A nudge appends a canonical system message, so the group's latest message is
// no longer the human ask and candidacy lapses: one nudge per stalled ask, no
// matter how many cooldowns pass, until somebody actually speaks. The streak
// counter stays as a defense-in-depth bound and resets on any real message.
func TestNudgeIsOneShotUntilSomebodySpeaks(t *testing.T) {
	fx, nudger, now := staleNudgeFixture(t)
	nudger.SetClassifier(nudgeClassifierFunc(func(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error) {
		return GroupNudgeDecision{Stalled: true, Target: "agent-1", Reason: "finish the report"}, nil
	}))
	for i := range 3 {
		tick := now.Add(time.Duration(i) * (groupNudgeCooldown + time.Minute))
		nudger.now = func() time.Time { return tick }
		if err := nudger.RunOnce(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if got := countNudgeMessages(t, fx); got != 1 {
		t.Fatalf("nudges without a new message = %d, want 1", got)
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

// The post check belongs to the session-queue slot, so triage itself still
// admits a nudge whose target posted while it was pending.
func TestNudgeTriageDefersMootCheckUntilSessionSlot(t *testing.T) {
	fx, nudger, _ := staleNudgeFixture(t)
	ctx := context.Background()
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
	if err := fx.d.processOutbox(ctx, outbox); err != nil {
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
	message, state, err := fx.d.messageAndState(ctx, fx.d.q, claimed.GroupMessageID)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := DecodeGroupOutboxEnvelope(outbox.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	act, reason, degraded := fx.d.triageWake(ctx, claimed, message, state, envelope)
	if !act || reason != "nudge" || degraded {
		t.Fatalf("triage = act:%v reason:%q degraded:%v, want queued nudge", act, reason, degraded)
	}
}

func TestQueuedNudgeRechecksMootAfterSessionSlotWithoutRunning(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	fx.d.chat = fx.d.chats.chatDispatch
	fx.d.leaseDuration = time.Minute
	hub := NewGroupEventHub()
	fx.d.SetGroupEventHub(hub)
	follow, cancel := hub.Subscribe(fx.groupID)
	defer cancel()
	envelope, err := encodeGroupOutboxEnvelope(GroupOutboxEnvelope{NudgeTarget: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_outbox SET envelope = $1 WHERE id = $2`, envelope, fx.outbox.ID); err != nil {
		t.Fatal(err)
	}
	// Hold the local session queue without taking a durable dispatch row. The
	// nudge can claim its DB lease, but it cannot announce running until this
	// predecessor releases the slot.
	stream, done, err := fx.d.chats.queue.Enqueue(ctx, agent.BuildGroupSessionKey("agent-1", fx.groupID), func(context.Context) (*pkgchannel.ChatStream, error) {
		return textStream("queue holder"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Events {
	}
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(ctx, fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-1", Content: "wake finished"}); err != nil {
		t.Fatal(err)
	}
	if err := fx.q.CreateGroupNudge(ctx, sqlc.CreateGroupNudgeParams{ID: "d15a0000-0000-0000-0000-000000000301", GroupMessageID: fx.message.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	dispatch, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000301")
	if err != nil {
		t.Fatal(err)
	}
	executeC := make(chan error, 1)
	go func() { executeC <- fx.d.ExecuteDispatch(ctx, dispatch) }()
	select {
	case event := <-follow:
		if event.Turn != nil && event.Turn.State == "running" {
			t.Fatal("queued nudge announced running before it owned the session slot")
		}
	case <-time.After(75 * time.Millisecond):
	}
	close(done)
	if err := <-executeC; err != nil {
		t.Fatalf("execute queued nudge: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(ctx, dispatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Status != "silent" || dispatch.LastError != "nudge_moot" {
		t.Fatalf("queued nudge = %q/%q, want silent/nudge_moot", dispatch.Status, dispatch.LastError)
	}
	if states := drainTurnStates(t, follow); len(states) != 1 || states[0] != "silent" {
		t.Fatalf("turn states = %v, want [silent]", states)
	}
}
