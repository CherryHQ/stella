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
		ActorType: "human",
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
	coord := &Coordinator{store: cfgstore.NewDBStore(db), arbiter: NewArbiter(ArbiterConfig{MaxRepliesPerTrigger: 3})}
	d := NewGroupDispatcher(db, coord, NewPublisherRegistry())
	d.leaseDuration = 0
	d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		return textStream("ok"), nil
	}
	return dispatcherFixture{db: db, q: q, d: d, outbox: outbox, message: message}
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
	msg, err := q.CreateGroupMessage(context.Background(), sqlc.CreateGroupMessageParams{
		ID:        id,
		GroupID:   groupID,
		Seq:       seq,
		ActorType: "human",
		ActorID:   "user-1",
		Content:   "hello",
	})
	if err != nil {
		t.Fatalf("create message %s: %v", id, err)
	}
	return msg
}

func listPendingDispatchIDs(t *testing.T, q *sqlc.Queries, now time.Time, limit int32) []string {
	t.Helper()
	rows, err := q.ListPendingGroupDispatch(context.Background(), sqlc.ListPendingGroupDispatchParams{
		Now:        nullTime(now),
		LimitCount: limit,
	})
	if err != nil {
		t.Fatalf("list pending dispatch: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func TestListPendingGroupDispatchBlocksLaterSameAgentSeq(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete outbox: %v", err)
	}
	now := time.Now().UTC()
	earlier := fx.message
	later := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000002", 2)
	createDispatchForGroupMessage(t, fx.q, earlier, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	createDispatchForGroupMessage(t, fx.q, later, "d15a0000-0000-0000-0000-000000000002", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})

	ids := listPendingDispatchIDs(t, fx.q, now, 25)
	if !containsString(ids, "d15a0000-0000-0000-0000-000000000001") || containsString(ids, "d15a0000-0000-0000-0000-000000000002") {
		t.Fatalf("pending ids = %v, want earlier only", ids)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET status = 'completed' WHERE id = 'd15a0000-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete earlier: %v", err)
	}
	ids = listPendingDispatchIDs(t, fx.q, now, 25)
	if !containsString(ids, "d15a0000-0000-0000-0000-000000000002") {
		t.Fatalf("pending ids = %v, want later after terminal earlier", ids)
	}
}

func TestListPendingGroupDispatchExpiredRunningDoesNotBlockLater(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete outbox: %v", err)
	}
	now := time.Now().UTC()
	later := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000002", 2)
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.message.GroupID, "running", nullTime(now.Add(-time.Minute)))
	createDispatchForGroupMessage(t, fx.q, later, "d15a0000-0000-0000-0000-000000000002", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})

	ids := listPendingDispatchIDs(t, fx.q, now, 25)
	if !containsString(ids, "d15a0000-0000-0000-0000-000000000002") {
		t.Fatalf("pending ids = %v, want expired running not to block later", ids)
	}
}

func TestListPendingGroupDispatchBlocksLaterWhenEarlierOutboxNotTerminal(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	now := time.Now().UTC()
	later := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000002", 2)
	createDispatchForGroupMessage(t, fx.q, later, "d15a0000-0000-0000-0000-000000000002", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})

	ids := listPendingDispatchIDs(t, fx.q, now, 25)
	if containsString(ids, "d15a0000-0000-0000-0000-000000000002") {
		t.Fatalf("pending ids = %v, want later blocked by earlier outbox", ids)
	}
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete earlier outbox: %v", err)
	}
	ids = listPendingDispatchIDs(t, fx.q, now, 25)
	if !containsString(ids, "d15a0000-0000-0000-0000-000000000002") {
		t.Fatalf("pending ids = %v, want later after terminal earlier outbox", ids)
	}
}

