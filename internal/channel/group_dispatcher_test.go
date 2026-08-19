package channel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type recordingGroupPublisher struct {
	err   error
	calls int
	texts []string
}

type groupTriageFunc func(context.Context, GroupTriageRequest) (bool, string, error)

func (f groupTriageFunc) Decide(ctx context.Context, req GroupTriageRequest) (bool, string, error) {
	return f(ctx, req)
}

type eventRecordingGroupPublisher struct {
	calls  int
	events []pkgchannel.Event
}

func (p *eventRecordingGroupPublisher) Publish(_ context.Context, req GroupPublishRequest) error {
	p.calls++
	if req.Stream == nil {
		return nil
	}
	for event := range req.Stream.Events {
		p.events = append(p.events, event)
	}
	return nil
}

type groupTurnCommitterFunc func(context.Context, *sqlc.Queries, memory.DeferredGroupTurn) error

func (f groupTurnCommitterFunc) CommitGroupTurn(ctx context.Context, q *sqlc.Queries, turn memory.DeferredGroupTurn) error {
	return f(ctx, q, turn)
}

type blockingGroupPublisher struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingGroupPublisher) Publish(ctx context.Context, req GroupPublishRequest) error {
	close(p.started)
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if req.Stream != nil {
		for range req.Stream.Events {
		}
	}
	return nil
}

func (p *recordingGroupPublisher) Publish(ctx context.Context, req GroupPublishRequest) error {
	p.calls++
	if req.Stream != nil {
		for evt := range req.Stream.Events {
			if evt.Text != "" {
				p.texts = append(p.texts, evt.Text)
			}
		}
	}
	return p.err
}

type dispatcherFixture struct {
	db      *pgxpool.Pool
	q       *sqlc.Queries
	d       *GroupDispatcher
	outbox  sqlc.CtxGroupOutbox
	message sqlc.CtxGroupMessage
	groupID string
}

func newDispatcherFixture(t *testing.T, platform, envelope string) dispatcherFixture {
	t.Helper()
	db := dbtest.New(t)
	q := sqlc.New(db)
	ctx := context.Background()
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID:        "agent-1",
		Name:      "Agent One",
		Workspace: t.TempDir(),
		Sandbox:   json.RawMessage("{}"),
		Scope:     "system",
		Enabled:   true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
		ID:      "ch-1",
		AgentID: pgtype.Text{String: "agent-1", Valid: true},
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	state, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID:               "11111111-1111-1111-1111-111111111111",
		Platform:         platform,
		PlatformGroupID:  "physical-group-1",
		PlatformThreadID: "",
		GroupName:        "Group One",
		CreatedByUserID:  pgtype.Text{},
	})
	if err != nil {
		t.Fatalf("create group state: %v", err)
	}
	if _, err := q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
		GroupID:        state.ID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
	}); err != nil {
		t.Fatalf("add group member: %v", err)
	}
	message, err := q.CreateGroupMessage(ctx, sqlc.CreateGroupMessageParams{
		ID:        "a1a1a1a1-0000-0000-0000-000000000001",
		GroupID:   state.ID,
		Seq:       1,
		ActorType: string(eventlog.ActorHuman),
		ActorID:   "user-1",
		Content:   "hello",
	})
	if err != nil {
		t.Fatalf("create group message: %v", err)
	}
	setGroupNextSeq(t, db, state.ID, message.Seq)
	outbox, err := q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{
		ID:             "b0b0b0b0-0000-0000-0000-000000000001",
		GroupMessageID: message.ID,
		GroupID:        state.ID,
		Envelope:       envelope,
		Status:         "pending",
		LastError:      "",
	})
	if err != nil {
		t.Fatalf("create outbox: %v", err)
	}
	coord := &Coordinator{store: cfgstore.NewDBStore(db)}
	d := NewGroupDispatcher(db, coord, NewPublisherRegistry())
	d.leaseDuration = 0
	d.SetGroupTurnCommitter(groupTurnCommitterFunc(func(context.Context, *sqlc.Queries, memory.DeferredGroupTurn) error { return nil }))
	d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: true})
		}
		return textStream("ok"), nil
	}
	return dispatcherFixture{db: db, q: q, d: d, outbox: outbox, message: message, groupID: state.ID}
}

func textStream(text string) *pkgchannel.ChatStream {
	ch := make(chan pkgchannel.Event, 1)
	ch <- pkgchannel.Event{Text: text}
	close(ch)
	return &pkgchannel.ChatStream{Events: ch, SessionID: "session-1"}
}

func createDispatchForGroupMessage(t *testing.T, q *sqlc.Queries, msg sqlc.CtxGroupMessage, id, agentID, groupID string, status string, leaseUntil pgtype.Timestamptz) {
	t.Helper()
	if err := q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             id,
		GroupMessageID: msg.ID,
		GroupID:        groupID,
		AgentID:        agentID,
		ReplyChannelID: "ch-1",
		Status:         status,
		LeaseUntil:     leaseUntil,
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch %s: %v", id, err)
	}
}

func createGroupMessageWithSeq(t *testing.T, q *sqlc.Queries, groupID, id string, seq int64) sqlc.CtxGroupMessage {
	t.Helper()
	return createGroupMessage(t, q, groupID, id, seq, eventlog.ActorHuman, "user-1", "hello")
}

func createGroupMessage(t *testing.T, q *sqlc.Queries, groupID, id string, seq int64, actorType eventlog.ActorType, actorID, content string) sqlc.CtxGroupMessage {
	t.Helper()
	msg, err := q.CreateGroupMessage(context.Background(), sqlc.CreateGroupMessageParams{
		ID:        id,
		GroupID:   groupID,
		Seq:       seq,
		ActorType: string(actorType),
		ActorID:   actorID,
		Content:   content,
	})
	if err != nil {
		t.Fatalf("create message %s: %v", id, err)
	}
	return msg
}

func addFixtureAgent(t *testing.T, fx dispatcherFixture, agentID, channelID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := fx.q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: agentID, Name: agentID, Workspace: t.TempDir(), Sandbox: json.RawMessage("{}"), Scope: "system", Enabled: true}); err != nil {
		t.Fatalf("create %s: %v", agentID, err)
	}
	if err := fx.q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{ID: channelID, AgentID: pgtype.Text{String: agentID, Valid: true}}); err != nil {
		t.Fatalf("create %s channel: %v", agentID, err)
	}
	if _, err := fx.q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{GroupID: fx.groupID, AgentID: agentID, ReplyChannelID: channelID}); err != nil {
		t.Fatalf("add %s to group: %v", agentID, err)
	}
}

