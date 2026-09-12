package channel

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// accountFenceSender records each op it was asked to send and reports account
// ownership the same way real adapters do: by platform account identity.
type accountFenceSender struct {
	mu   sync.Mutex
	owns string
	sent []pkgchannel.OutboundOp
}

func (s *accountFenceSender) SendOperation(_ context.Context, op pkgchannel.OutboundOp) (pkgchannel.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, op)
	return pkgchannel.SendResult{PlatformMessageID: "plat-1"}, nil
}

func (s *accountFenceSender) OwnsAccount(accountKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return accountKey == s.owns
}

func (s *accountFenceSender) sentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// enqueueGroupReply drives the real ingress-to-outbox path: appendGroupMessage
// persists the trigger (with the bot account that received it, as audit), a
// dispatch row routed to replyChannelID accepts a response, and
// publishAccepted hands the reply to the durable outbox. Returns the ops'
// delivery key and the persisted trigger.
func enqueueGroupReply(t *testing.T, fx dispatcherFixture, coord *Coordinator, messageID, observeAccount, replyChannelID string) string {
	t.Helper()
	ctx := context.Background()
	result, err := coord.appendGroupMessage(ctx, pkgchannel.IncomingMessage{
		Platform:      "telegram",
		ChannelID:     "ch-a",
		ChatID:        "physical-group-1",
		BotAccountKey: observeAccount,
		SenderID:      "alice",
		MessageID:     messageID,
		Content:       pkgchannel.TextContent("hi @bot"),
		Mentions:      []pkgchannel.Mention{{AgentID: "agent-1"}},
	})
	if err != nil {
		t.Fatalf("appendGroupMessage: %v", err)
	}
	trigger := result.Message
	if got := trigger.SourceAccountKey.String; got != observeAccount {
		t.Fatalf("persisted source_account_key = %q, want %q (audit)", got, observeAccount)
	}

	dispatchID := uuid.NewString()
	insertGroupDispatchTo(t, fx.db, dispatchID, trigger.ID, fx.groupID, "agent-1", replyChannelID)
	row, err := fx.q.GetGroupDispatch(ctx, dispatchID)
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	// The production accept entry builds and commits the whole op chain: the
	// account snapshot is bound inside the same transaction as the message.
	accepted, err := commitGroupReply(t, fx, row, []agentruntime.Event{
		{Text: "group reply"},
		{Image: &agentruntime.ImageEvent{Data: "aW1n", MimeType: "image/png"}},
	}, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatalf("accept response: %v", err)
	}
	if !accepted.Enqueued {
		t.Fatal("accepted reply did not commit its outbox chain")
	}
	return "group:" + row.ID
}

