package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type recordingGroupPublisher struct {
	err   error
	calls int
	texts []string
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

func createDispatchForGroupMessage(t *testing.T, db sqlc.DBTX, msg sqlc.CtxGroupMessage, id, agentID, groupID string, status string, leaseUntil pgtype.Timestamptz) {
	t.Helper()
	insertGroupDispatch(t, db, id, msg.ID, groupID, agentID, status, 0, leaseUntil)
}

// insertGroupDispatch writes a wake row straight to the table. Production only
// ever creates dispatches through CreateGroupWake/CreateGroupNudge, which pin
// status/attempt_count to a fresh row; recovery tests need arbitrary
// status/attempt/lease combinations that no production query can produce.
func insertGroupDispatch(t *testing.T, db sqlc.DBTX, id, groupMessageID, groupID, agentID, status string, attemptCount int64, leaseUntil pgtype.Timestamptz) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
INSERT INTO ctx_group_dispatch (
  id, group_message_id, group_id, agent_id, reply_channel_id, status, attempt_count, lease_until, next_attempt_at, last_error, trigger_seq, kind
)
VALUES ($1, $2, $3, $4, 'ch-1', $5, $6, $7, NULL, '',
  (SELECT seq FROM ctx_group_message WHERE id = $2), 'wake') ON CONFLICT DO NOTHING`,
		id, groupMessageID, groupID, agentID, status, attemptCount, leaseUntil); err != nil {
		t.Fatalf("insert dispatch %s: %v", id, err)
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

// poll runs on the goroutine that also reaps expired leases and drains the
// outbox. A full worker queue must cost latency, not those two.
func TestPollDoesNotBlockOnFullDispatchQueue(t *testing.T) {
	ctx := context.Background()
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.dispatchC = make(chan sqlc.CtxGroupDispatch)
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000061", GroupMessageID: fx.message.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- fx.d.poll(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("poll blocked on a full dispatch queue")
	}

	row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000061")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "pending" {
		t.Fatalf("deferred wake status = %q, want pending for the next poll", row.Status)
	}
}

func TestGroupDispatcherReapExpiredPublishedDispatchRequeuesFinalization(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	past := time.Now().UTC().Add(-time.Minute)
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-0000000000ff", fx.message.ID, fx.message.GroupID, "agent-1", "running", fx.d.maxAttempts, nullTime(past))
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
	if dispatch.Status != "pending" {
		t.Fatalf("dispatch status = %q, want pending finalization repair", dispatch.Status)
	}
	if !dispatch.NextAttemptAt.Valid || !dispatch.NextAttemptAt.Time.After(time.Now().UTC()) {
		t.Fatalf("next attempt = %+v, want heartbeat cancellation grace", dispatch.NextAttemptAt)
	}
}

func TestExpiredRequeueDoesNotOverwriteRenewedDispatchLease(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := context.Background()
	now := time.Now().UTC()
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-0000000000fd", fx.message.ID, fx.message.GroupID, "agent-1", "running", 1, nullTime(now.Add(-time.Minute)))
	row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-0000000000fd")
	if err != nil {
		t.Fatal(err)
	}
	if updated, err := fx.q.ExtendRunningGroupDispatchLease(ctx, sqlc.ExtendRunningGroupDispatchLeaseParams{
		ID: row.ID, AttemptCount: row.AttemptCount, LeaseUntil: nullTime(now.Add(time.Minute)),
	}); err != nil || updated != 1 {
		t.Fatalf("renew dispatch lease: rows=%d err=%v", updated, err)
	}
	updated, err := fx.q.RequeueExpiredGroupDispatch(ctx, sqlc.RequeueExpiredGroupDispatchParams{
		ID: row.ID, AttemptCount: row.AttemptCount, Now: nullTime(now),
		NextAttemptAt: nullTime(now.Add(time.Minute)), LastError: "stale reaper",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated != 0 {
		t.Fatalf("stale reaper updated %d rows, want 0 after heartbeat renewal", updated)
	}
	row, err = fx.q.GetGroupDispatch(ctx, row.ID)
	if err != nil || row.Status != "running" || !row.LeaseUntil.Time.After(now) {
		t.Fatalf("renewed dispatch = %+v, err=%v", row, err)
	}
}

// Claims expire read-side only, so without a reaper the table grows until the
// group is deleted. The grace period is what keeps a just-expired claim around
// for the agent that overran its lease, so the reaper must delete only the old
// ones.
func TestGroupDispatcherReapExpiredDeletesOnlyLongExpiredClaims(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := context.Background()
	now := time.Now().UTC()
	claims := []struct {
		id         string
		key        string
		leaseUntil time.Time
	}{
		{"c1a10000-0000-0000-0000-000000000001", "live", now.Add(time.Hour)},
		{"c1a10000-0000-0000-0000-000000000002", "just-expired", now.Add(-time.Minute)},
		{"c1a10000-0000-0000-0000-000000000003", "long-expired", now.Add(-2 * time.Hour)},
	}
	for _, claim := range claims {
		if _, err := fx.q.ClaimGroupWork(ctx, sqlc.ClaimGroupWorkParams{
			ID:           claim.id,
			GroupID:      fx.groupID,
			Key:          claim.key,
			OwnerAgentID: "agent-1",
			Note:         claim.key,
			LeaseUntil:   claim.leaseUntil,
		}); err != nil {
			t.Fatalf("seed claim %s: %v", claim.key, err)
		}
	}

	if err := fx.d.reapExpired(ctx); err != nil {
		t.Fatalf("reap expired: %v", err)
	}

	var remaining []string
	rows, err := fx.db.Query(ctx, `SELECT key FROM ctx_group_claim WHERE group_id = $1 ORDER BY key`, fx.groupID)
	if err != nil {
		t.Fatalf("list claims: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan claim: %v", err)
		}
		remaining = append(remaining, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate claims: %v", err)
	}
	if !slices.Equal(remaining, []string{"just-expired", "live"}) {
		t.Fatalf("claims after reap = %v, want the live and just-expired ones kept", remaining)
	}
}

// An accepted reply whose publish never happened still owes egress. Retiring it
// on lease expiry (the crash path) would strand the committed message forever,
// so the reaper must requeue it back onto the canonical-replay path instead.
func TestGroupDispatcherReapExpiredRequeuesAcceptedUnpublishedDispatch(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	past := time.Now().UTC().Add(-time.Minute)
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-0000000000fe", fx.message.ID, fx.message.GroupID, "agent-1", "running", fx.d.maxAttempts, nullTime(past))
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
	fx.d.publish.publishers.Register("ch-1", &recordingGroupPublisher{})

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if got := dispatchAgentsByMessage(t, fx.db, fx.message.ID); len(got) != 1 || got[0] != "agent-1" {
		t.Fatalf("dispatch agents = %v, want [agent-1]", got)
	}
}

// No deterministic rule addresses this wake, on a platform group with no fast
// model in sight. The floor is open: the turn runs and the model itself decides
// whether to speak or PASS.
func TestUnclassifiedWakeRunsTheTurn(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	row := sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-1", TriggerSeq: fx.message.Seq, Kind: "wake"}
	state, err := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	act, reason, degraded := fx.d.triageWake(context.Background(), row, fx.message, state, GroupOutboxEnvelope{})
	if !act || degraded || reason != "open_floor" {
		t.Fatalf("act=%v reason=%q degraded=%v", act, reason, degraded)
	}
}

func TestMentionSurvivesCoalescedAndHeldWake(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	mentionEnvelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{AgentID: "agent-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_outbox SET envelope = $1 WHERE group_message_id = $2`, mentionEnvelope, fx.message.ID); err != nil {
		t.Fatal(err)
	}

	// The newer wake is the one that survives coalescing. It has no mention of
	// agent-1, and the current envelope points at a peer, but the older human
	// mention is still unread because the superseded turn never committed its
	// cursor. This is also the HOLD successor shape: the trigger moved forward,
	// while the agent's read boundary did not.
	followUp := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000002", 2, eventlog.ActorHuman, "user-1", "why no answer?")
	setGroupNextSeq(t, fx.db, fx.groupID, followUp.Seq)
	state, err := fx.q.GetGroupStateByID(ctx, fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	row := sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-1", TriggerSeq: followUp.Seq, Kind: "wake"}
	act, reason, degraded := fx.d.triageWake(ctx, row, followUp, state, GroupOutboxEnvelope{
		Mentions: []pkgchannel.Mention{{AgentID: "agent-2"}},
	})
	if !act || degraded || reason != "mentioned" {
		t.Fatalf("act=%v reason=%q degraded=%v, want mentioned wake to act", act, reason, degraded)
	}
}