func TestWakeMaterializationSkipsAuthor(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	if _, err := fx.q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "agent-2", Name: "Agent Two", Workspace: t.TempDir(), Sandbox: json.RawMessage("{}"), Scope: "system", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := fx.q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{ID: "ch-2", AgentID: pgtype.Text{String: "agent-2", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{GroupID: fx.groupID, AgentID: "agent-2", ReplyChannelID: "ch-2"}); err != nil {
		t.Fatal(err)
	}
	if err := fx.d.materializeDispatchRowsTx(ctx, fx.outbox); err != nil {
		t.Fatal(err)
	}
	rows, err := fx.q.ListPendingGroupWakePairs(ctx, sqlc.ListPendingGroupWakePairsParams{Now: nullTime(time.Now().UTC()), LimitCount: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("human wakes = %d, %v; want 2", len(rows), err)
	}
	agentMessage, err := fx.q.CreateGroupMessage(ctx, sqlc.CreateGroupMessageParams{ID: "a1a1a1a1-0000-0000-0000-000000000002", GroupID: fx.groupID, Seq: 2, ActorType: string(eventlog.ActorAgent), ActorID: "agent-1", Content: "@agent-2"})
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := fx.q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{ID: "b0b0b0b0-0000-0000-0000-000000000002", GroupMessageID: agentMessage.ID, GroupID: fx.groupID, Envelope: "{}", Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.d.materializeDispatchRowsTx(ctx, outbox); err != nil {
		t.Fatal(err)
	}
	var authorWakes int
	if err := fx.db.QueryRow(ctx, `SELECT count(*) FROM ctx_group_dispatch WHERE group_message_id = $1 AND agent_id = 'agent-1'`, agentMessage.ID).Scan(&authorWakes); err != nil {
		t.Fatal(err)
	}
	if authorWakes != 0 {
		t.Fatalf("author wakes = %d, want 0", authorWakes)
	}
}

func TestClaimNewestWakeSupersedesOlder(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	older := fx.message
	newer := createGroupMessageWithSeq(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000003", 2)
	for id, message := range map[string]sqlc.CtxGroupMessage{"d15a0000-0000-0000-0000-000000000011": older, "d15a0000-0000-0000-0000-000000000012": newer} {
		if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: id, GroupMessageID: message.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
			t.Fatal(err)
		}
	}
	claimed, ok, err := fx.d.claimDispatch(ctx, sqlc.CtxGroupDispatch{Status: "pending", Kind: "wake", GroupID: fx.groupID, AgentID: "agent-1"})
	if err != nil || !ok || claimed.GroupMessageID != newer.ID {
		t.Fatalf("claimed = %#v, ok=%v, err=%v", claimed, ok, err)
	}
	var status string
	if err := fx.db.QueryRow(ctx, `SELECT status FROM ctx_group_dispatch WHERE id = $1`, "d15a0000-0000-0000-0000-000000000011").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "superseded" {
		t.Fatalf("older status = %q, want superseded", status)
	}
}

func TestGroupDispatcherReapExpiredCompletesPublishedDispatch(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	past := time.Now().UTC().Add(-time.Minute)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-0000000000ff",
		GroupMessageID: fx.message.ID,
		GroupID:        fx.message.GroupID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "running",
		AttemptCount:   fx.d.maxAttempts,
		LeaseUntil:     nullTime(past),
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = 'result-1', published_at = now() WHERE id = 'd15a0000-0000-0000-0000-0000000000ff'`); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	if err := fx.d.reapExpired(context.Background()); err != nil {
		t.Fatalf("reap expired: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-0000000000ff")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if dispatch.Status != "completed" {
		t.Fatalf("dispatch status = %q, want completed", dispatch.Status)
	}
}

// An accepted reply whose publish never happened still owes egress. Retiring it
// on lease expiry (the crash path) would strand the committed message forever,
// so the reaper must requeue it back onto the canonical-replay path instead.
func TestGroupDispatcherReapExpiredRequeuesAcceptedUnpublishedDispatch(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	past := time.Now().UTC().Add(-time.Minute)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-0000000000fe",
		GroupMessageID: fx.message.ID,
		GroupID:        fx.message.GroupID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "running",
		AttemptCount:   fx.d.maxAttempts,
		LeaseUntil:     nullTime(past),
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = 'result-1' WHERE id = 'd15a0000-0000-0000-0000-0000000000fe'`); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	if err := fx.d.reapExpired(context.Background()); err != nil {
		t.Fatalf("reap expired: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-0000000000fe")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if dispatch.Status != "pending" {
		t.Fatalf("dispatch status = %q, want pending", dispatch.Status)
	}
	if dispatch.ResultMessageID != "result-1" {
		t.Fatalf("result marker = %q, want preserved", dispatch.ResultMessageID)
	}
}

// Supersede retires stale snapshots, but a pending row carrying an accepted
// result is not stale: nothing reads a superseded row, so retiring it here is
// how a committed reply silently stops being delivered.
func TestClaimNewestWakeKeepsAcceptedUnpublishedOlder(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	ctx := context.Background()
	older := fx.message
	newer := createGroupMessageWithSeq(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000004", 2)
	for id, message := range map[string]sqlc.CtxGroupMessage{"d15a0000-0000-0000-0000-000000000021": older, "d15a0000-0000-0000-0000-000000000022": newer} {
		if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: id, GroupMessageID: message.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET result_message_id = 'result-1' WHERE id = 'd15a0000-0000-0000-0000-000000000021'`); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	if _, ok, err := fx.d.claimDispatch(ctx, sqlc.CtxGroupDispatch{Status: "pending", Kind: "wake", GroupID: fx.groupID, AgentID: "agent-1"}); err != nil || !ok {
		t.Fatalf("claim newest: ok=%v, err=%v", ok, err)
	}
	var status string
	if err := fx.db.QueryRow(ctx, `SELECT status FROM ctx_group_dispatch WHERE id = $1`, "d15a0000-0000-0000-0000-000000000021").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("accepted-unpublished older status = %q, want pending", status)
	}
}

func TestGroupDispatcherWebNoMentionSingleMemberFallbackCreatesOneDispatch(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if got := dispatchAgentsByMessage(t, fx.db, fx.message.ID); len(got) != 1 || got[0] != "agent-1" {
		t.Fatalf("dispatch agents = %v, want [agent-1]", got)
	}
}

func TestTriageClassifierActsOnRelevantMessage(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	fx.d.SetGroupTriage(groupTriageFunc(func(_ context.Context, req GroupTriageRequest) (bool, string, error) {
		if req.AgentID != "agent-1" || req.Message != fx.message.Content {
			t.Fatalf("unexpected triage request: %+v", req)
		}
		return true, "relevant", nil
	}))
	row := sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-1", TriggerSeq: fx.message.Seq, Kind: "wake"}
	state, err := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	act, reason, degraded := fx.d.triageWake(context.Background(), row, fx.message, state, GroupOutboxEnvelope{})
	if !act || degraded || reason != "classifier:relevant" {
		t.Fatalf("act=%v reason=%q degraded=%v", act, reason, degraded)
	}
}

func TestUnmentionedPeerSilentWhenOthersMentioned(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	row := sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-1", TriggerSeq: fx.message.Seq, Kind: "wake"}
	state, err := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	act, reason, degraded := fx.d.triageWake(context.Background(), row, fx.message, state, GroupOutboxEnvelope{Mentions: []pkgchannel.Mention{{AgentID: "other"}}})
	if act || degraded || reason != "mentioned_peer" {
		t.Fatalf("act=%v reason=%q degraded=%v", act, reason, degraded)
	}
}

func TestTriageActiveClaimKeepsAgentChainAlive(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	store := eventlog.NewStore(fx.db)
	var latest eventlog.AppendResult
	for _, agentID := range []string{"agent-2", "agent-1"} {
		result, err := store.AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: agentID, Content: "still working"})
		if err != nil {
			t.Fatal(err)
		}
		latest = result
	}
	claim := NewGroupClaimTools(fx.db).Tools()[0]
	if _, err := claim.Execute(claimContext(fx.groupID, "agent-1"), map[string]any{"key": "report"}); err != nil {
		t.Fatal(err)
	}
	fx.d.SetGroupTriage(groupTriageFunc(func(_ context.Context, req GroupTriageRequest) (bool, string, error) {
		if len(req.Claims) != 1 {
			t.Fatalf("claims=%v", req.Claims)
		}
		return true, "claimed_followup", nil
	}))
	state, err := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	act, reason, degraded := fx.d.triageWake(context.Background(), sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-2", TriggerSeq: latest.Seq, Kind: "wake"}, latest.Message, state, GroupOutboxEnvelope{})
	if !act || degraded || reason != "classifier:claimed_followup" {
		t.Fatalf("act=%v reason=%s degraded=%v", act, reason, degraded)
	}
}

func TestClaimedRunUsesHardCapNotLapping(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	store := eventlog.NewStore(fx.db)
	var latest eventlog.AppendResult
	for _, agentID := range []string{"agent-2", "agent-1"} {
		result, err := store.AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: agentID, Content: "working"})
		if err != nil {
			t.Fatal(err)
		}
		latest = result
	}
	claim := NewGroupClaimTools(fx.db).Tools()[0]
	if _, err := claim.Execute(claimContext(fx.groupID, "agent-1"), map[string]any{"key": "report"}); err != nil {
		t.Fatal(err)
	}
	fx.d.SetGroupTriage(groupTriageFunc(func(context.Context, GroupTriageRequest) (bool, string, error) { return true, "continue", nil }))
	state, err := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	row := sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-2", TriggerSeq: latest.Seq, Kind: "wake"}
	if act, reason, _ := fx.d.triageWake(context.Background(), row, latest.Message, state, GroupOutboxEnvelope{}); !act || reason != "classifier:continue" {
		t.Fatalf("claimed run lapped: act=%v reason=%q", act, reason)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_state SET agent_chain_hard_limit=1 WHERE id=$1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	state, err = fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	if act, reason, _ := fx.d.triageWake(context.Background(), row, latest.Message, state, GroupOutboxEnvelope{}); act || reason != "hard_cap" {
		t.Fatalf("claimed run bypassed hard cap: act=%v reason=%q", act, reason)
	}
}

func TestHardCapsPrecedeMention(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_state SET max_agent_posts_per_minute = 1 WHERE id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-1", Content: "already posted"}); err != nil {
		t.Fatal(err)
	}
	row := sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-1", TriggerSeq: fx.message.Seq, Kind: "wake"}
	state, err := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	act, reason, _ := fx.d.triageWake(context.Background(), row, fx.message, state, GroupOutboxEnvelope{Mentions: []pkgchannel.Mention{{AgentID: "agent-1"}}})
	if act || reason != "hard_cap" {
		t.Fatalf("act=%v reason=%q", act, reason)
	}
}

func TestTriageClassifierFailureRequeuesThenSilent(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	fx.d.SetGroupTriage(groupTriageFunc(func(context.Context, GroupTriageRequest) (bool, string, error) {
		return false, "provider", errors.New("down")
	}))
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		t.Fatal("failed triage must not chat")
		return nil, nil
	}
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000091", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	row, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000091")
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), row); err == nil {
		t.Fatal("triage failure must requeue")
	}
	row, _ = fx.q.GetGroupDispatch(context.Background(), row.ID)
	if row.Status != "pending" {
		t.Fatalf("status=%q, want pending", row.Status)
	}
}

func TestPlatformRulesOnlyTriageMatrix(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	row := sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-1", TriggerSeq: fx.message.Seq, Kind: "wake"}
	state, _ := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	act, reason, _ := fx.d.triageWake(context.Background(), row, fx.message, state, GroupOutboxEnvelope{})
	if act || reason != "rules_only" {
		t.Fatalf("act=%v reason=%q", act, reason)
	}
}

func TestUnclaimedAgentRunStopsAfterOneLap(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-1", Content: "first"}); err != nil {
		t.Fatal(err)
	}
	if !fx.d.agentRunLapped(context.Background(), fx.groupID, fx.message.Seq+2, "agent-1") {
		t.Fatal("agent repeat must lap")
	}
}

func TestFreshnessGateHoldsWhenPeerPostedAfterSnapshot(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000092", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000092")
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "new"}); err != nil {
		t.Fatal(err)
	}
	state, _ := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	_, err := fx.d.acceptGroupResponse(context.Background(), row, state, groupResponse{text: "reply", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if !errors.Is(err, errGroupTurnHeld) {
		t.Fatalf("err=%v, want held", err)
	}
}

func TestFreshnessGateSerializesWithHumanIngest(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000096", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000096")
	if err != nil {
		t.Fatal(err)
	}

	// Human ingress and acceptance share ctx_group_state's row lock. Hold it as
	// the ingress path does, start acceptance, then commit the human row first.
	// The acceptance must observe that commit and HOLD rather than post stale text.
	tx, err := fx.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	qtx := fx.q.WithTx(tx)
	if _, err := qtx.GetGroupStateByIDForUpdate(ctx, fx.groupID); err != nil {
		t.Fatal(err)
	}
	acceptC := make(chan error, 1)
	state, err := fx.q.GetGroupStateByID(ctx, fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, acceptErr := fx.d.acceptGroupResponse(ctx, row, state, groupResponse{text: "stale", complete: true}, memory.DeferredGroupTurn{Complete: true})
		acceptC <- acceptErr
	}()
	time.Sleep(25 * time.Millisecond) // acceptance is blocked behind ingress
	seq, err := qtx.BumpGroupSeq(ctx, fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := qtx.CreateGroupMessage(ctx, sqlc.CreateGroupMessageParams{ID: "a1a1a1a1-0000-0000-0000-000000000096", GroupID: fx.groupID, Seq: seq, ActorType: string(eventlog.ActorHuman), ActorID: "user-2", Content: "arrived before accept"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-acceptC; !errors.Is(err, errGroupTurnHeld) {
		t.Fatalf("accept err=%v, want HOLD", err)
	}
}

func TestVerbatimDuplicateHeld(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000093", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000093")
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-2", Content: "same"}); err != nil {
		t.Fatal(err)
	}
	state, _ := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	_, err := fx.d.acceptGroupResponse(context.Background(), row, state, groupResponse{text: "same", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if !errors.Is(err, errGroupTurnHeld) {
		t.Fatalf("err=%v, want held", err)
	}
}

func TestHoldLimitPostsThrough(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_state SET hold_limit = 0 WHERE id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000094", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000094")
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "new"}); err != nil {
		t.Fatal(err)
	}
	state, _ := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if _, err := fx.d.acceptGroupResponse(context.Background(), row, state, groupResponse{text: "reply", complete: true}, memory.DeferredGroupTurn{Complete: true}); err != nil {
		t.Fatalf("hold limit must post through: %v", err)
	}
}

func TestHoldChainResetsAtNewHumanTrigger(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_state SET hold_limit = 1 WHERE id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	// This HOLD belongs to the first human chain and must not consume the next
	// human trigger's one permitted freshness check.
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000097", GroupMessageID: fx.message.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET status='held', held_up_to_seq=1 WHERE id=$1`, "d15a0000-0000-0000-0000-000000000097"); err != nil {
		t.Fatal(err)
	}
	newHuman, err := eventlog.NewStore(fx.db).AppendToGroup(ctx, fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "new question"})
	if err != nil {
		t.Fatal(err)
	}
	createDispatchForGroupMessage(t, fx.q, newHuman.Message, "d15a0000-0000-0000-0000-000000000098", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(ctx, fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-3", Content: "newer peer"}); err != nil {
		t.Fatal(err)
	}
	row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000098")
	if err != nil {
		t.Fatal(err)
	}
	state, _ := fx.q.GetGroupStateByID(ctx, fx.groupID)
	if _, err := fx.d.acceptGroupResponse(ctx, row, state, groupResponse{text: "reply", complete: true}, memory.DeferredGroupTurn{Complete: true}); !errors.Is(err, errGroupTurnHeld) {
		t.Fatalf("new human chain err=%v, want HOLD", err)
	}
}

func TestHoldSuccessorSnapshotCoversHoldSeq(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000099", GroupMessageID: fx.message.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET status='held', held_up_to_seq=3 WHERE id=$1`, "d15a0000-0000-0000-0000-000000000099"); err != nil {
		t.Fatal(err)
	}
	second := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000097", 2, eventlog.ActorAgent, "agent-2", "peer one")
	setGroupNextSeq(t, fx.db, fx.groupID, second.Seq)
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000100", GroupMessageID: second.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fx.d.claimDispatch(ctx, sqlc.CtxGroupDispatch{Status: "pending", Kind: "wake", GroupID: fx.groupID, AgentID: "agent-1"}); err != nil || ok {
		t.Fatalf("uncovered successor claimed=%v err=%v", ok, err)
	}
	third := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000098", 3, eventlog.ActorAgent, "agent-2", "peer two")
	setGroupNextSeq(t, fx.db, fx.groupID, third.Seq)
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000101", GroupMessageID: third.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := fx.d.claimDispatch(ctx, sqlc.CtxGroupDispatch{Status: "pending", Kind: "wake", GroupID: fx.groupID, AgentID: "agent-1"})
	if err != nil || !ok || claimed.TriggerSeq < 3 {
		t.Fatalf("covered successor=%+v ok=%v err=%v", claimed, ok, err)
	}
}

func TestHoldNeverRepeatsOnSameSnapshot(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000102", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	first, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000102")
	peer, err := eventlog.NewStore(fx.db).AppendToGroup(ctx, fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "newer"})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := fx.q.GetGroupStateByID(ctx, fx.groupID)
	if _, err := fx.d.acceptGroupResponse(ctx, first, state, groupResponse{text: "first", complete: true}, memory.DeferredGroupTurn{Complete: true}); !errors.Is(err, errGroupTurnHeld) {
		t.Fatalf("first err=%v, want HOLD", err)
	}
	held, _ := fx.q.GetGroupDispatch(ctx, first.ID)
	if !held.HeldUpToSeq.Valid || held.HeldUpToSeq.Int64 != peer.Seq {
		t.Fatalf("held_up_to_seq=%+v, want %d", held.HeldUpToSeq, peer.Seq)
	}
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000103", GroupMessageID: peer.Message.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	successor, ok, err := fx.d.claimDispatch(ctx, sqlc.CtxGroupDispatch{Status: "pending", Kind: "wake", GroupID: fx.groupID, AgentID: "agent-1"})
	if err != nil || !ok || successor.TriggerSeq != held.HeldUpToSeq.Int64 {
		t.Fatalf("successor=%+v ok=%v err=%v", successor, ok, err)
	}
	if _, err := fx.d.acceptGroupResponse(ctx, successor, state, groupResponse{text: "second", complete: true}, memory.DeferredGroupTurn{Complete: true}); err != nil {
		t.Fatalf("same covered snapshot held again: %v", err)
	}
}