func TestListPendingGroupDispatchBlockedRowsDoNotConsumeLimit(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete outbox: %v", err)
	}
	now := time.Now().UTC()
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	for i := range 30 {
		msg := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, fmt.Sprintf("a1a1a1a1-0000-0000-0000-00000000b%03d", i), int64(i+2))
		createDispatchForGroupMessage(t, fx.q, msg, fmt.Sprintf("d15a0000-0000-0000-0000-00000000b%03d", i), "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	}
	otherGroup, err := fx.q.CreateGroupState(context.Background(), sqlc.CreateGroupStateParams{ID: "22222222-2222-2222-2222-222222222222", Platform: "web", PlatformGroupID: "physical-group-2", GroupName: "Group Two"})
	if err != nil {
		t.Fatalf("create group-2: %v", err)
	}
	otherMsg := createGroupMessageWithSeq(t, fx.q, otherGroup.ID, "a1a1a1a1-0000-0000-0000-00000000000f", 1)
	createDispatchForGroupMessage(t, fx.q, otherMsg, "d15a0000-0000-0000-0000-0000000000fe", "agent-1", otherGroup.ID, "pending", pgtype.Timestamptz{})

	ids := listPendingDispatchIDs(t, fx.q, now, 25)
	if !containsString(ids, "d15a0000-0000-0000-0000-0000000000fe") {
		t.Fatalf("pending ids = %v, want other group despite 30 blocked rows", ids)
	}
}