func TestConsumedMentionDoesNotCarryIntoLaterWake(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	mentionEnvelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{AgentID: "agent-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_outbox SET envelope = $1 WHERE group_message_id = $2`, mentionEnvelope, fx.message.ID); err != nil {
		t.Fatal(err)
	}
	if err := fx.q.UpsertIngestCursor(ctx, sqlc.UpsertIngestCursorParams{
		GroupID:  fx.groupID,
		Pipeline: memory.GroupIngestPipeline("agent-1"),
		LastSeq:  fx.message.Seq,
	}); err != nil {
		t.Fatal(err)
	}

	followUp := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000003", 2, eventlog.ActorHuman, "user-1", "another question")
	setGroupNextSeq(t, fx.db, fx.groupID, followUp.Seq)
	state, err := fx.q.GetGroupStateByID(ctx, fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	row := sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-1", TriggerSeq: followUp.Seq, Kind: "wake"}
	act, reason, degraded := fx.d.triageWake(ctx, row, followUp, state, GroupOutboxEnvelope{})
	if !act || degraded || reason != "open_floor" {
		t.Fatalf("act=%v reason=%q degraded=%v, want consumed mention to be ignored", act, reason, degraded)
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
	state, err := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	act, reason, degraded := fx.d.triageWake(context.Background(), sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-2", TriggerSeq: latest.Seq, Kind: "wake"}, latest.Message, state, GroupOutboxEnvelope{})
	if !act || degraded || reason != "open_floor" {
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
	state, err := fx.q.GetGroupStateByID(context.Background(), fx.groupID)
	if err != nil {
		t.Fatal(err)
	}
	row := sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-2", TriggerSeq: latest.Seq, Kind: "wake"}
	if act, reason, _ := fx.d.triageWake(context.Background(), row, latest.Message, state, GroupOutboxEnvelope{}); !act || reason != "open_floor" {
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
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000092", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000092")
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "new"}); err != nil {
		t.Fatal(err)
	}
	outcome, err := fx.d.acceptGroupResponse(context.Background(), row, groupResponse{text: "reply", complete: true}, memory.DeferredGroupTurn{Complete: true})
	wantGroupTurnStopped(t, outcome, err, groupTurnHeld, "freshness")
}

func TestFreshnessGateSerializesWithHumanIngest(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000096", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
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
	go func() {
		outcome, acceptErr := fx.d.acceptGroupResponse(ctx, row, groupResponse{text: "stale", complete: true}, memory.DeferredGroupTurn{Complete: true})
		if acceptErr == nil && outcome.Status != groupTurnHeld {
			acceptErr = fmt.Errorf("outcome=%s/%s, want held", outcome.Status, outcome.Reason)
		}
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
	if err := <-acceptC; err != nil {
		t.Fatalf("accept err=%v, want HOLD", err)
	}
}

func TestVerbatimDuplicateSilencedWithoutSpendingHold(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	// The dedup gate only sits downstream of an exhausted hold budget: while an
	// agent can still be held, a peer posting after the snapshot trips
	// freshness first. Spend the budget so this pins dedup and not its
	// neighbour.
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_state SET hold_limit = 0 WHERE id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000093", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000093")
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-2", Content: "same"}); err != nil {
		t.Fatal(err)
	}
	outcome, err := fx.d.acceptGroupResponse(context.Background(), row, groupResponse{text: "same", complete: true}, memory.DeferredGroupTurn{Complete: true})
	wantGroupTurnStopped(t, outcome, err, groupTurnSilent, "duplicate")
}

// Dedup is scoped to the causal chain: a phrase a peer used in an older chain
// must not make that phrase unpostable forever, since this gate ignores
// hold_limit and would discard the turn every single time.
func TestVerbatimDuplicateOutsideChainPostsThrough(t *testing.T) {
	ctx := context.Background()
	fx := newDispatcherFixture(t, "web", "{}")
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_state SET hold_limit = 0 WHERE id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	// Peer said it in the chain that the seq-1 human message opened.
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(ctx, fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-2", Content: "done"}); err != nil {
		t.Fatal(err)
	}
	// A new human message opens a new chain; this agent answers inside it.
	human := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000071", 3, eventlog.ActorHuman, "user-1", "and now?")
	setGroupNextSeq(t, fx.db, fx.groupID, human.Seq)
	createDispatchForGroupMessage(t, fx.db, human, "d15a0000-0000-0000-0000-000000000071", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000071")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := fx.d.acceptGroupResponse(ctx, row, groupResponse{text: "done", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if outcome.Status != groupTurnAccepted {
		t.Fatalf("outcome = %+v, want accepted: the echo belongs to an older chain", outcome)
	}
}

func TestHardCapSilencesAcceptedTurn(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_state SET max_replies_per_human_trigger = 0 WHERE id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000107", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000107")
	outcome, err := fx.d.acceptGroupResponse(ctx, row, groupResponse{text: "reply", complete: true}, memory.DeferredGroupTurn{Complete: true})
	wantGroupTurnStopped(t, outcome, err, groupTurnSilent, "hard_cap")
	// silent, not held: the cap is terminal, so this row must not come back.
	stopped, _ := fx.q.GetGroupDispatch(ctx, row.ID)
	if stopped.Status != "silent" || stopped.LastError != "hard_cap" {
		t.Fatalf("dispatch=%s/%s, want silent/hard_cap", stopped.Status, stopped.LastError)
	}
}

