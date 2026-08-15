package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
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
	coord := &Coordinator{store: cfgstore.NewDBStore(db), arbiter: NewArbiter(ArbiterConfig{MaxRepliesPerTrigger: 3})}
	d := NewGroupDispatcher(db, coord, NewPublisherRegistry())
	d.leaseDuration = 0
	d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
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
	msg, err := q.CreateGroupMessage(context.Background(), sqlc.CreateGroupMessageParams{
		ID:        id,
		GroupID:   groupID,
		Seq:       seq,
		ActorType: string(eventlog.ActorHuman),
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

// A publish failure must not throw away a response the agent already produced:
// the response is recorded first, so the retry re-delivers it instead of
// paying for a second agent turn that would answer with different text.
func TestGroupDispatcherPublishFailureRecordsResultAndRedelivers(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	boom := errors.New("boom")
	publisher := &recordingGroupPublisher{err: boom}
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
	if dispatch.Status != "pending" || dispatch.ResultMessageID == "" || dispatch.DeliveryComplete {
		t.Fatalf("dispatch status/result/delivered = %q/%q/%v, want pending with a recorded undelivered result", dispatch.Status, dispatch.ResultMessageID, dispatch.DeliveryComplete)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 1 {
		t.Fatalf("agent messages = %d, want the response persisted once", got)
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
	if chatCalls != 1 {
		t.Fatalf("chat calls = %d, want the agent to run exactly once", chatCalls)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 1 {
		t.Fatalf("agent messages = %d, want no second response", got)
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

func TestGroupDispatcherDeliveredResultSkipsChatPublishAndAppend(t *testing.T) {
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
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = 'result-1', delivery_complete = true WHERE id = 'd15a0000-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("set delivered result marker: %v", err)
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

type capturingGroupPublisher struct {
	req GroupPublishRequest
}

func (p *capturingGroupPublisher) Publish(_ context.Context, req GroupPublishRequest) error {
	p.req = req
	if req.Stream != nil {
		for range req.Stream.Events {
		}
	}
	return nil
}

// TestExecuteDispatchAbortClosureUsesSessionQueueGroupKey verifies the
// GroupPublishRequest.Abort closure ExecuteDispatch builds targets the exact
// same per-(agent,group) session queue slot chatDispatchUnqueued enqueues
// under. A wrong key would make a Discord Cancel click silently no-op
// instead of stopping the running turn.
func TestExecuteDispatchAbortClosureUsesSessionQueueGroupKey(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	publisher := &capturingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)

	sessionKey := agent.BuildGroupSessionKey("agent-1", fx.groupID)
	wrongKey := agent.BuildGroupSessionKey("agent-2", fx.groupID)
	correctCalled := false
	wrongCalled := false

	slot := fx.d.queue.getOrCreate(sessionKey)
	slot.mu.Lock()
	slot.activeCancel = func() { correctCalled = true }
	slot.mu.Unlock()
	defer fx.d.queue.release(slot)

	wrongSlot := fx.d.queue.getOrCreate(wrongKey)
	wrongSlot.mu.Lock()
	wrongSlot.activeCancel = func() { wrongCalled = true }
	wrongSlot.mu.Unlock()
	defer fx.d.queue.release(wrongSlot)

	if err := fx.d.ProcessOutbox(context.Background(), fx.outbox); err != nil {
		t.Fatalf("process outbox: %v", err)
	}
	if publisher.req.RequesterID != "user-1" {
		t.Fatalf("RequesterID = %q, want the triggering message's actor id", publisher.req.RequesterID)
	}
	if publisher.req.Abort == nil {
		t.Fatal("Abort is nil, want a closure targeting the dispatch's session queue slot")
	}
	if !publisher.req.Abort() {
		t.Fatal("Abort() = false, want true: it should cancel the active slot keyed by agent.BuildGroupSessionKey(agent-1, group)")
	}
	if !correctCalled {
		t.Fatal("Abort() did not cancel the slot keyed by agent.BuildGroupSessionKey(agent-1, group)")
	}
	if wrongCalled {
		t.Fatal("Abort() cancelled the wrong agent's session slot")
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

// chunkingGroupPublisher stands in for a platform publisher that delivers a
// response as separately confirmable chunks: it skips what the cursor already
// covers, confirms every chunk the "platform" accepts, and stops at failAt
// (1-based) to model a partial delivery.
type chunkingGroupPublisher struct {
	failAt     int
	confirmErr func() error
	calls      int
	streams    int
	sent       []string
	last       GroupPublishRequest
}

func (p *chunkingGroupPublisher) Publish(ctx context.Context, req GroupPublishRequest) error {
	p.calls++
	p.last = req
	text := req.Text
	if req.Stream != nil {
		p.streams++
		var sb strings.Builder
		for evt := range req.Stream.Events {
			sb.WriteString(evt.Text)
		}
		text = sb.String()
	}
	if text == "" {
		return nil
	}
	for i, chunk := range strings.Split(text, "\n") {
		if int64(i) < req.DeliveryCursor {
			continue
		}
		if p.failAt == i+1 {
			return fmt.Errorf("chunk %d rejected by the platform", i+1)
		}
		p.sent = append(p.sent, chunk)
		if p.confirmErr != nil {
			if err := p.confirmErr(); err != nil {
				return err
			}
		}
		if err := req.Confirm(ctx, int64(i+1)); err != nil {
			return err
		}
	}
	return nil
}

func newResumeDispatch(t *testing.T, fx dispatcherFixture) sqlc.CtxGroupDispatch {
	t.Helper()
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "d15a0000-0000-0000-0000-000000000001",
		GroupMessageID: fx.message.ID,
		GroupID:        fx.groupID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	row, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	return row
}

func makeDispatchDue(t *testing.T, fx dispatcherFixture) sqlc.CtxGroupDispatch {
	t.Helper()
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET next_attempt_at = NULL WHERE id = 'd15a0000-0000-0000-0000-000000000001'`); err != nil {
		t.Fatalf("make dispatch due: %v", err)
	}
	row, err := fx.q.GetGroupDispatch(context.Background(), "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch before retry: %v", err)
	}
	return row
}

// TestGroupDispatcherResumesPartialDeliveryFromCursor is the whole feature in
// one journey: three chunks, the second one fails, and the retry re-delivers
// only chunks two and three from the persisted response — without running the
// agent again.
func TestGroupDispatcherResumesPartialDeliveryFromCursor(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	chatCalls := 0
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
		return textStream("one\ntwo\nthree"), nil
	}
	publisher := &chunkingGroupPublisher{failAt: 2}
	fx.d.publishers.Register("ch-1", publisher)
	dispatch := newResumeDispatch(t, fx)

	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err == nil {
		t.Fatal("expected the failed chunk to fail the dispatch")
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), dispatch.ID)
	if err != nil {
		t.Fatalf("get dispatch after partial delivery: %v", err)
	}
	if dispatch.Status != "pending" || dispatch.ResultMessageID == "" {
		t.Fatalf("dispatch status/result = %q/%q, want a requeued row with a recorded response", dispatch.Status, dispatch.ResultMessageID)
	}
	if dispatch.DeliveryCursor != 1 || dispatch.DeliveryComplete {
		t.Fatalf("delivery cursor/complete = %d/%v, want 1/false", dispatch.DeliveryCursor, dispatch.DeliveryComplete)
	}
	if !slices.Equal(publisher.sent, []string{"one"}) {
		t.Fatalf("sent chunks = %v, want only the first", publisher.sent)
	}

	publisher.failAt = 0
	if err := fx.d.ExecuteDispatch(context.Background(), makeDispatchDue(t, fx), nil); err != nil {
		t.Fatalf("resume dispatch: %v", err)
	}
	if publisher.last.Stream != nil || publisher.last.Text != "one\ntwo\nthree" || publisher.last.DeliveryCursor != 1 {
		t.Fatalf("resume request = stream %v text %q cursor %d, want the persisted text from cursor 1", publisher.last.Stream != nil, publisher.last.Text, publisher.last.DeliveryCursor)
	}
	if !slices.Equal(publisher.sent, []string{"one", "two", "three"}) {
		t.Fatalf("sent chunks = %v, want the tail delivered exactly once", publisher.sent)
	}
	if chatCalls != 1 || publisher.streams != 1 {
		t.Fatalf("chat calls / live streams = %d/%d, want the agent to run exactly once", chatCalls, publisher.streams)
	}
	if got := countAgentGroupMessages(t, fx.db); got != 1 {
		t.Fatalf("agent messages = %d, want one persisted response", got)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), dispatch.ID)
	if err != nil {
		t.Fatalf("get dispatch after resume: %v", err)
	}
	if dispatch.Status != "completed" || !dispatch.DeliveryComplete || dispatch.DeliveryCursor != 3 {
		t.Fatalf("dispatch = %q cursor %d delivered %v, want completed/3/true", dispatch.Status, dispatch.DeliveryCursor, dispatch.DeliveryComplete)
	}
}

// A confirmation that does not land means another attempt owns the row. The
// publisher reports it as a delivery failure and the dispatcher requeues —
// still without re-running the agent, because the response is already recorded.
func TestGroupDispatcherConfirmFailureRequeuesWithoutRerunningAgent(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	chatCalls := 0
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
		return textStream("one\ntwo"), nil
	}
	publisher := &chunkingGroupPublisher{}
	// Steal the row mid-delivery: the next confirmation cannot match this
	// attempt's attempt_count, which is exactly what a lost lease looks like.
	publisher.confirmErr = func() error {
		_, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET attempt_count = attempt_count + 1 WHERE id = 'd15a0000-0000-0000-0000-000000000001'`)
		publisher.confirmErr = nil
		return err
	}
	fx.d.publishers.Register("ch-1", publisher)
	dispatch := newResumeDispatch(t, fx)

	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err == nil {
		t.Fatal("expected the lost confirmation to fail the dispatch")
	}
	if chatCalls != 1 {
		t.Fatalf("chat calls = %d, want exactly one agent turn", chatCalls)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), dispatch.ID)
	if err != nil {
		t.Fatalf("get dispatch after lost confirmation: %v", err)
	}
	if dispatch.DeliveryComplete {
		t.Fatal("delivery marked complete after a confirmation that never landed")
	}
	if dispatch.DeliveryCursor != 0 {
		t.Fatalf("delivery cursor = %d, want 0: the stolen row must not record this attempt's progress", dispatch.DeliveryCursor)
	}
}

// A cursor only indexes the response it was recorded against. An attempt that
// regenerates instead of re-delivering must clear it, or the publisher would
// skip leading chunks of text nobody has seen.
func TestGroupDispatcherResetsDeliveryCursorWhenRegenerating(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		return textStream("one\ntwo"), nil
	}
	publisher := &chunkingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	dispatch := newResumeDispatch(t, fx)
	// A previous attempt died mid-delivery before its response was recorded.
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET delivery_cursor = 5 WHERE id = $1`, dispatch.ID); err != nil {
		t.Fatalf("seed stale cursor: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), dispatch.ID)
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}

	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
		t.Fatalf("execute dispatch: %v", err)
	}
	if publisher.last.DeliveryCursor != 0 {
		t.Fatalf("live publish cursor = %d, want 0 for a regenerated response", publisher.last.DeliveryCursor)
	}
	if !slices.Equal(publisher.sent, []string{"one", "two"}) {
		t.Fatalf("sent chunks = %v, want the whole regenerated response", publisher.sent)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), dispatch.ID)
	if err != nil {
		t.Fatalf("get dispatch after execute: %v", err)
	}
	if dispatch.DeliveryCursor != 2 || !dispatch.DeliveryComplete {
		t.Fatalf("delivery cursor/complete = %d/%v, want 2/true", dispatch.DeliveryCursor, dispatch.DeliveryComplete)
	}
}

// A result marker that cannot be read back retires the row. Re-running the
// agent would answer a message the group was already answered, which is worse
// than losing the tail of one delivery.
func TestGroupDispatcherUnreadableResultRetiresWithoutRerunningAgent(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	chatCalls := 0
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		chatCalls++
		return textStream("ok"), nil
	}
	publisher := &chunkingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	dispatch := newResumeDispatch(t, fx)
	if _, err := fx.db.Exec(context.Background(), `UPDATE ctx_group_dispatch SET result_message_id = 'a1a1a1a1-0000-0000-0000-0000000000ff' WHERE id = $1`, dispatch.ID); err != nil {
		t.Fatalf("set orphaned result marker: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), dispatch.ID)
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}

	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err != nil {
		t.Fatalf("execute dispatch: %v", err)
	}
	if chatCalls != 0 || publisher.calls != 0 {
		t.Fatalf("chat/publish calls = %d/%d, want an already-answered turn to run neither", chatCalls, publisher.calls)
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), dispatch.ID)
	if err != nil {
		t.Fatalf("get dispatch after execute: %v", err)
	}
	if dispatch.Status != "completed" {
		t.Fatalf("dispatch status = %q, want completed", dispatch.Status)
	}
}

// The cursor is a high-water mark: a late or duplicate confirmation must never
// rewind it, and one from a stale attempt must not land at all.
func TestAdvanceGroupDispatchDeliveryIsMonotonicAndOwnershipScoped(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := context.Background()
	createDispatchForGroupMessage(t, fx.q, fx.message, "d15a0000-0000-0000-0000-000000000001", "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	advance := func(cursor, attempt int64) int64 {
		t.Helper()
		rows, err := fx.q.AdvanceGroupDispatchDelivery(ctx, sqlc.AdvanceGroupDispatchDeliveryParams{ID: row.ID, AttemptCount: attempt, DeliveryCursor: cursor})
		if err != nil {
			t.Fatalf("advance delivery cursor: %v", err)
		}
		return rows
	}
	cursor := func() int64 {
		t.Helper()
		current, err := fx.q.GetGroupDispatch(ctx, row.ID)
		if err != nil {
			t.Fatalf("get dispatch: %v", err)
		}
		return current.DeliveryCursor
	}
	if advance(2, row.AttemptCount) != 1 || cursor() != 2 {
		t.Fatalf("cursor = %d after advancing to 2", cursor())
	}
	if advance(1, row.AttemptCount) != 0 || cursor() != 2 {
		t.Fatalf("cursor = %d after a rewinding confirmation, want it pinned at 2", cursor())
	}
	if advance(3, row.AttemptCount+1) != 0 || cursor() != 2 {
		t.Fatalf("cursor = %d after a stale attempt's confirmation, want it pinned at 2", cursor())
	}
}

// abandoningGroupPublisher fails the way a platform publisher fails on its
// first API call: it returns without reading a single event.
type abandoningGroupPublisher struct{}

func (abandoningGroupPublisher) Publish(context.Context, GroupPublishRequest) error {
	return errors.New("platform unreachable")
}

// A publisher that fails without draining the stream must not strand the
// dispatcher: the response is drained and recorded anyway, which is both what
// keeps the wrapper goroutine from blocking on a full forward buffer and what
// makes the failed attempt re-deliverable. The event count deliberately exceeds
// the wrapper's buffer so an undrained stream would deadlock instead of pass.
func TestGroupDispatcherRecordsResultAfterPublisherAbandonsStream(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	const events = 150
	fx.d.chat = func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
		ch := make(chan pkgchannel.Event, events)
		for range events {
			ch <- pkgchannel.Event{Text: "x"}
		}
		close(ch)
		return &pkgchannel.ChatStream{Events: ch, SessionID: "session-1"}, nil
	}
	fx.d.publishers.Register("ch-1", abandoningGroupPublisher{})
	dispatch := newResumeDispatch(t, fx)

	done := make(chan error, 1)
	go func() { done <- fx.d.ExecuteDispatch(context.Background(), dispatch, nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the publisher error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ExecuteDispatch blocked on a stream the publisher never drained")
	}
	var content string
	if err := fx.db.QueryRow(context.Background(), `SELECT content FROM ctx_group_message WHERE actor_type = 'agent'`).Scan(&content); err != nil {
		t.Fatalf("read recorded response: %v", err)
	}
	if content != strings.Repeat("x", events) {
		t.Fatalf("recorded response has %d runes, want the whole drained stream", len(content))
	}
}