func TestHoldDiscardsDeferredTurn(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	fx.d.SetGroupTurnCommitter(groupTurnCommitterFunc(func(context.Context, *sqlc.Queries, memory.DeferredGroupTurn) error {
		t.Fatal("a held turn must not commit deferred memory")
		return nil
	}))
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000104", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(ctx, fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "newer"}); err != nil {
		t.Fatal(err)
	}
	row, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000104")
	if err := fx.d.ExecuteDispatch(ctx, row); err != nil {
		t.Fatalf("HOLD is terminal for this turn, got %v", err)
	}
	row, _ = fx.q.GetGroupDispatch(ctx, row.ID)
	if row.Status != "held" {
		t.Fatalf("dispatch status=%q, want held", row.Status)
	}
}

func TestPendingPostFinalFailureRequeuesHeldPeers(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	ctx := context.Background()
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000105", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	post, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000105")
	state, _ := fx.q.GetGroupStateByID(ctx, fx.groupID)
	accepted, err := fx.d.acceptGroupResponse(ctx, post, state, groupResponse{text: "pending delivery", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	post.ResultMessageID = accepted.Message.ID
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000106", "agent-2", fx.groupID, "running", pgtype.Timestamptz{})
	peer, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000106")
	if _, err := fx.d.acceptGroupResponse(ctx, peer, state, groupResponse{text: "peer reply", complete: true}, memory.DeferredGroupTurn{Complete: true}); !errors.Is(err, errGroupTurnHeld) {
		t.Fatalf("peer err=%v, want HOLD", err)
	}
	if err := fx.d.failAcceptedPublish(ctx, post, errors.New("platform down")); err == nil {
		t.Fatal("final publish failure must be reported")
	}
	peer, _ = fx.q.GetGroupDispatch(ctx, peer.ID)
	if peer.Status != "pending" || peer.HeldUpToSeq.Valid {
		t.Fatalf("held peer=%+v, want requeued with cleared gate", peer)
	}
}

func TestSilentTriageSupersedesOlderWakes(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	newer := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000099", 2, eventlog.ActorHuman, "user-2", "newer")
	setGroupNextSeq(t, fx.db, fx.groupID, newer.Seq)
	if _, err := fx.q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{ID: "b0b0b0b0-0000-0000-0000-000000000099", GroupMessageID: newer.ID, GroupID: fx.groupID, Envelope: "{}", Status: "completed", LastError: ""}); err != nil {
		t.Fatal(err)
	}
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000107", GroupMessageID: fx.message.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000108", GroupMessageID: newer.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	fx.d.SetGroupTriage(groupTriageFunc(func(context.Context, GroupTriageRequest) (bool, string, error) { return false, "not_needed", nil }))
	if err := fx.d.ExecuteDispatch(ctx, sqlc.CtxGroupDispatch{Status: "pending", Kind: "wake", GroupID: fx.groupID, AgentID: "agent-1"}); err != nil {
		t.Fatal(err)
	}
	var older, latest string
	if err := fx.db.QueryRow(ctx, `SELECT status FROM ctx_group_dispatch WHERE id=$1`, "d15a0000-0000-0000-0000-000000000107").Scan(&older); err != nil {
		t.Fatal(err)
	}
	if err := fx.db.QueryRow(ctx, `SELECT status FROM ctx_group_dispatch WHERE id=$1`, "d15a0000-0000-0000-0000-000000000108").Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if older != "superseded" || latest != "silent" {
		t.Fatalf("statuses older/latest=%q/%q, want superseded/silent", older, latest)
	}
}

