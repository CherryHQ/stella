package channel

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

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
// persists the trigger (with the bot account that received it), a dispatch row
// accepts a response, and publishAccepted hands the reply to the durable
// outbox. Returns the ops' delivery key and the persisted trigger.
func enqueueGroupReply(t *testing.T, fx dispatcherFixture, coord *Coordinator, messageID, accountKey string) string {
	t.Helper()
	ctx := context.Background()
	result, err := coord.appendGroupMessage(ctx, pkgchannel.IncomingMessage{
		Platform:      "telegram",
		ChannelID:     "ch-1",
		ChatID:        "physical-group-1",
		BotAccountKey: accountKey,
		SenderID:      "alice",
		MessageID:     messageID,
		Content:       pkgchannel.TextContent("hi @bot"),
		Mentions:      []pkgchannel.Mention{{AgentID: "agent-1"}},
	})
	if err != nil {
		t.Fatalf("appendGroupMessage: %v", err)
	}
	trigger := result.Message
	if got := trigger.SourceAccountKey.String; got != accountKey {
		t.Fatalf("persisted source_account_key = %q, want %q", got, accountKey)
	}

	dispatchID := uuid.NewString()
	createDispatchForGroupMessage(t, fx.db, trigger, dispatchID, "agent-1", fx.groupID, "running", pgtype.Timestamptz{})
	row, err := fx.q.GetGroupDispatch(ctx, dispatchID)
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	response := groupResponse{events: []pkgchannel.Event{{Text: "group reply"}, {Image: &pkgchannel.ImageEvent{Data: "aW1n", MimeType: "image/png"}}}}
	accepted, err := fx.d.acceptGroupResponse(ctx, row, response, memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatalf("accept response: %v", err)
	}
	row, err = fx.q.GetGroupDispatch(ctx, row.ID)
	if err != nil {
		t.Fatalf("reload dispatch: %v", err)
	}
	state, err := fx.q.GetGroupStateByID(ctx, fx.groupID)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	job := publishJob{row: row, trigger: trigger, state: state, response: response, acceptedMessageID: accepted.Accepted.Message.ID}
	if err := fx.d.publishAccepted(ctx, job); err != nil {
		t.Fatalf("publish accepted: %v", err)
	}
	return "group:" + row.ID
}

func groupOutboxOps(t *testing.T, fx dispatcherFixture, deliveryKey string) []struct {
	Kind       string
	State      string
	AccountKey string
} {
	t.Helper()
	rows, err := fx.db.Query(context.Background(),
		`SELECT operation_kind, state, COALESCE(source_account_key, '') FROM channel_outbox WHERE delivery_key = $1 ORDER BY operation_index`, deliveryKey)
	if err != nil {
		t.Fatalf("query ops: %v", err)
	}
	defer rows.Close()
	var ops []struct {
		Kind       string
		State      string
		AccountKey string
	}
	for rows.Next() {
		var op struct {
			Kind       string
			State      string
			AccountKey string
		}
		if err := rows.Scan(&op.Kind, &op.State, &op.AccountKey); err != nil {
			t.Fatalf("scan op: %v", err)
		}
		ops = append(ops, op)
	}
	return ops
}

func drainGroupOps(t *testing.T, fx dispatcherFixture, sender *accountFenceSender) {
	t.Helper()
	ctx := context.Background()
	s := choutbox.New(fx.db)
	for range 8 {
		if _, err := s.ProcessDue(ctx, "ch-1", "", sender); err != nil {
			t.Fatalf("ProcessDue: %v", err)
		}
	}
}

// The group reply's account fence keys on the account that received the
// trigger — persisted with the message at ingest — never the logical channel
// id. This test drives the real enqueueAccepted path: bot-1's message produces
// ops that only a sender owning bot-1 may deliver.
func TestGroupReplyOpsCarryTriggerAccount(t *testing.T) {
	ts := setupStores(t)
	el := eventlog.NewStore(ts.db)
	coord := &Coordinator{eventLog: el, store: ts.store}

	fx := newTelegramGroupFixture(t, ts.db, coord)

	sender := &accountFenceSender{owns: "bot-1"}
	deliveryKey := enqueueGroupReply(t, fx, coord, "tg-m1", "bot-1")

	ops := groupOutboxOps(t, fx, deliveryKey)
	if len(ops) != 2 { // send_group_reply + send_attachment for the image
		t.Fatalf("enqueued ops = %+v, want 2 ops", ops)
	}
	for _, op := range ops {
		if op.AccountKey != "bot-1" {
			t.Fatalf("op %s carries account %q, want bot-1", op.Kind, op.AccountKey)
		}
	}
	drainGroupOps(t, fx, sender)
	if sender.sentCount() != 2 {
		t.Fatalf("sender calls = %d, want 2", sender.sentCount())
	}
	for _, op := range groupOutboxOps(t, fx, deliveryKey) {
		if op.State != "sent" {
			t.Fatalf("op %s state = %q, want sent", op.Kind, op.State)
		}
	}
}

// After the channel is re-bound to a different platform account, pending
// replies must fail account_mismatch instead of going out under the new
// identity. Rotating credentials under the same account identity still owns
// the work because the fence compares account keys, not credentials.
func TestGroupReplyOpsRejectReboundAccount(t *testing.T) {
	ts := setupStores(t)
	el := eventlog.NewStore(ts.db)
	coord := &Coordinator{eventLog: el, store: ts.store}
	fx := newTelegramGroupFixture(t, ts.db, coord)

	deliveryKey := enqueueGroupReply(t, fx, coord, "tg-m2", "bot-1")

	// The channel now answers to bot-2: every pending op for bot-1 is fenced.
	newOwner := &accountFenceSender{owns: "bot-2"}
	drainGroupOps(t, fx, newOwner)
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

	// A later turn that arrived under bot-2 delivers under bot-2; the account
	// key is bound at ingest, so the fence follows the trigger, not config.
	deliveryKey2 := enqueueGroupReply(t, fx, coord, "tg-m3", "bot-2")
	drainGroupOps(t, fx, newOwner)
	if newOwner.sentCount() != 2 {
		t.Fatalf("new-owner sends = %d, want 2", newOwner.sentCount())
	}
	for _, op := range groupOutboxOps(t, fx, deliveryKey2) {
		if op.State != "sent" {
			t.Fatalf("bot-2 op %s state = %q, want sent", op.Kind, op.State)
		}
	}
}

// newTelegramGroupFixture mirrors newDispatcherFixture with a telegram
// platform group whose member replies through the telegram channel row.
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
	if _, err := db.Exec(ctx, `INSERT INTO channel (id, name, type, agent_id, enabled, config) VALUES ('ch-1', 'TG Bot', 'telegram', 'agent-1', true, '{}')`); err != nil {
		t.Fatalf("create channel: %v", err)
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
		ReplyChannelID: "ch-1",
	}); err != nil {
		t.Fatalf("add group member: %v", err)
	}
	d := NewGroupDispatcher(db, coord, NewPublisherRegistry())
	d.leaseDuration = 0
	d.SetGroupTurnCommitter(groupTurnCommitterFunc(func(context.Context, *sqlc.Queries, memory.DeferredGroupTurn) error { return nil }))
	return dispatcherFixture{db: db, q: q, d: d, groupID: state.ID}
}