func TestListPendingGroupDispatchGateIsPerGroupAgent(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := context.Background()
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete outbox: %v", err)
	}
	now := time.Now().UTC()
	if _, err := fx.q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "agent-2", Name: "Agent Two", Workspace: t.TempDir(), Sandbox: json.RawMessage("{}"), Scope: "system", Enabled: true}); err != nil {
		t.Fatalf("create agent-2: %v", err)
	}
	state2, err := fx.q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{ID: "22222222-2222-2222-2222-222222222222", Platform: "web", PlatformGroupID: "physical-group-2", GroupName: "Group Two"})
	if err != nil {
		t.Fatalf("create group-2: %v", err)
	}
	laterSameAgent := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000002", 2)
	laterOtherAgent := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000003", 3)
	otherGroup := createGroupMessageWithSeq(t, fx.q, state2.ID, "a1a1a1a1-0000-0000-0000-000000000004", 2)
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	createDispatchForGroupMessage(t, fx.q, laterSameAgent, "d15a0000-0000-0000-0000-000000000002", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	createDispatchForGroupMessage(t, fx.q, laterOtherAgent, "d15a0000-0000-0000-0000-000000000003", "agent-2", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	createDispatchForGroupMessage(t, fx.q, otherGroup, "d15a0000-0000-0000-0000-000000000004", "agent-1", state2.ID, "pending", pgtype.Timestamptz{})

	ids := listPendingDispatchIDs(t, fx.q, now, 25)
	if containsString(ids, "d15a0000-0000-0000-0000-000000000002") || !containsString(ids, "d15a0000-0000-0000-0000-000000000003") || !containsString(ids, "d15a0000-0000-0000-0000-000000000004") {
		t.Fatalf("pending ids = %v, want same-agent blocked only", ids)
	}
}

func containsString(xs []string, want string) bool {
	return slices.Contains(xs, want)
}

func TestGroupDispatcherReapExpiredCompletesDispatchWithResultMarker(t *testing.T) {
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
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = 'result-1' WHERE id = 'd15a0000-0000-0000-0000-0000000000ff'`); err != nil {
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

func TestGroupDispatcherProcessOutboxHappyPath(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	outbox, err := fx.q.GetGroupOutbox(context.Background(), fx.outbox.ID)
	if err != nil {
		t.Fatalf("get outbox: %v", err)
	}
	if outbox.Status != "completed" {
		t.Fatalf("outbox status = %q, want completed", outbox.Status)
	}
	if publisher.calls != 1 || len(publisher.texts) != 1 || publisher.texts[0] != "ok" {
		t.Fatalf("publisher calls/texts = %d/%v, want one ok", publisher.calls, publisher.texts)
	}
	status := dispatchStatusByMessage(t, fx.db, fx.message.ID)
	if status != "completed" {
		t.Fatalf("dispatch status = %q, want completed", status)
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

func TestGroupDispatcherWebNoMentionMultiMemberStaysSilent(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	addSecondMember(t, fx)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	count, err := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID)
	if err != nil {
		t.Fatalf("count dispatch: %v", err)
	}
	if count != 0 {
		t.Fatalf("dispatch rows = %d, want 0", count)
	}
}

// A multi-member platform (non-web) group with no semantic arbiter takes the
// degraded Warn path and stays silent — it must never broadcast to every member
// the way the deleted `group_mode: always` fallback once did. This is the
// positive lock on that branch: it fails if any all-members fallback is
// reintroduced for platform groups.
func TestGroupDispatcherPlatformNoMentionMultiMemberNoArbiterStaysSilent(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	addSecondMember(t, fx)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	count, err := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID)
	if err != nil {
		t.Fatalf("count dispatch: %v", err)
	}
	if count != 0 {
		t.Fatalf("dispatch rows = %d, want 0 (platform multi-member, no arbiter)", count)
	}
}

func TestGroupDispatcherZeroRespondersCompletesOutbox(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	outbox, err := fx.q.GetGroupOutbox(context.Background(), fx.outbox.ID)
	if err != nil {
		t.Fatalf("get outbox: %v", err)
	}
	if outbox.Status != "completed" {
		t.Fatalf("outbox status = %q, want completed", outbox.Status)
	}
	count, err := fx.q.CountGroupDispatchByMessage(context.Background(), fx.message.ID)
	if err != nil {
		t.Fatalf("count dispatch: %v", err)
	}
	if count != 0 {
		t.Fatalf("dispatch rows = %d, want 0", count)
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

func TestGroupDispatcherDispatchesAllMentionedMembers(t *testing.T) {
	envelope, err := EncodeGroupOutboxEnvelope([]pkgchannel.Mention{{AgentID: "agent-1"}, {AgentID: "agent-2"}})
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	fx := newDispatcherFixture(t, "telegram", envelope)
	addSecondMember(t, fx)
	firstPublisher := &recordingGroupPublisher{}
	secondPublisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", firstPublisher)
	fx.d.publishers.Register("ch-2", secondPublisher)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if got := dispatchAgentsByMessage(t, fx.db, fx.message.ID); len(got) != 2 || got[0] != "agent-1" || got[1] != "agent-2" {
		t.Fatalf("dispatch agents = %v, want [agent-1 agent-2]", got)
	}
	if firstPublisher.calls != 1 || secondPublisher.calls != 1 {
		t.Fatalf("publisher calls = %d/%d, want 1/1", firstPublisher.calls, secondPublisher.calls)
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
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err == nil {
		t.Fatal("expected publisher error")
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after failure: %v", err)
	}
	if dispatch.Status != "pending" || dispatch.ResultMessageID != "" {
		t.Fatalf("dispatch status/result = %q/%q, want pending empty result", dispatch.Status, dispatch.ResultMessageID)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 0 {
		t.Fatalf("agent messages = %d, want 0", got)
	}

	publisher.err = nil
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET next_attempt_at = NULL WHERE id = 'd15a0000-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("make dispatch due: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch before retry: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
		t.Fatalf("retry dispatch: %v", err)
	}
	if publisher.calls != 2 {
		t.Fatalf("publisher calls = %d, want retry to republish", publisher.calls)
	}
}

func TestGroupDispatcherWritebackFailureLeavesResultEmptyAndRequeues(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	chatCalls := 0
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
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
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err == nil {
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
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
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
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
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

func TestGroupDispatcherResultMessageSkipsChatPublishAndAppend(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	chatCalls := 0
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
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
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = 'result-1' WHERE id = 'd15a0000-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("set result marker: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
		t.Fatalf("execute dispatch: %v", err)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after execute: %v", err)
	}
	if dispatch.Status != "completed" || chatCalls != 0 || publisher.calls != 0 {
		t.Fatalf("status/chat/publish = %q/%d/%d, want completed/0/0", dispatch.Status, chatCalls, publisher.calls)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 0 {
		t.Fatalf("agent messages = %d, want 0", got)
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
	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
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

	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err == nil {
		t.Fatal("expected publisher error")
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch after failure: %v", err)
	}
	if dispatch.Status != "failed" {
		t.Fatalf("dispatch status = %q, want failed", dispatch.Status)
	}
}

func TestGroupDispatcherDispatchSyncUsesPublisherOverride(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &recordingGroupPublisher{}

	if err := fx.d.DispatchSync(context.Background(), fx.outbox, publisher); err != nil {
		t.Fatalf("dispatch sync: %v", err)
	}
	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", publisher.calls)
	}
}

func TestGroupDispatcherDispatchSyncWaitsForBlockedDispatch(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := context.Background()
	if _, err := fx.db.Exec(ctx, `UPDATE ctx_group_outbox SET status = 'completed' WHERE id = 'b0b0b0b0-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("complete earlier outbox: %v", err)
	}
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.message.GroupID, "pending", pgtype.Timestamptz{})
	later := createGroupMessageWithSeq(t, fx.q, fx.message.GroupID, "a1a1a1a1-0000-0000-0000-000000000002", 2)
	setGroupNextSeq(t, fx.db, fx.message.GroupID, later.Seq)
	laterOutbox, err := fx.q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{ID: "b0b0b0b0-0000-0000-0000-000000000002", GroupMessageID: later.ID, GroupID: fx.message.GroupID, Envelope: "{}", Status: "pending", LastError: ""})
	if err != nil {
		t.Fatalf("create later outbox: %v", err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET status = 'completed' WHERE id = 'd15a0000-0000-0000-0000-000000000001'`)
	}()
	publisher := &recordingGroupPublisher{}
	if err := fx.d.DispatchSync(ctx, laterOutbox, publisher); err != nil {
		t.Fatalf("dispatch sync: %v", err)
	}
	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", publisher.calls)
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
	go func() { errC <- fx.d.ExecuteDispatch(context.Background(), dispatch, nil) }()
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
	go func() { errC <- fx.d.ExecuteDispatch(context.Background(), dispatch, nil) }()
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

func dispatchStatusByMessage(t *testing.T, db *pgxpool.Pool, messageID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(context.Background(), `SELECT status FROM ctx_group_dispatch WHERE group_message_id = $1`, messageID).Scan(&status); err != nil {
		t.Fatalf("query dispatch status: %v", err)
	}
	return status
}

func countAgentGroupMessages(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM ctx_group_message WHERE actor_type = 'agent'`).Scan(&count); err != nil {
		t.Fatalf("count agent messages: %v", err)
	}
	return count
}