func TestAgentReplyCreatesOutboxAfterPublish(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000095", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000095")
	state, _ := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	result, err := fx.d.acceptGroupResponse(context.Background(), row, state, groupResponse{text: "peer reply", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	row.ResultMessageID = result.Message.ID
	if err := fx.d.createAgentReplyOutbox(context.Background(), fx.q, row); err != nil {
		t.Fatal(err)
	}
	outbox, err := fx.q.GetGroupOutboxByMessage(context.Background(), result.Message.ID)
	if err != nil || outbox.Status != "pending" {
		t.Fatalf("agent outbox=%+v err=%v", outbox, err)
	}
}

func TestGroupDispatcherResolvesEnvelopeMentionAtDispatch(t *testing.T) {
	envelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{Raw: "@bot1", PlatformID: "bot1"}})
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	fx := newDispatcherFixture(t, "telegram", envelope)
	reg := NewBotIdentityRegistry()
	reg.Register("telegram", "bot1", "ch-1")
	fx.d.coord.botRegistry = reg
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if got := dispatchAgentsByMessage(t, fx.db, fx.message.ID); len(got) != 1 || got[0] != "agent-1" {
		t.Fatalf("dispatch agents = %v, want [agent-1]", got)
	}
}

func TestGroupDispatcherExistingDispatchSkipsEnvelopeDecode(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{not-json`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox should skip invalid envelope when dispatch rows exist: %v", err)
	}
	outbox, err := fx.q.GetGroupOutbox(context.Background(), fx.outbox.ID)
	if err != nil {
		t.Fatalf("get outbox: %v", err)
	}
	if outbox.Status != "completed" {
		t.Fatalf("outbox status = %q, want completed", outbox.Status)
	}
}

func TestGroupDispatcherPublishFailureLeavesResultEmptyAndRequeues(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	boom := errors.New("boom")
	publisher := &recordingGroupPublisher{err: boom}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err == nil {
		t.Fatal("expected publisher error")
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after failure: %v", err)
	}
	if dispatch.Status != "pending" || dispatch.ResultMessageID == "" || dispatch.PublishedAt.Valid {
		t.Fatalf("dispatch status/result/published = %q/%q/%v, want pending accepted-unpublished result", dispatch.Status, dispatch.ResultMessageID, dispatch.PublishedAt.Valid)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 1 {
		t.Fatalf("agent messages = %d, want accepted result", got)
	}

	publisher.err = nil
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET next_attempt_at = NULL WHERE id = 'd15a0000-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("make dispatch due: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch before retry: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err != nil {
		t.Fatalf("retry dispatch: %v", err)
	}
	if publisher.calls != 2 {
		t.Fatalf("publisher calls = %d, want retry to republish", publisher.calls)
	}
}

// Retrying an accepted-but-unpublished row is egress compensation, not a new
// speaking decision. Triage would count the agent's own committed post and go
// silent, leaving a reply that peers can read and humans never receive.
func TestGroupDispatcherRepublishesAcceptedResultDespiteHardCap(t *testing.T) {
	ctx := context.Background()
	fx := newDispatcherFixture(t, "web", `{}`)
	boom := errors.New("boom")
	publisher := &recordingGroupPublisher{err: boom}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(ctx, sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000031",
		GroupMessageID: fx.message.ID,
		GroupID:        fx.groupID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000031")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(ctx, dispatch); err == nil {
		t.Fatal("expected publisher error")
	}

	publisher.err = nil
	// Any triage branch would refuse now; the accepted post itself is what the
	// cap counts.
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_state SET max_replies_per_human_trigger = 0 WHERE id = $1`, fx.groupID); err != nil {
		t.Fatalf("tighten cap: %v", err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET next_attempt_at = NULL WHERE id = 'd15a0000-0000-0000-0000-000000000031'`); err != nil {
		t.Fatalf("make dispatch due: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000031")
	if err != nil {
		t.Fatalf("get dispatch before retry: %v", err)
	}
	if err := fx.d.ExecuteDispatch(ctx, dispatch); err != nil {
		t.Fatalf("retry dispatch: %v", err)
	}
	if publisher.calls != 2 {
		t.Fatalf("publisher calls = %d, want the accepted reply republished", publisher.calls)
	}
	dispatch, err = fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000031")
	if err != nil {
		t.Fatalf("get dispatch after retry: %v", err)
	}
	if !dispatch.PublishedAt.Valid {
		t.Fatalf("dispatch status = %q, published = %v, want published", dispatch.Status, dispatch.PublishedAt.Valid)
	}
}

func TestGroupDispatcherWritebackFailureLeavesResultEmptyAndRequeues(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	chatCalls := 0
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: true})
		}
		return textStream("ok"), nil
	}
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `CREATE FUNCTION fail_agent_writeback_fn() RETURNS trigger AS $$ BEGIN IF NEW.actor_type = 'agent' THEN RAISE EXCEPTION 'fail agent writeback'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql;`); err != nil {
		t.Fatalf("create trigger function: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `CREATE TRIGGER fail_agent_writeback BEFORE INSERT ON ctx_group_message FOR EACH ROW EXECUTE FUNCTION fail_agent_writeback_fn();`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err == nil {
		t.Fatal("expected writeback error")
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after failure: %v", err)
	}
	if dispatch.Status != "pending" || dispatch.ResultMessageID != "" {
		t.Fatalf("dispatch status/result = %q/%q, want pending empty result", dispatch.Status, dispatch.ResultMessageID)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 0 {
		t.Fatalf("agent messages = %d, want failed transaction to append none", got)
	}
	if _, err := fx.db.Exec(context.Background(), `DROP TRIGGER fail_agent_writeback ON ctx_group_message`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET next_attempt_at = NULL WHERE id = 'd15a0000-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("make dispatch due: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch before retry: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err != nil {
		t.Fatalf("retry dispatch: %v", err)
	}
	if chatCalls != 2 {
		t.Fatalf("chat calls = %d, want retry to rerun chat", chatCalls)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 1 {
		t.Fatalf("agent messages = %d, want one successful retry append", got)
	}
}

// TestGroupDispatcherSupersededTriggerCompletesWithoutChat pins the restart
// half of the group `/new` boundary: a dispatch row whose trigger seq sits at
// or below the agent's ingest cursor — a rotation consumed it while the row
// waited — retires as completed instead of running a pre-reset turn against
// the successor session.
func TestGroupDispatcherSupersededTriggerCompletesWithoutChat(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.chat = fx.d.chatDispatch // the cursor guard lives on the real chat path
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.UpsertIngestCursor(context.Background(), sqlc.UpsertIngestCursorParams{
		GroupID:  "11111111-1111-1111-1111-111111111111",
		Pipeline: memory.GroupIngestPipeline("agent-1"),
		LastSeq:  fx.message.Seq,
	}); err != nil {
		t.Fatalf("seed rotation boundary: %v", err)
	}
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001",
		"agent-1", "11111111-1111-1111-1111-111111111111", "pending", pgtype.Timestamptz{})
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err != nil {
		t.Fatalf("ExecuteDispatch: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after execute: %v", err)
	}
	if dispatch.Status != "completed" {
		t.Fatalf("dispatch status = %q, want completed retirement", dispatch.Status)
	}
	if publisher.calls != 0 {
		t.Fatalf("publisher calls = %d, want the superseded turn never published", publisher.calls)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 0 {
		t.Fatalf("agent messages = %d, want the superseded turn never ran", got)
	}
}

func TestGroupDispatcherAcceptedResultSkipsChatAndReplaysPublish(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	chatCalls := 0
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: true})
		}
		return textStream("ok"), nil
	}
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "running",
		AttemptCount:   1,
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	result, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-1", Content: "accepted"})
	if err != nil {
		t.Fatalf("append accepted result: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = $1 WHERE id = 'd15a0000-0000-0000-0000-000000000001'`, result.Message.ID); err != nil {
		t.Fatalf("set result marker: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err != nil {
		t.Fatalf("execute dispatch: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after execute: %v", err)
	}
	if dispatch.Status != "completed" || chatCalls != 0 || publisher.calls != 1 {
		t.Fatalf("status/chat/publish = %q/%d/%d, want completed/0/1", dispatch.Status, chatCalls, publisher.calls)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 1 {
		t.Fatalf("agent messages = %d, want accepted result only", got)
	}
}

func TestRepublishAfterRestartUsesCanonicalTextOnly(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &eventRecordingGroupPublisher{}
	// A fresh dispatcher has no retained event envelope. It must re-publish the
	// committed canonical row without starting another model turn.
	restarted := NewGroupDispatcher(fx.db, fx.d.coord, NewPublisherRegistry())
	restarted.SetGroupTurnCommitter(groupTurnCommitterFunc(func(context.Context, *sqlc.Queries, memory.DeferredGroupTurn) error {
		t.Fatal("restart replay must not commit another group turn")
		return nil
	}))
	restarted.publishers.Register("ch-1", publisher)
	restarted.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		t.Fatal("restart replay must not run chat")
		return nil, nil
	}
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        fx.groupID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "running",
		AttemptCount:   1,
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	accepted, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent,
		ActorID:   "agent-1",
		Content:   "canonical text",
		Reasoning: "canonical reasoning",
	})
	if err != nil {
		t.Fatalf("append accepted result: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = $1 WHERE id = $2`, accepted.Message.ID, "d15a0000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("set result marker: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := restarted.ExecuteDispatch(context.Background(), dispatch); err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if publisher.calls != 1 || len(publisher.events) != 2 {
		t.Fatalf("publisher calls/events = %d/%d, want 1/2 canonical events", publisher.calls, len(publisher.events))
	}
	if got := publisher.events[0].Reasoning; got != "canonical reasoning" {
		t.Fatalf("first replay event reasoning = %q", got)
	}
	if got := publisher.events[1].Text; got != "canonical text" {
		t.Fatalf("second replay event text = %q", got)
	}
}

func TestGroupDispatcherWebWriteErrorStillRecordsResult(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err != nil {
		t.Fatalf("execute dispatch: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after execute: %v", err)
	}
	if dispatch.ResultMessageID == "" || dispatch.Status != "completed" {
		t.Fatalf("dispatch status/result = %q/%q, want completed result", dispatch.Status, dispatch.ResultMessageID)
	}
}

func TestGroupDispatcherPublisherFailureMarksFailedAtMaxAttempts(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.maxAttempts = 1
	boom := errors.New("boom")
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{err: boom})
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}

	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err == nil {
		t.Fatal("expected publisher error")
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after failure: %v", err)
	}
	if dispatch.Status != "failed" {
		t.Fatalf("dispatch status = %q, want failed", dispatch.Status)
	}
	if dispatch.ResultMessageID == "" {
		t.Fatal("final publish failure must retain the accepted result")
	}
	result, err := fx.q.GetGroupMessage(context.Background(), dispatch.ResultMessageID)
	if err != nil {
		t.Fatalf("get failed result: %v", err)
	}
	if result.DeliveryState != "failed" {
		t.Fatalf("delivery state = %q, want failed", result.DeliveryState)
	}
}