// insertGroupDispatchTo is insertGroupDispatch with a selectable reply channel:
// a shared trigger wakes members whose replies route to different channels.
func insertGroupDispatchTo(t *testing.T, db sqlc.DBTX, id, groupMessageID, groupID, agentID, replyChannelID string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
INSERT INTO ctx_group_dispatch (
  id, group_message_id, group_id, agent_id, reply_channel_id, status, attempt_count, trigger_seq, kind
)
VALUES ($1, $2, $3, $4, $5, 'running', 0,
  (SELECT seq FROM ctx_group_message WHERE id = $2), 'wake') ON CONFLICT DO NOTHING`,
		id, groupMessageID, groupID, agentID, replyChannelID); err != nil {
		t.Fatalf("insert dispatch %s: %v", id, err)
	}
}

func groupOutboxOps(t *testing.T, fx dispatcherFixture, deliveryKey string) []struct {
	Kind       string
	ChannelID  string
	State      string
	AccountKey string
} {
	t.Helper()
	rows, err := fx.db.Query(context.Background(),
		`SELECT operation_kind, channel_id, state, COALESCE(source_account_key, '') FROM channel_outbox WHERE delivery_key = $1 ORDER BY operation_index`, deliveryKey)
	if err != nil {
		t.Fatalf("query ops: %v", err)
	}
	defer rows.Close()
	var ops []struct {
		Kind       string
		ChannelID  string
		State      string
		AccountKey string
	}
	for rows.Next() {
		var op struct {
			Kind       string
			ChannelID  string
			State      string
			AccountKey string
		}
		if err := rows.Scan(&op.Kind, &op.ChannelID, &op.State, &op.AccountKey); err != nil {
			t.Fatalf("scan op: %v", err)
		}
		ops = append(ops, op)
	}
	return ops
}

func drainGroupOps(t *testing.T, fx dispatcherFixture, channelID string, sender *accountFenceSender) {
	t.Helper()
	ctx := context.Background()
	s := choutbox.New(fx.db)
	for range 8 {
		if _, err := s.ProcessDue(ctx, channelID, "", sender); err != nil {
			t.Fatalf("ProcessDue: %v", err)
		}
	}
}

// A shared trigger observed by bot-A on ch-A can wake a member that replies
// through ch-B owned by bot-B. The op chain must fence on the responding
// channel's registered account (ch-B's runtime_account_key), snapshotted at
// dispatch-accept — never the observing account, which stays audit-only on
// the trigger row.
func TestGroupReplyOpsFenceOnReplyChannelAccount(t *testing.T) {
	ts := setupStores(t)
	el := eventlog.NewStore(ts.db)
	coord := &Coordinator{eventLog: el, store: ts.store}

	fx := newTelegramGroupFixture(t, ts.db, coord)

	// bot-A observed the message; the member replies through ch-B/bot-B.
	deliveryKey := enqueueGroupReply(t, fx, coord, "tg-m1", "bot-a", "ch-b")

	ops := groupOutboxOps(t, fx, deliveryKey)
	if len(ops) != 2 { // send_group_reply + send_attachment for the image
		t.Fatalf("enqueued ops = %+v, want 2 ops", ops)
	}
	for _, op := range ops {
		if op.ChannelID != "ch-b" {
			t.Fatalf("op %s targets channel %q, want ch-b", op.Kind, op.ChannelID)
		}
		if op.AccountKey != "bot-b" {
			t.Fatalf("op %s carries account %q, want bot-b (reply channel account)", op.Kind, op.AccountKey)
		}
	}

	// The fence value alone proves the observing bot (bot-a) cannot own this
	// work; the actual rejection path is exercised by the rebind test below —
	// draining here with a wrong-account sender would fail the ops for good.
	responder := &accountFenceSender{owns: "bot-b"}
	drainGroupOps(t, fx, "ch-b", responder)
	if responder.sentCount() != 2 {
		t.Fatalf("sender calls = %d, want 2", responder.sentCount())
	}
	for _, op := range groupOutboxOps(t, fx, deliveryKey) {
		if op.State != "sent" {
			t.Fatalf("op %s state = %q, want sent", op.Kind, op.State)
		}
	}
}

// After ch-B is re-bound to a different platform account, pending replies
// snapshotted under bot-B must fail account_mismatch instead of going out
// under the new identity. Rotating credentials under the same account still
// owns the work because the fence compares account keys, not credentials —
// the real-adapter rotation path is covered in plugins/channels/telegram.
func TestGroupReplyOpsRejectReboundAccount(t *testing.T) {
	ts := setupStores(t)
	el := eventlog.NewStore(ts.db)
	coord := &Coordinator{eventLog: el, store: ts.store}
	fx := newTelegramGroupFixture(t, ts.db, coord)

	deliveryKey := enqueueGroupReply(t, fx, coord, "tg-m2", "bot-a", "ch-b")

	// ch-B now answers to bot-b2: every pending op snapshotted to bot-B is
	// fenced. The snapshot was taken at accept time, so the rebind cannot
	// rewrite already-enqueued work.
	if _, err := fx.db.Exec(context.Background(),
		`UPDATE channel SET runtime_account_key = 'bot-b2' WHERE id = 'ch-b'`); err != nil {
		t.Fatalf("rebind account: %v", err)
	}
	newOwner := &accountFenceSender{owns: "bot-b2"}
	drainGroupOps(t, fx, "ch-b", newOwner)
	if newOwner.sentCount() != 0 {
		t.Fatalf("rebound sender delivered %d ops, want 0", newOwner.sentCount())
	}
	ops := groupOutboxOps(t, fx, deliveryKey)
	if ops[0].State != "failed" {
		t.Fatalf("primary op state = %q, want failed (account_mismatch)", ops[0].State)
	}
	for _, op := range ops[1:] {
		if op.State != "canceled" {
			t.Fatalf("dependent op %s state = %q, want canceled", op.Kind, op.State)
		}
	}

	// A later reply accepted under the re-bound account snapshots bot-b2 and
	// delivers under bot-b2.
	deliveryKey2 := enqueueGroupReply(t, fx, coord, "tg-m3", "bot-a", "ch-b")
	drainGroupOps(t, fx, "ch-b", newOwner)
	if newOwner.sentCount() != 2 {
		t.Fatalf("new-owner sends = %d, want 2", newOwner.sentCount())
	}
	for _, op := range groupOutboxOps(t, fx, deliveryKey2) {
		if op.AccountKey != "bot-b2" || op.State != "sent" {
			t.Fatalf("bot-b2 op %s = (%q, %q), want (bot-b2, sent)", op.Kind, op.AccountKey, op.State)
		}
	}
}

// newTelegramGroupFixture mirrors newDispatcherFixture with a telegram
// platform group: ch-A is the channel that observes the shared group and ch-B
// (registered to platform account bot-B) is the member's reply channel.
func newTelegramGroupFixture(t *testing.T, db *pgxpool.Pool, coord *Coordinator) dispatcherFixture {
	t.Helper()
	ctx := context.Background()
	q := sqlc.New(db)
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
	if _, err := db.Exec(ctx, `INSERT INTO channel (id, name, type, agent_id, enabled, config) VALUES ('ch-a', 'TG Bot A', 'telegram', 'agent-1', true, '{}')`); err != nil {
		t.Fatalf("create observer channel: %v", err)
	}
	// The reply channel belongs to a different agent — one agent per
	// (agent_id, type) — and is bound to platform account bot-B.
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID:        "agent-2",
		Name:      "Agent Two",
		Workspace: t.TempDir(),
		Sandbox:   json.RawMessage("{}"),
		Scope:     "system",
		Enabled:   true,
	}); err != nil {
		t.Fatalf("create reply agent: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO channel (id, name, type, agent_id, enabled, config, runtime_account_key) VALUES ('ch-b', 'TG Bot B', 'telegram', 'agent-2', true, '{}', 'bot-b')`); err != nil {
		t.Fatalf("create reply channel: %v", err)
	}
	state, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID:               "22222222-2222-2222-2222-222222222222",
		Platform:         "telegram",
		PlatformGroupID:  "physical-group-1",
		PlatformThreadID: "",
		GroupName:        "TG Group",
		CreatedByUserID:  pgtype.Text{},
	})
	if err != nil {
		t.Fatalf("create group state: %v", err)
	}
	if _, err := q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
		GroupID:        state.ID,
		AgentID:        "agent-1",
		ReplyChannelID: "ch-b",
	}); err != nil {
		t.Fatalf("add group member: %v", err)
	}
	d := NewGroupDispatcher(db, coord)
	d.leaseDuration = 0
	d.SetGroupTurnCommitter(groupTurnCommitterFunc(func(context.Context, *sqlc.Queries, memory.DeferredGroupTurn) error { return nil }))
	return dispatcherFixture{db: db, q: q, d: d, groupID: state.ID}
}