func TestHoldLimitPostsThrough(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_state SET hold_limit = 0 WHERE id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000094", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000094")
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.d.acceptGroupResponse(context.Background(), row, groupResponse{text: "reply", complete: true}, memory.DeferredGroupTurn{Complete: true}); err != nil {
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
	createDispatchForGroupMessage(t, fx.db, newHuman.Message, "d15a0000-0000-0000-0000-000000000098", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(ctx, fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-3", Content: "newer peer"}); err != nil {
		t.Fatal(err)
	}
	row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000098")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := fx.d.acceptGroupResponse(ctx, row, groupResponse{text: "reply", complete: true}, memory.DeferredGroupTurn{Complete: true})
	wantGroupTurnStopped(t, outcome, err, groupTurnHeld, "freshness")
}

// A held nudge already spends hold budget, so the claim gate must see it too.
// Ignoring it would let a wake re-run against a snapshot the agent was shown.
func TestHeldNudgeGatesWakeClaim(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	if err := fx.q.CreateGroupNudge(ctx, sqlc.CreateGroupNudgeParams{ID: "d15a0000-0000-0000-0000-000000000102", GroupMessageID: fx.message.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET status='held', held_up_to_seq=3 WHERE id=$1`, "d15a0000-0000-0000-0000-000000000102"); err != nil {
		t.Fatal(err)
	}
	second := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000102", 2, eventlog.ActorAgent, "agent-2", "peer one")
	setGroupNextSeq(t, fx.db, fx.groupID, second.Seq)
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000103", GroupMessageID: second.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fx.d.claimDispatch(ctx, sqlc.CtxGroupDispatch{Status: "pending", Kind: "wake", GroupID: fx.groupID, AgentID: "agent-1"}); err != nil || ok {
		t.Fatalf("wake claimed past a held nudge: ok=%v err=%v", ok, err)
	}
	third := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000103", 3, eventlog.ActorAgent, "agent-2", "peer two")
	setGroupNextSeq(t, fx.db, fx.groupID, third.Seq)
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000104", GroupMessageID: third.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fx.d.claimDispatch(ctx, sqlc.CtxGroupDispatch{Status: "pending", Kind: "wake", GroupID: fx.groupID, AgentID: "agent-1"}); err != nil || !ok {
		t.Fatalf("covered successor refused: ok=%v err=%v", ok, err)
	}
}

// The stalled-work nudge writes a system row. Counting it as peer activity lets
// recovery invalidate the very turn it is waiting for: the agent finishes, is
// held against the nudge, and its work is discarded.
func TestSystemMessageDoesNotTripFreshnessHold(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000051", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000051")
	if err != nil {
		t.Fatal(err)
	}
	nudge := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000051", 2, eventlog.ActorSystem, "nudge", "agent-1, please continue.")
	setGroupNextSeq(t, fx.db, fx.groupID, nudge.Seq)
	outcome, err := fx.d.acceptGroupResponse(ctx, row, groupResponse{text: "the report is done", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatalf("accept after nudge: %v", err)
	}
	if outcome.Status != groupTurnAccepted {
		t.Fatalf("outcome = %+v, want accepted despite the system row", outcome)
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
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000102", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	first, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000102")
	peer, err := eventlog.NewStore(fx.db).AppendToGroup(ctx, fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "newer"})
	if err != nil {
		t.Fatal(err)
	}
	firstOutcome, err := fx.d.acceptGroupResponse(ctx, first, groupResponse{text: "first", complete: true}, memory.DeferredGroupTurn{Complete: true})
	wantGroupTurnStopped(t, firstOutcome, err, groupTurnHeld, "freshness")
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
	secondOutcome, err := fx.d.acceptGroupResponse(ctx, successor, groupResponse{text: "second", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil || secondOutcome.Status != groupTurnAccepted {
		t.Fatalf("same covered snapshot held again: outcome=%s/%s err=%v", secondOutcome.Status, secondOutcome.Reason, err)
	}
}

func TestHoldCommitsDeferredTurnWithoutFinalReply(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	var committed memory.DeferredGroupTurn
	fx.d.SetGroupTurnCommitter(groupTurnCommitterFunc(func(_ context.Context, _ *sqlc.Queries, turn memory.DeferredGroupTurn) error {
		committed = turn
		return nil
	}))
	fx.d.publish.publishers.Register("ch-1", &recordingGroupPublisher{})
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000104", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
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
	if !committed.Complete {
		t.Fatal("held turn did not commit its deferred history")
	}
}

func TestPendingPostFinalFailureRequeuesHeldPeers(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	ctx := context.Background()
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000105", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	post, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000105")
	accepted, err := fx.d.acceptGroupResponse(ctx, post, groupResponse{text: "pending delivery", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	post.ResultMessageID = accepted.Accepted.Message.ID
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000106", "agent-2", fx.groupID, "running", pgtype.Timestamptz{})
	peer, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000106")
	peerOutcome, err := fx.d.acceptGroupResponse(ctx, peer, groupResponse{text: "peer reply", complete: true}, memory.DeferredGroupTurn{Complete: true})
	wantGroupTurnStopped(t, peerOutcome, err, groupTurnHeld, "freshness")
	if err := fx.d.publish.failAcceptedPublish(ctx, post, errors.New("platform down")); err == nil {
		t.Fatal("final publish failure must be reported")
	}
	peer, _ = fx.q.GetGroupDispatch(ctx, peer.ID)
	if peer.Status != "pending" || peer.HeldUpToSeq.Valid {
		t.Fatalf("held peer=%+v, want requeued with cleared gate", peer)
	}
}

func TestSilentWakeSupersedesOlderWakes(t *testing.T) {
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
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_state SET max_replies_per_human_trigger = 0 WHERE id = $1`, fx.groupID); err != nil {
		t.Fatal(err)
	}
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

// Web is a platform whose publisher does nothing, not a surface with its own
// delivery model: a web reply is born undelivered, the (noop) publish step marks
// it delivered, and the same successor outbox wakes its peers.
func TestWebAgentReplyTraversesTheSameDeliveryLifecycle(t *testing.T) {
	ctx := context.Background()
	fx := newDispatcherFixture(t, "web", "{}")
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000095", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000095")
	state, _ := fx.q.GetGroupStateByID(ctx, fx.groupID)
	result, err := fx.d.acceptGroupResponse(ctx, row, groupResponse{text: "peer reply", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted.Message.DeliveryState != "pending" {
		t.Fatalf("delivery_state at accept = %q, want pending", result.Accepted.Message.DeliveryState)
	}
	row, err = fx.q.GetGroupDispatch(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := fx.d.publish.publisherFor(state, row)
	if err != nil {
		t.Fatalf("web publisher: %v", err)
	}
	if err := fx.d.publishAccepted(ctx, publishJob{row: row, trigger: fx.message, state: state, publisher: publisher, response: groupResponse{text: "peer reply", complete: true}}); err != nil {
		t.Fatalf("publish accepted: %v", err)
	}
	message, err := fx.q.GetGroupMessage(ctx, result.Accepted.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if message.DeliveryState != "delivered" {
		t.Fatalf("delivery_state after publish = %q, want delivered", message.DeliveryState)
	}
	outbox, err := fx.q.GetGroupOutboxByMessage(ctx, result.Accepted.Message.ID)
	if err != nil || outbox.Status != "pending" {
		t.Fatalf("agent outbox=%+v err=%v", outbox, err)
	}
	if err := fx.d.ProcessOutbox(ctx, outbox); err != nil {
		t.Fatalf("process agent reply outbox: %v", err)
	}
	if got := dispatchAgentsByMessage(t, fx.db, result.Accepted.Message.ID); len(got) != 1 || got[0] != "agent-2" {
		t.Fatalf("woken peers = %v, want [agent-2]", got)
	}
}

// Agent-to-agent collaboration must not be a web-only capability: no platform
// echoes a bot's own message back through ingest, so the successor outbox is
// the only thing that carries an agent post to its peers.
func TestAgentReplyCreatesOutboxOnPlatformGroup(t *testing.T) {
	ctx := context.Background()
	fx := newDispatcherFixture(t, "telegram", "{}")
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000096", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000096")
	result, err := fx.d.acceptGroupResponse(ctx, row, groupResponse{text: "peer reply", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	row, err = fx.q.GetGroupDispatch(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.d.publish.markAcceptedPublished(ctx, row); err != nil {
		t.Fatalf("mark accepted published: %v", err)
	}
	outbox, err := fx.q.GetGroupOutboxByMessage(ctx, result.Accepted.Message.ID)
	if err != nil || outbox.Status != "pending" {
		t.Fatalf("platform agent outbox=%+v err=%v", outbox, err)
	}
	message, err := fx.q.GetGroupMessage(ctx, result.Accepted.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if message.DeliveryState != "delivered" {
		t.Fatalf("delivery_state = %q, want delivered", message.DeliveryState)
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
	fx.d.publish.publishers.Register("ch-1", &recordingGroupPublisher{})

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
	fx.d.publish.publishers.Register("ch-1", publisher)
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000001", fx.message.ID, "11111111-1111-1111-1111-111111111111", "agent-1", "pending", 0, pgtype.Timestamptz{})

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
	fx.d.publish.publishers.Register("ch-1", publisher)
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000001", fx.message.ID, "11111111-1111-1111-1111-111111111111", "agent-1", "pending", 0, pgtype.Timestamptz{})
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
	fx.d.publish.publishers.Register("ch-1", publisher)
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000031", fx.message.ID, fx.groupID, "agent-1", "pending", 0, pgtype.Timestamptz{})
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

// A publisher that returns an error told us the outcome; a crash does not. The
// start marker is what separates the two on recovery, so it must be cleared on
// the first and survive the second.
func TestPublishStartMarkerClearedOnReturnedError(t *testing.T) {
	ctx := context.Background()
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{err: errors.New("boom")}
	fx.d.publish.publishers.Register("ch-1", publisher)
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000041", fx.message.ID, fx.groupID, "agent-1", "pending", 0, pgtype.Timestamptz{})
	dispatch, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000041")
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.d.ExecuteDispatch(ctx, dispatch); err == nil {
		t.Fatal("expected publisher error")
	}
	dispatch, err = fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000041")
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.PublishStartedAt.Valid {
		t.Fatalf("publish_started_at = %v, want cleared after a returned error", dispatch.PublishStartedAt)
	}

	publisher.err = nil
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET next_attempt_at = NULL WHERE id = $1`, dispatch.ID); err != nil {
		t.Fatal(err)
	}
	dispatch, err = fx.q.GetGroupDispatch(ctx, dispatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.d.ExecuteDispatch(ctx, dispatch); err != nil {
		t.Fatalf("retry dispatch: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(ctx, dispatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !dispatch.PublishStartedAt.Valid || !dispatch.PublishedAt.Valid {
		t.Fatalf("started=%v published=%v, want both recorded after delivery", dispatch.PublishStartedAt.Valid, dispatch.PublishedAt.Valid)
	}
}

func TestGroupDispatcherWritebackFailureLeavesResultEmptyAndRequeues(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publish.publishers.Register("ch-1", publisher)
	chatCalls := 0
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: true})
		}
		return textStream("ok"), nil
	}
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000001", fx.message.ID, "11111111-1111-1111-1111-111111111111", "agent-1", "pending", 0, pgtype.Timestamptz{})
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
	fx.d.chat = fx.d.chats.chatDispatch // the cursor guard lives on the real chat path
	publisher := &recordingGroupPublisher{}
	fx.d.publish.publishers.Register("ch-1", publisher)
	if err := fx.q.UpsertIngestCursor(context.Background(), sqlc.UpsertIngestCursorParams{
		GroupID:  "11111111-1111-1111-1111-111111111111",
		Pipeline: memory.GroupIngestPipeline("agent-1"),
		LastSeq:  fx.message.Seq,
	}); err != nil {
		t.Fatalf("seed rotation boundary: %v", err)
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000001",
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
	fx.d.publish.publishers.Register("ch-1", publisher)
	chatCalls := 0
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: true})
		}
		return textStream("ok"), nil
	}
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000001", fx.message.ID, "11111111-1111-1111-1111-111111111111", "agent-1", "running", 1, pgtype.Timestamptz{})
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
	restarted.publish.publishers.Register("ch-1", publisher)
	restarted.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		t.Fatal("restart replay must not run chat")
		return nil, nil
	}
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000001", fx.message.ID, fx.groupID, "agent-1", "running", 1, pgtype.Timestamptz{})
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
	fx.d.publish.publishers.Register("ch-1", publisher)
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000001", fx.message.ID, "11111111-1111-1111-1111-111111111111", "agent-1", "pending", 0, pgtype.Timestamptz{})
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
	fx.d.publish.publishers.Register("ch-1", &recordingGroupPublisher{err: boom})
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000001", fx.message.ID, "11111111-1111-1111-1111-111111111111", "agent-1", "pending", 0, pgtype.Timestamptz{})
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
	fx.d.publish.publishers.Register("ch-1", publisher)
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: false})
		}
		return textStream("partial"), nil
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
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

func TestAcceptTxRollbackAnnouncesNoMessage(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	hub := NewGroupEventHub()
	fx.d.SetGroupEventHub(hub)
	fx.d.SetGroupTurnCommitter(groupTurnCommitterFunc(func(context.Context, *sqlc.Queries, memory.DeferredGroupTurn) error {
		return errors.New("injected memory failure")
	}))
	follow, cancel := hub.Subscribe(fx.groupID)
	defer cancel()
	fx.d.publish.publishers.Register("ch-1", &recordingGroupPublisher{})
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
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
	// Presence frames are fine and expected (the turn did run, then failed); a
	// canonical message projection is what a rolled-back accept must never emit.
	for {
		select {
		case event, alive := <-follow:
			if !alive {
				return
			}
			if event.Turn == nil {
				t.Fatalf("rollback announced a message projection: %+v", event)
			}
		case <-time.After(50 * time.Millisecond):
			return
		}
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
	stop := fx.d.startHeartbeat(context.Background(), "outbox", claimed.ID, claimed.LeaseUntil.Time, func(ctx context.Context, until time.Time) (int64, error) {
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
	stop := fx.d.startHeartbeat(context.Background(), "outbox", claimed.ID, claimed.LeaseUntil.Time, func(ctx context.Context, until time.Time) (int64, error) {
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

func TestGroupDispatcherHeartbeatCancelsBeforeUnconfirmedLeaseExpires(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.leaseDuration = 90 * time.Millisecond
	lost := make(chan struct{})
	stop := fx.d.startHeartbeat(context.Background(), "dispatch", "dispatch-1", time.Now().UTC().Add(fx.d.leaseDuration), func(context.Context, time.Time) (int64, error) {
		return 0, errors.New("database unavailable")
	}, func() { close(lost) })
	defer stop()
	select {
	case <-lost:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("heartbeat kept working past its last proven lease")
	}
}

func TestGroupDispatcherCancelsDispatchAfterOwnershipLoss(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.leaseDuration = 2 * time.Second
	publisher := &blockingGroupPublisher{started: make(chan struct{}), release: make(chan struct{})}
	fx.d.publish.publishers.Register("ch-1", publisher)
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000001", fx.message.ID, "11111111-1111-1111-1111-111111111111", "agent-1", "pending", 0, pgtype.Timestamptz{})
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
	fx.d.publish.publishers.Register("ch-1", publisher)
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000001", fx.message.ID, "11111111-1111-1111-1111-111111111111", "agent-1", "pending", 0, pgtype.Timestamptz{})
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

// wantGroupTurnStopped asserts which backstop stopped the turn. The reason is
// what the UI shows, so pinning only "not accepted" would let one gate answer
// for another.
func wantGroupTurnStopped(t *testing.T, outcome groupAcceptOutcome, err error, status groupAcceptStatus, reason string) {
	t.Helper()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if outcome.Status != status || outcome.Reason != reason {
		t.Fatalf("outcome=%s/%s, want %s/%s", outcome.Status, outcome.Reason, status, reason)
	}
}

func TestTriggerRenderedAsTranscriptLine(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()

	human := fx.message // seq 1, human "user-1"
	blocks := fx.d.chats.triggerContent(ctx, fx.groupID, human)
	text, ok := blocks[0].(ai.TextContent)
	if !ok || text.Text != "[seq:1 user-1]: hello" {
		t.Fatalf("human trigger = %#v, want a labelled transcript line", blocks[0])
	}

	peer := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-0000000000f1", 2, eventlog.ActorAgent, "agent-1", "on it")
	blocks = fx.d.chats.triggerContent(ctx, fx.groupID, peer)
	text, ok = blocks[0].(ai.TextContent)
	if !ok || text.Text != "[seq:2 @Agent One]: on it" {
		t.Fatalf("peer trigger = %#v, want [seq:2 @Agent One]: on it", blocks[0])
	}

	nudge := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-0000000000f2", 3, eventlog.ActorSystem, "nudge", "@Agent One, please continue.")
	blocks = fx.d.chats.triggerContent(ctx, fx.groupID, nudge)
	text, ok = blocks[0].(ai.TextContent)
	if !ok || text.Text != "[seq:3 system]: @Agent One, please continue." {
		t.Fatalf("nudge trigger = %#v, want a system-labelled line", blocks[0])
	}
}

func TestTriggerLabelSurvivesImageOnlyMessage(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	msg := sqlc.CtxGroupMessage{
		Seq: 9, ActorType: string(eventlog.ActorAgent), ActorID: "agent-1",
		ContentBlocks: []byte(`[{"kind":"image","data":"aGk=","mime_type":"image/png"}]`),
	}
	blocks := fx.d.chats.triggerContent(context.Background(), fx.groupID, msg)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v, want label + image", blocks)
	}
	text, ok := blocks[0].(ai.TextContent)
	if !ok || text.Text != "[seq:9 @Agent One]:" {
		t.Fatalf("label block = %#v", blocks[0])
	}
	if !ai.HasImage(blocks) {
		t.Fatal("image block dropped")
	}
}

// A model that read the group and has nothing to add says so with PASS. It must
// leave no post behind, and the group must not see the fact that it thought.
func TestModelPassRetiresSilentWithoutPost(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	publisher := &recordingGroupPublisher{}
	fx.d.publish.publishers.Register("ch-1", publisher)
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{Complete: true})
		}
		return textStream("PASS"), nil
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-0000000001a1", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-0000000001a1")
	if err := fx.d.ExecuteDispatch(ctx, row); err != nil {
		t.Fatalf("a pass is a normal outcome, got %v", err)
	}
	row, _ = fx.q.GetGroupDispatch(ctx, row.ID)
	if row.Status != "silent" || row.LastError != "model_pass" {
		t.Fatalf("dispatch = %q/%q, want silent/model_pass", row.Status, row.LastError)
	}
	var posts int
	if err := fx.db.QueryRow(ctx, `SELECT count(*) FROM ctx_group_message WHERE group_id=$1 AND actor_type='agent'`, fx.groupID).Scan(&posts); err != nil {
		t.Fatal(err)
	}
	if posts != 0 {
		t.Fatalf("agent posts = %d, want none", posts)
	}
}

// Passing still means the agent read the group: the peer rows it was shown and
// its ingest cursor commit, or it re-reads them forever. Its own empty turn
// does not.
func TestModelPassCommitsReadContextWithoutOwnRows(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	var committed memory.DeferredGroupTurn
	var commits int
	fx.d.SetGroupTurnCommitter(groupTurnCommitterFunc(func(_ context.Context, _ *sqlc.Queries, turn memory.DeferredGroupTurn) error {
		committed, commits = turn, commits+1
		return nil
	}))
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{
				Complete:         true,
				TriggerSeq:       1,
				InjectedPeerRows: []ai.Message{ai.UserMessage{Content: "[seq:1 user-1]: hello"}},
				OwnRows:          []ai.Message{ai.AssistantMessage{}},
			})
		}
		return textStream("  pass  "), nil
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-0000000001a2", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-0000000001a2")
	if err := fx.d.ExecuteDispatch(ctx, row); err != nil {
		t.Fatal(err)
	}
	if commits != 1 {
		t.Fatalf("commits = %d, want 1", commits)
	}
	if len(committed.InjectedPeerRows) != 1 || committed.TriggerSeq != 1 {
		t.Fatalf("committed turn = %+v, want the read peer rows and cursor", committed)
	}
	if len(committed.OwnRows) != 0 {
		t.Fatalf("committed own rows = %+v, want none", committed.OwnRows)
	}
}

func TestModelPassRecognition(t *testing.T) {
	passes := []string{"PASS", "pass", "  PASS  ", "`PASS`", "**PASS**", "PASS.", "```\nPASS\n```", "```text\nPASS\n```", "", "   "}
	for _, text := range passes {
		if !isModelPass(text) {
			t.Errorf("isModelPass(%q) = false, want true", text)
		}
	}
	// A reply that merely starts with the word is a reply.
	replies := []string{"PASS, but check the logs", "I'll pass this to Anna", "passing on the deploy: it needs review"}
	for _, text := range replies {
		if isModelPass(text) {
			t.Errorf("isModelPass(%q) = true, want false", text)
		}
	}
}

// A pass is a decision not to speak, not a decision to forget: an agent that
// claimed work and then passed must still remember the claim it holds.
func TestModelPassKeepsToolRowsAndDropsOnlyThePassReply(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	var committed memory.DeferredGroupTurn
	fx.d.SetGroupTurnCommitter(groupTurnCommitterFunc(func(_ context.Context, _ *sqlc.Queries, turn memory.DeferredGroupTurn) error {
		committed = turn
		return nil
	}))
	toolCall := ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "call-1", Name: "group_claim"}}}
	toolResult := ai.ToolResultMessage{ToolCallID: "call-1", ToolName: "group_claim", Content: []ai.ContentBlock{ai.TextContent{Text: "claimed"}}}
	fx.d.chat = func(ctx context.Context, _ sqlc.CtxGroupDispatch, _ sqlc.CtxGroupMessage, _ sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		if sink, ok := memory.GroupTurnSinkFrom(ctx); ok {
			sink.Deliver(memory.DeferredGroupTurn{
				Complete:   true,
				TriggerSeq: 1,
				OwnRows: []ai.Message{
					ai.UserMessage{Content: "[seq:1 user-1]: hello"},
					toolCall,
					toolResult,
					ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "PASS"}}},
				},
			})
		}
		return textStream("PASS"), nil
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-0000000001a3", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	row, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-0000000001a3")
	if err := fx.d.ExecuteDispatch(ctx, row); err != nil {
		t.Fatal(err)
	}
	if len(committed.OwnRows) != 3 {
		t.Fatalf("committed own rows = %+v, want the trigger and the tool pair", committed.OwnRows)
	}
	if _, ok := committed.OwnRows[2].(ai.ToolResultMessage); !ok {
		t.Fatalf("last committed row = %T, want the tool result", committed.OwnRows[2])
	}
}

// Giving up on a row that already carries an accepted reply is not the same as
// giving up on a wake. The message is committed and readable by peers, so it
// must be marked undelivered and the peers it held released; otherwise it sits
// 'pending' forever and holds them with it.
func TestFinalFailureOnAcceptedRowReleasesHeldPeers(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	ctx := context.Background()
	fx.d.maxAttempts = 1
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-0000000001a4", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	post, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-0000000001a4")
	if _, err := fx.d.acceptGroupResponse(ctx, post, groupResponse{text: "pending delivery", complete: true}, memory.DeferredGroupTurn{Complete: true}); err != nil {
		t.Fatal(err)
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-0000000001a5", "agent-2", fx.groupID, "running", pgtype.Timestamptz{})
	peer, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-0000000001a5")
	peerOutcome, err := fx.d.acceptGroupResponse(ctx, peer, groupResponse{text: "peer reply", complete: true}, memory.DeferredGroupTurn{Complete: true})
	wantGroupTurnStopped(t, peerOutcome, err, groupTurnHeld, "freshness")

	// The reply channel is gone by the time egress is retried: publisherFor
	// fails before the accepted result can be replayed.
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET status = 'pending', attempt_count = 1 WHERE id = $1`, post.ID); err != nil {
		t.Fatal(err)
	}
	post, _ = fx.q.GetGroupDispatch(ctx, post.ID)
	if err := fx.d.ExecuteDispatch(ctx, post); err == nil {
		t.Fatal("missing publisher must be reported")
	}
	post, _ = fx.q.GetGroupDispatch(ctx, post.ID)
	if post.Status != "failed" {
		t.Fatalf("dispatch status = %q, want failed", post.Status)
	}
	result, err := fx.q.GetGroupMessage(ctx, post.ResultMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeliveryState != "failed" {
		t.Fatalf("delivery state = %q, want failed", result.DeliveryState)
	}
	peer, _ = fx.q.GetGroupDispatch(ctx, peer.ID)
	if peer.Status != "pending" || peer.HeldUpToSeq.Valid {
		t.Fatalf("held peer = %+v, want requeued with cleared gate", peer)
	}
}

// drainTurnStates collects the turn states announced on a hub subscription until
// the feed goes quiet. Message frames are ignored: this is about presence only.
func drainTurnStates(t *testing.T, follow <-chan GroupEvent) []string {
	t.Helper()
	states := []string{}
	for {
		select {
		case event, alive := <-follow:
			if !alive {
				return states
			}
			if event.Turn != nil {
				states = append(states, event.Turn.State)
			}
		case <-time.After(300 * time.Millisecond):
			return states
		}
	}
}

// TestAcceptedTurnAnnouncesRunningThenDone pins the presence lifecycle a browser
// depends on: the dispatcher opens the turn before the model runs, and the
// publish driver closes it once the reply is delivered.
func TestAcceptedTurnAnnouncesRunningThenDone(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	hub := NewGroupEventHub()
	fx.d.SetGroupEventHub(hub)
	follow, cancel := hub.Subscribe(fx.groupID)
	defer cancel()
	fx.d.publish.publishers.Register("ch-1", &recordingGroupPublisher{})
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err != nil {
		t.Fatalf("execute dispatch: %v", err)
	}
	if got := drainTurnStates(t, follow); len(got) != 2 || got[0] != "running" || got[1] != "done" {
		t.Fatalf("turn states = %v, want [running done]", got)
	}
}

// TestGatedWakeAnnouncesSilentWithoutRunning proves presence is only claimed for
// a turn that actually runs: a wake the gate stops never lights the badge.
func TestGatedWakeAnnouncesSilentWithoutRunning(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	hub := NewGroupEventHub()
	fx.d.SetGroupEventHub(hub)
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		t.Fatal("a gated wake must not start a model turn")
		return nil, nil
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_state SET max_agent_posts_per_minute = 1 WHERE id = $1`, fx.groupID); err != nil {
		t.Fatalf("tighten rate cap: %v", err)
	}
	if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-1", Content: "already posted"}); err != nil {
		t.Fatalf("append agent post: %v", err)
	}
	follow, cancel := hub.Subscribe(fx.groupID)
	defer cancel()
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err != nil {
		t.Fatalf("execute gated dispatch: %v", err)
	}
	if got := drainTurnStates(t, follow); len(got) != 1 || got[0] != "silent" {
		t.Fatalf("turn states = %v, want [silent]", got)
	}
}

// TestCompensationReplayAnnouncesDoneWithoutRunning proves the egress-compensation
// path stays presence-quiet on the way in: the reply already exists, republishing
// is immediate, and announcing 'running' there would flash a badge for a turn no
// model is taking. The delivery still closes the turn with 'done'.
func TestCompensationReplayAnnouncesDoneWithoutRunning(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	hub := NewGroupEventHub()
	fx.d.SetGroupEventHub(hub)
	fx.d.publish.publishers.Register("ch-1", &recordingGroupPublisher{})
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		t.Fatal("compensation replay must not run chat")
		return nil, nil
	}
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-000000000001", fx.message.ID, fx.groupID, "agent-1", "running", 1, pgtype.Timestamptz{})
	accepted, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{
		ActorType: eventlog.ActorAgent, ActorID: "agent-1", Content: "canonical text",
	})
	if err != nil {
		t.Fatalf("append accepted result: %v", err)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = $1 WHERE id = $2`, accepted.Message.ID, "d15a0000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("set result marker: %v", err)
	}
	follow, cancel := hub.Subscribe(fx.groupID)
	defer cancel()
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch); err != nil {
		t.Fatalf("replay accepted reply: %v", err)
	}
	if got := drainTurnStates(t, follow); len(got) != 1 || got[0] != "done" {
		t.Fatalf("turn states = %v, want [done]", got)
	}
}

func TestWakeAndNudgeClaimsAreMutuallyExclusive(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	fx.d.leaseDuration = time.Minute
	if err := fx.q.CreateGroupWake(ctx, sqlc.CreateGroupWakeParams{ID: "d15a0000-0000-0000-0000-000000000201", GroupMessageID: fx.message.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	nudgeMessage := createGroupMessage(t, fx.q, fx.groupID, "a1a1a1a1-0000-0000-0000-000000000202", 2, eventlog.ActorSystem, "nudge", "continue")
	if err := fx.q.CreateGroupNudge(ctx, sqlc.CreateGroupNudgeParams{ID: "d15a0000-0000-0000-0000-000000000202", GroupMessageID: nudgeMessage.ID, GroupID: fx.groupID, AgentID: "agent-1", ReplyChannelID: "ch-1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fx.d.claimDispatch(ctx, sqlc.CtxGroupDispatch{Status: "pending", Kind: "wake", GroupID: fx.groupID, AgentID: "agent-1"}); err != nil || !ok {
		t.Fatalf("claim wake: ok=%v err=%v", ok, err)
	}
	nudge, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000202")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fx.d.claimDispatch(ctx, nudge); err != nil || ok {
		t.Fatalf("nudge claimed beside a live wake: ok=%v err=%v", ok, err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET lease_until = now() - interval '1 minute' WHERE status = 'running' AND group_id = $1 AND agent_id = $2`, fx.groupID, "agent-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fx.d.claimDispatch(ctx, nudge); err != nil || ok {
		t.Fatalf("nudge claimed before reaper retired expired owner: ok=%v err=%v", ok, err)
	}
}

func TestStoppedTurnsCommitHistoryToolTraceAndCursor(t *testing.T) {
	cases := []struct {
		name       string
		prepare    func(t *testing.T, fx dispatcherFixture)
		wantStatus groupAcceptStatus
		wantReason string
	}{
		{
			name: "freshness",
			prepare: func(t *testing.T, fx dispatcherFixture) {
				if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorHuman, ActorID: "user-2", Content: "newer"}); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus: groupTurnHeld, wantReason: "freshness",
		},
		{
			name: "duplicate",
			prepare: func(t *testing.T, fx dispatcherFixture) {
				if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_state SET hold_limit = 0 WHERE id = $1`, fx.groupID); err != nil {
					t.Fatal(err)
				}
				if _, err := eventlog.NewStore(fx.db).AppendToGroup(context.Background(), fx.groupID, eventlog.GroupMessage{ActorType: eventlog.ActorAgent, ActorID: "agent-2", Content: "stale reply"}); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus: groupTurnSilent, wantReason: "duplicate",
		},
		{
			name: "hard_cap",
			prepare: func(t *testing.T, fx dispatcherFixture) {
				if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_state SET max_replies_per_human_trigger = 0 WHERE id = $1`, fx.groupID); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus: groupTurnSilent, wantReason: "hard_cap",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newDispatcherFixture(t, "web", "{}")
			ctx := context.Background()
			var committed memory.DeferredGroupTurn
			fx.d.SetGroupTurnCommitter(groupTurnCommitterFunc(func(_ context.Context, _ *sqlc.Queries, turn memory.DeferredGroupTurn) error {
				committed = turn
				return nil
			}))
			createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000203", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
			row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000203")
			if err != nil {
				t.Fatal(err)
			}
			tc.prepare(t, fx)
			outcome, err := fx.d.acceptGroupResponse(ctx, row, groupResponse{text: "stale reply", complete: true}, memory.DeferredGroupTurn{
				Complete: true, TriggerSeq: fx.message.Seq,
				InjectedPeerRows: []ai.Message{ai.UserMessage{Content: "[seq:1 user-1]: hello"}},
				OwnRows: []ai.Message{
					ai.AssistantMessage{Content: []ai.ContentBlock{ai.ToolCall{ID: "call-1", Name: "group_claim"}}},
					ai.ToolResultMessage{ToolCallID: "call-1", ToolName: "group_claim", Content: []ai.ContentBlock{ai.TextContent{Text: "claimed"}}},
					ai.AssistantMessage{Content: []ai.ContentBlock{ai.ThinkingContent{Thinking: "stale reasoning"}, ai.TextContent{Text: "stale reply"}}},
				},
			})
			wantGroupTurnStopped(t, outcome, err, tc.wantStatus, tc.wantReason)
			if committed.TriggerSeq != fx.message.Seq || len(committed.InjectedPeerRows) != 1 {
				t.Fatalf("committed read boundary = %+v, want trigger cursor and history", committed)
			}
			if len(committed.OwnRows) != 2 {
				t.Fatalf("committed own rows = %+v, want tool trace without stale assistant reply", committed.OwnRows)
			}
			if _, ok := committed.OwnRows[1].(ai.ToolResultMessage); !ok {
				t.Fatalf("last committed row = %T, want paired tool result", committed.OwnRows[1])
			}
		})
	}
}

func TestFailAcceptedPublishRequeuesOnlyCausallyHeldPeers(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	ctx := context.Background()
	for _, member := range []struct{ agent, channel string }{{"agent-2", "ch-2"}, {"agent-3", "ch-3"}, {"agent-4", "ch-4"}} {
		addFixtureAgent(t, fx, member.agent, member.channel)
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000204", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	post, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000204")
	accepted, err := fx.d.acceptGroupResponse(ctx, post, groupResponse{text: "pending delivery", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	post.ResultMessageID = accepted.Accepted.Message.ID
	for _, peer := range []struct {
		id, agent string
		message   sqlc.CtxGroupMessage
		heldUpTo  int64
	}{
		{"d15a0000-0000-0000-0000-000000000205", "agent-2", fx.message, accepted.Accepted.Message.Seq},
		{"d15a0000-0000-0000-0000-000000000206", "agent-3", fx.message, fx.message.Seq},
		{"d15a0000-0000-0000-0000-000000000207", "agent-4", accepted.Accepted.Message, accepted.Accepted.Message.Seq},
	} {
		createDispatchForGroupMessage(t, fx.db, peer.message, peer.id, peer.agent, fx.groupID, "running", pgtype.Timestamptz{})
		if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET status = 'held', held_up_to_seq = $1 WHERE id = $2`, peer.heldUpTo, peer.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := fx.d.publish.failAcceptedPublish(ctx, post, errors.New("platform down")); err == nil {
		t.Fatal("final publish failure must be reported")
	}
	for _, want := range []struct{ id, status string }{
		{"d15a0000-0000-0000-0000-000000000205", "pending"},
		{"d15a0000-0000-0000-0000-000000000206", "held"},
		{"d15a0000-0000-0000-0000-000000000207", "held"},
	} {
		row, err := fx.q.GetGroupDispatch(ctx, want.id)
		if err != nil || row.Status != want.status {
			t.Fatalf("peer %s = %q/%v, want %q", want.id, row.Status, err, want.status)
		}
	}
}

func TestPublishSuccessFinalizationFailureDoesNotRepublishOrFail(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	fx.d.maxAttempts = 1
	publisher := &recordingGroupPublisher{}
	fx.d.publish.publishers.Register("ch-1", publisher)
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000208", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	if _, err := fx.db.Exec(ctx, `CREATE FUNCTION fail_reply_outbox_fn() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'fail reply outbox'; END; $$ LANGUAGE plpgsql;`); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `CREATE TRIGGER fail_reply_outbox BEFORE INSERT ON ctx_group_outbox FOR EACH ROW EXECUTE FUNCTION fail_reply_outbox_fn();`); err != nil {
		t.Fatal(err)
	}
	row, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000208")
	if err := fx.d.ExecuteDispatch(ctx, row); err == nil {
		t.Fatal("finalization failure must surface for retry")
	}
	row, _ = fx.q.GetGroupDispatch(ctx, row.ID)
	if row.Status != "pending" || !row.PublishedAt.Valid || publisher.calls != 1 {
		t.Fatalf("after bookkeeping failure status/published/calls = %q/%v/%d, want pending/true/1", row.Status, row.PublishedAt.Valid, publisher.calls)
	}
	if _, err := fx.db.Exec(ctx, `DROP TRIGGER fail_reply_outbox ON ctx_group_outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET next_attempt_at = NULL WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}
	row, _ = fx.q.GetGroupDispatch(ctx, row.ID)
	if err := fx.d.ExecuteDispatch(ctx, row); err != nil {
		t.Fatalf("finalization repair: %v", err)
	}
	row, _ = fx.q.GetGroupDispatch(ctx, row.ID)
	if row.Status != "completed" || publisher.calls != 1 {
		t.Fatalf("after repair status/calls = %q/%d, want completed/1", row.Status, publisher.calls)
	}
}

func TestAcceptedPublishLeaseRecoveryFailsAtTenAndReleasesPeers(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	ctx := context.Background()
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000209", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	post, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000209")
	accepted, err := fx.d.acceptGroupResponse(ctx, post, groupResponse{text: "pending delivery", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000210", "agent-2", fx.groupID, "running", pgtype.Timestamptz{})
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET status = 'held', held_up_to_seq = $1 WHERE id = $2`, accepted.Accepted.Message.Seq, "d15a0000-0000-0000-0000-000000000210"); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET attempt_count = $1, lease_until = now() - interval '1 minute' WHERE id = $2`, acceptedPublishRecoveryMaxAttempts, post.ID); err != nil {
		t.Fatal(err)
	}
	if err := fx.d.reapExpired(ctx); err != nil {
		t.Fatal(err)
	}
	post, _ = fx.q.GetGroupDispatch(ctx, post.ID)
	if post.Status != "failed" {
		t.Fatalf("crash recovery dispatch = %q, want failed at %d", post.Status, acceptedPublishRecoveryMaxAttempts)
	}
	message, err := fx.q.GetGroupMessage(ctx, post.ResultMessageID)
	if err != nil || message.DeliveryState != "failed" {
		t.Fatalf("accepted message = %q/%v, want failed", message.DeliveryState, err)
	}
	peer, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000210")
	if peer.Status != "pending" || peer.HeldUpToSeq.Valid {
		t.Fatalf("held peer = %+v, want released", peer)
	}
}

func TestExpiredAcceptedFailureDoesNotOverrideRenewedLease(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", "{}")
	ctx := context.Background()
	addFixtureAgent(t, fx, "agent-2", "ch-2")
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000213", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	post, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000213")
	accepted, err := fx.d.acceptGroupResponse(ctx, post, groupResponse{text: "pending delivery", complete: true}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000214", "agent-2", fx.groupID, "running", pgtype.Timestamptz{})
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET status = 'held', held_up_to_seq = $1 WHERE id = $2`, accepted.Accepted.Message.Seq, "d15a0000-0000-0000-0000-000000000214"); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC()
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_dispatch SET attempt_count = $1, lease_until = $2 WHERE id = $3`, acceptedPublishRecoveryMaxAttempts, cutoff.Add(time.Minute), post.ID); err != nil {
		t.Fatal(err)
	}
	post, _ = fx.q.GetGroupDispatch(ctx, post.ID)
	if err := fx.d.publish.failExpiredAcceptedPublish(ctx, post, errors.New("stale reaper"), cutoff); err == nil {
		t.Fatal("renewed lease must reject stale accepted-publish compensation")
	}
	post, _ = fx.q.GetGroupDispatch(ctx, post.ID)
	message, messageErr := fx.q.GetGroupMessage(ctx, post.ResultMessageID)
	peer, peerErr := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000214")
	if post.Status != "running" || messageErr != nil || message.DeliveryState != "pending" || peerErr != nil || peer.Status != "held" {
		t.Fatalf("stale compensation changed state: post=%q message=%q/%v peer=%q/%v", post.Status, message.DeliveryState, messageErr, peer.Status, peerErr)
	}
}

func TestPublishMarkerBookkeepingUnknownUsesRecoveryCeiling(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "{}")
	ctx := context.Background()
	fx.d.maxAttempts = 1
	publisher := &recordingGroupPublisher{}
	fx.d.publish.publishers.Register("ch-1", publisher)
	createDispatchForGroupMessage(t, fx.db, fx.message, "d15a0000-0000-0000-0000-000000000211", "agent-1", fx.groupID, "pending", pgtype.Timestamptz{})
	if _, err := fx.db.Exec(ctx, `CREATE FUNCTION fail_published_marker_fn() RETURNS trigger AS $$ BEGIN IF NEW.published_at IS NOT NULL THEN RAISE EXCEPTION 'published marker outcome unknown'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql;`); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(ctx, `CREATE TRIGGER fail_published_marker BEFORE UPDATE ON ctx_group_dispatch FOR EACH ROW EXECUTE FUNCTION fail_published_marker_fn();`); err != nil {
		t.Fatal(err)
	}
	row, _ := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000211")
	if err := fx.d.ExecuteDispatch(ctx, row); err == nil {
		t.Fatal("unknown marker outcome must surface for recovery")
	}
	row, _ = fx.q.GetGroupDispatch(ctx, row.ID)
	if row.Status != "pending" || !isAcceptedPublishRecovery(row, nil) || publisher.calls != 1 {
		t.Fatalf("marker failure status/class/calls = %q/%v/%d, want pending/recovery/1", row.Status, isAcceptedPublishRecovery(row, nil), publisher.calls)
	}
}

func TestKnownPublisherErrorCannotErasePriorAmbiguousSend(t *testing.T) {
	d := &GroupDispatcher{maxAttempts: 3}
	row := sqlc.CtxGroupDispatch{
		ResultMessageID: "a1a1a1a1-0000-0000-0000-000000000212",
		LastError:       acceptedPublishRecoveryPrefix + "lease expired before publish",
	}
	cause := errors.New("later platform attempt failed")
	if got := d.dispatchAttemptLimit(row, cause); got != acceptedPublishRecoveryMaxAttempts {
		t.Fatalf("ambiguous accepted publish limit = %d, want recovery limit %d", got, acceptedPublishRecoveryMaxAttempts)
	}
}