func TestIncompleteStreamDiscardsDeferredTurnAndPersistsNothing(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: false})
		}
		return textStream("partial"), nil
	}
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err == nil {
		t.Fatal("incomplete stream must fail the dispatch")
	}
	if publisher.calls != 0 || countAgentGroupMessages(t, fx.db) != 0 {
		t.Fatalf("publisher/messages = %d/%d, want 0/0", publisher.calls, countAgentGroupMessages(t, fx.db))
	}
}

func TestAcceptTxRollbackAnnouncesNothing(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	hub := NewGroupEventHub()
	fx.d.SetGroupEventHub(hub)
	fx.d.SetGroupTurnCommitter(groupTurnCommitterFunc(func(context.Context, *sqlc.Queries, memory.DeferredGroupTurn) error {
		return errors.New("injected memory failure")
	}))
	follow, cancel := hub.Subscribe(fx.groupID)
	defer cancel()
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{})
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err == nil {
		t.Fatal("expected injected commit failure")
	}
	if countAgentGroupMessages(t, fx.db) != 0 {
		t.Fatal("rollback left a group post")
	}
	select {
	case event := <-follow:
		t.Fatalf("rollback announced %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestGroupDispatcherExtendsOutboxLeaseWhileRunning(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.leaseDuration = 2 * time.Second
	claimed, err := fx.q.ClaimPendingGroupOutbox(context.Background(), sqlc.ClaimPendingGroupOutboxParams{
		ID:         fx.outbox.ID,
		Now:        nullTime(time.Now().UTC()),
		LeaseUntil: nullTime(time.Now().UTC().Add(fx.d.leaseDuration)),
	})
	if err != nil {
		t.Fatalf("claim outbox: %v", err)
	}
	initialLease := claimed.LeaseUntil.Time
	stop := fx.d.startHeartbeat(context.Background(), "outbox", claimed.ID, func(ctx context.Context, until time.Time) (int64, error) {
		return fx.q.ExtendRunningGroupOutboxLease(ctx, sqlc.ExtendRunningGroupOutboxLeaseParams{
			ID:           claimed.ID,
			LeaseUntil:   nullTime(until),
			AttemptCount: claimed.AttemptCount,
		})
	}, nil)
	defer stop()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("outbox lease was not extended; initial lease %q", initialLease)
		case <-ticker.C:
			current, err := fx.q.GetGroupOutbox(context.Background(), claimed.ID)
			if err != nil {
				t.Fatalf("get outbox while waiting heartbeat: %v", err)
			}
			if !current.LeaseUntil.Time.IsZero() && !current.LeaseUntil.Time.Equal(initialLease) {
				return
			}
		}
	}
}