func TestWrapGroupResponseStreamSkipsWritebackOnErrEvent(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	stream := make(chan pkgchannel.Event, 2)
	stream <- pkgchannel.Event{Text: "partial"}
	stream <- pkgchannel.Event{Err: errors.New("boom")}
	close(stream)

	wrapped, responseC := fx.d.wrapGroupResponseStream(context.Background(), &pkgchannel.ChatStream{Events: stream, SessionID: "session-1"})
	for range wrapped.Events {
	}
	response := <-responseC
	if response.complete {
		t.Fatalf("response.complete = true, want false")
	}
	assertNoAgentGroupMessages(t, fx.db)
}

func TestWrapGroupResponseStreamSkipsWritebackAfterCancel(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	stream := make(chan pkgchannel.Event)
	wrapped, responseC := fx.d.wrapGroupResponseStream(ctx, &pkgchannel.ChatStream{Events: stream, SessionID: "session-1"})

	stream <- pkgchannel.Event{Text: "partial"}
	cancel()
	stream <- pkgchannel.Event{Text: " ignored"}
	close(stream)
	for range wrapped.Events {
	}
	response := <-responseC
	if response.complete {
		t.Fatalf("response.complete = true, want false")
	}
	assertNoAgentGroupMessages(t, fx.db)
}

func TestWrapGroupResponseStreamBuffersCompleteStream(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	stream := make(chan pkgchannel.Event, 2)
	stream <- pkgchannel.Event{Text: "complete"}
	close(stream)

	wrapped, responseC := fx.d.wrapGroupResponseStream(context.Background(), &pkgchannel.ChatStream{Events: stream, SessionID: "session-1"})
	for range wrapped.Events {
	}
	response := <-responseC
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
