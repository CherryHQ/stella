package channel

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type recordingGroupPublisher struct {
	err   error
	calls int
	texts []string
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
	db      *sql.DB
	q       *sqlc.Queries
	d       *GroupDispatcher
	outbox  sqlc.CtxGroupOutbox
	message sqlc.CtxGroupMessage
}

func newDispatcherFixture(t *testing.T, platform, envelope string) dispatcherFixture {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q := sqlc.New(db)
	ctx := context.Background()
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID:                   "agent-1",
		Name:                 "Agent One",
		Workspace:            t.TempDir(),
		Sandbox:              "{}",
		EnabledBuiltinSkills: "[]",
		Scope:                "system",
		Enabled:              1,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
		ID:      "ch-1",
		AgentID: sql.NullString{String: "agent-1", Valid: true},
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	state, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID:               "group-1",
		Platform:         platform,
		PlatformGroupID:  "physical-group-1",
		PlatformThreadID: "",
		GroupName:        "Group One",
		CreatedByUserID:  sql.NullString{},
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
		ID:        "msg-1",
		GroupID:   state.ID,
		Seq:       1,
		ActorType: "human",
		ActorID:   "user-1",
		Content:   "hello",
	})
	if err != nil {
		t.Fatalf("create group message: %v", err)
	}
	outbox, err := q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{
		ID:             "outbox-1",
		GroupMessageID: message.ID,
		GroupID:        state.ID,
		Envelope:       envelope,
		Status:         "pending",
		LastError:      "",
	})
	if err != nil {
		t.Fatalf("create outbox: %v", err)
	}
	coord := &Coordinator{arbiter: NewArbiter(ArbiterConfig{MaxRepliesPerTrigger: 3})}
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

func TestGroupDispatcherExistingDispatchSkipsEnvelopeDecode(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{not-json`)
	publisher := &recordingGroupPublisher{}
	fx.d.publishers.Register("ch-1", publisher)
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "dispatch-1",
		GroupMessageID: fx.message.ID,
		GroupID:        "group-1",
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

func TestGroupDispatcherPublisherFailureMarksFailedAtMaxAttempts(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	fx.d.maxAttempts = 1
	boom := errors.New("boom")
	fx.d.publishers.Register("ch-1", &recordingGroupPublisher{err: boom})
	if err := fx.q.CreateGroupDispatch(context.Background(), sqlc.CreateGroupDispatchParams{
		ID:             "dispatch-1",
		GroupMessageID: fx.message.ID,
		GroupID:        "group-1",
		AgentID:        "agent-1",
		ReplyChannelID: "ch-1",
		Status:         "pending",
		LastError:      "",
	}); err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err := fx.q.GetGroupDispatch(context.Background(), "dispatch-1")
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}

	if err := fx.d.ExecuteDispatch(context.Background(), dispatch, nil); err == nil {
		t.Fatal("expected publisher error")
	}
	dispatch, err = fx.q.GetGroupDispatch(context.Background(), "dispatch-1")
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

func dispatchStatusByMessage(t *testing.T, db *sql.DB, messageID string) string {
	t.Helper()
	var status string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM ctx_group_dispatch WHERE group_message_id = ?`, messageID).Scan(&status); err != nil {
		t.Fatalf("query dispatch status: %v", err)
	}
	return status
}