func TestGroupDispatcherHeartbeatDoesNotExtendAfterOwnershipLoss(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.leaseDuration = 2 * time.Second
	claimed, err := fx.q.ClaimPendingGroupOutbox(context.Background(), sqlc.ClaimPendingGroupOutboxParams{
		ID:         fx.outbox.ID,
		Now:        nullTime(time.Now().UTC()),
		LeaseUntil: nullTime(time.Now().UTC().Add(fx.d.leaseDuration)),
	})
	if err != nil {
		t.Fatalf("claim outbox: %v", err)
	}
	stop := fx.d.startHeartbeat(context.Background(), "outbox", claimed.ID, func(ctx context.Context, until time.Time) (int64, error) {
		return fx.q.ExtendRunningGroupOutboxLease(ctx, sqlc.ExtendRunningGroupOutboxLeaseParams{
			ID:           claimed.ID,
			LeaseUntil:   nullTime(until),
			AttemptCount: claimed.AttemptCount,
		})
	}, nil)
	defer stop()
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_outbox SET attempt_count = attempt_count + 1 WHERE id = $1`, claimed.ID); err != nil {
		t.Fatalf("simulate ownership loss: %v", err)
	}
	afterLoss, err := fx.q.GetGroupOutbox(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("get outbox after ownership loss: %v", err)
	}
	time.Sleep(time.Second)
	current, err := fx.q.GetGroupOutbox(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("get outbox after heartbeat: %v", err)
	}
	if !current.LeaseUntil.Time.Equal(afterLoss.LeaseUntil.Time) {
		t.Fatalf("stale heartbeat extended lease after ownership loss: before=%q after=%q", afterLoss.LeaseUntil.Time, current.LeaseUntil.Time)
	}
}

func TestGroupDispatcherCancelsDispatchAfterOwnershipLoss(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.leaseDuration = 2 * time.Second
	publisher := &blockingGroupPublisher{started: make(chan struct{}), release: make(chan struct{})}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	errC := make(chan error, 1)
	go func() { errC <- fx.d.ExecuteDispatch(context.Background(), dispatch) }()
	select {
	case <-publisher.started:
	case err := <-errC:
		t.Fatalf("execute dispatch returned before publisher started: %v", err)
	case <-time.After(time.Second):
		t.Fatal("publisher did not start")
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET attempt_count = attempt_count + 1 WHERE id = $1`, "d15a0000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("simulate dispatch ownership loss: %v", err)
	}
	select {
	case err := <-errC:
		if err == nil {
			t.Fatal("execute dispatch succeeded after ownership loss")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execute dispatch did not stop after ownership loss")
	}
	current, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after ownership loss: %v", err)
	}
	if current.Status == "completed" {
		t.Fatal("stale owner marked dispatch completed after ownership loss")
	}
}

func TestGroupDispatcherExtendsDispatchLeaseWhilePublishing(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.leaseDuration = 2 * time.Second
	publisher := &blockingGroupPublisher{started: make(chan struct{}), release: make(chan struct{})}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        "11111111-1111-1111-1111-111111111111",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}

	errC := make(chan error, 1)
	go func() { errC <- fx.d.ExecuteDispatch(context.Background(), dispatch) }()
	select {
	case <-publisher.started:
	case err := <-errC:
		t.Fatalf("execute dispatch returned before publisher started: %v", err)
	case <-time.After(time.Second):
		t.Fatal("publisher did not start")
	}
	running, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get running dispatch: %v", err)
	}
	initialLease := running.LeaseUntil.Time
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			close(publisher.release)
			t.Fatalf("dispatch lease was not extended; initial lease %q", initialLease)
		case <-ticker.C:
			current, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
			if err != nil {
				t.Fatalf("get dispatch while waiting heartbeat: %v", err)
			}
			if !current.LeaseUntil.Time.IsZero() && !current.LeaseUntil.Time.Equal(initialLease) {
				close(publisher.release)
				if err := <-errC; err != nil {
					t.Fatalf("execute dispatch after heartbeat: %v", err)
				}
				return
			}
		}
	}
}

func dispatchAgentsByMessage(t *testing.T, db *pgxpool.Pool, messageID string) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT agent_id FROM ctx_group_dispatch WHERE group_message_id = $1 ORDER BY agent_id`, messageID)
	if err != nil {
		t.Fatalf("query dispatch agents: %v", err)
	}
	defer rows.Close()
	var agents []string
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			t.Fatalf("scan dispatch agent: %v", err)
		}
		agents = append(agents, agentID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dispatch agents: %v", err)
	}
	return agents
}

func countAgentGroupMessages(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM ctx_group_message WHERE actor_type = 'agent'`).Scan(&count); err != nil {
		t.Fatalf("count agent messages: %v", err)
	}
	return count
}

func TestBufferGroupResponseSkipsWritebackOnErrEvent(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	stream := make(chan pkgchannel.Event, 2)
	stream <- pkgchannel.Event{Text: "partial"}
	stream <- pkgchannel.Event{Err: errors.New("boom")}
	close(stream)

	response := fx.d.bufferGroupResponse(context.Background(), &pkgchannel.ChatStream{Events: stream, SessionID: "session-1"})
	if response.complete {
		t.Fatalf("response.complete = true, want false")
	}
	assertNoAgentGroupMessages(t, fx.db)
}

func TestBufferGroupResponseSkipsWritebackAfterCancel(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	stream := make(chan pkgchannel.Event)
	go func() {
		stream <- pkgchannel.Event{Text: "partial"}
		cancel()
		stream <- pkgchannel.Event{Text: " ignored"}
		close(stream)
	}()
	response := fx.d.bufferGroupResponse(ctx, &pkgchannel.ChatStream{Events: stream, SessionID: "session-1"})
	if response.complete {
		t.Fatalf("response.complete = true, want false")
	}
	assertNoAgentGroupMessages(t, fx.db)
}

func TestBufferGroupResponseBuffersCompleteStream(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	stream := make(chan pkgchannel.Event, 2)
	stream <- pkgchannel.Event{Text: "complete"}
	close(stream)

	response := fx.d.bufferGroupResponse(context.Background(), &pkgchannel.ChatStream{Events: stream, SessionID: "session-1"})
	if !response.complete || response.text != "complete" || response.sessionID != "session-1" {
		t.Fatalf("response = %+v, want complete buffered response", response)
	}
	assertNoAgentGroupMessages(t, fx.db)
}

func setGroupNextSeq(t *testing.T, db *pgxpool.Pool, groupID string, seq int64) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `UPDATE ctx_group_state SET next_seq = $1 WHERE id = $2`, seq, groupID); err != nil {
		t.Fatalf("set group next seq: %v", err)
	}
}

// TestExecuteDispatchAbortClosureUsesSessionQueueGroupKey verifies the
// GroupPublishRequest.Abort closure ExecuteDispatch builds targets the exact
// same per-(agent,group) session queue slot chatDispatchUnqueued enqueues
// under. A wrong key would make a Discord Cancel click silently no-op
// instead of stopping the running turn.
func assertNoAgentGroupMessages(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM ctx_group_message WHERE actor_type = 'agent'`).Scan(&count); err != nil {
		t.Fatalf("count agent messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("agent group messages = %d, want 0", count)
	}
}
