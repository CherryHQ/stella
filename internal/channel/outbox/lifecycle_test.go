package outbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// durableChain is one fully-populated session spine: conversation → message,
// session execution, session events, agent_run → channel_outbox → bytea
// attachment. Owner deletion must take the whole chain or none of it.
type durableChain struct {
	conversationID string
	sessionID      string
	runID          string
	outboxID       string
}

func seedAgent(t *testing.T, db *pgxpool.Pool, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := sqlc.New(db).CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: id, Name: id, Workspace: t.TempDir(), Sandbox: json.RawMessage(`{}`),
		Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
}

func seedDurableChain(t *testing.T, db *pgxpool.Pool, sessionID, suffix string, conv sqlc.CreateConversationParams) durableChain {
	t.Helper()
	ctx := context.Background()
	q := sqlc.New(db)
	conv.SessionID = sessionID
	conv.Channel = "web"
	conv.Kind = "chat"
	conv.LastActive = time.Now().UTC()
	if _, err := q.CreateConversation(ctx, conv); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{
		ID: "10000000-0000-0000-0000-00000000" + suffix, ConversationID: conv.ID,
		Seq: 1, Role: "user", EventType: "message", Content: "hi", ActorType: "human",
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}
	if _, err := q.ClaimSessionExecution(ctx, sqlc.ClaimSessionExecutionParams{
		SessionID: sessionID, Token: "20000000-0000-0000-0000-00000000" + suffix,
	}); err != nil {
		t.Fatalf("claim execution: %v", err)
	}
	run, err := q.CreateAgentRun(ctx, sqlc.CreateAgentRunParams{
		SessionID: sessionID, AgentID: "agent-lc", RequestKey: "req-" + suffix,
		Actor: json.RawMessage(`{}`), Input: json.RawMessage(`{}`), ReplyAddress: json.RawMessage(`{}`),
		EnqueueSeq: 0,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := q.InsertSessionEvent(ctx, sqlc.InsertSessionEventParams{
		SessionID: sessionID, RunID: pgtype.Text{String: run.ID, Valid: true},
		Seq: 1, Event: json.RawMessage(`{"text":"hi"}`),
	}); err != nil {
		t.Fatalf("insert session event: %v", err)
	}
	opRow, err := q.CreateChannelOutbox(ctx, sqlc.CreateChannelOutboxParams{
		RunID: pgtype.Text{String: run.ID, Valid: true}, DeliveryKey: "del-" + suffix,
		OperationIndex: 0, OperationKind: OpSendAttachment, ChannelID: "ch-lc",
		Address: json.RawMessage(`{}`), Payload: json.RawMessage(`{}`), DependsOn: json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatalf("create outbox op: %v", err)
	}
	if err := q.CreateChannelOutboxAttachment(ctx, sqlc.CreateChannelOutboxAttachmentParams{
		OutboxID: opRow.ID, Data: []byte("COMMITTED-BYTES"),
	}); err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	return durableChain{conversationID: conv.ID, sessionID: sessionID, runID: run.ID, outboxID: opRow.ID}
}

func assertChainRows(t *testing.T, db *pgxpool.Pool, c durableChain, want int) {
	t.Helper()
	ctx := context.Background()
	counts := map[string]int{}
	for _, q := range []struct {
		name string
		sql  string
		arg  string
	}{
		{"ctx_conversation", `SELECT count(*) FROM ctx_conversation WHERE id = $1`, c.conversationID},
		{"ctx_message", `SELECT count(*) FROM ctx_message WHERE conversation_id = $1`, c.conversationID},
		{"ctx_session_execution", `SELECT count(*) FROM ctx_session_execution WHERE session_id = $1`, c.sessionID},
		{"ctx_session_event", `SELECT count(*) FROM ctx_session_event WHERE session_id = $1`, c.sessionID},
		{"agent_run", `SELECT count(*) FROM agent_run WHERE id = $1`, c.runID},
		{"channel_outbox", `SELECT count(*) FROM channel_outbox WHERE id = $1`, c.outboxID},
		{"channel_outbox_attachment", `SELECT count(*) FROM channel_outbox_attachment WHERE outbox_id = $1`, c.outboxID},
	} {
		var n int
		if err := db.QueryRow(ctx, q.sql, q.arg).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", q.name, err)
		}
		counts[q.name] = n
	}
	for table, n := range counts {
		if n != want {
			t.Fatalf("%s rows = %d, want %d (all counts: %v)", table, n, want, counts)
		}
	}
}

// FK verification for a physical DELETE on ctx_conversation (the session API
// archives; hard delete happens through owner cascades): the whole durable
// spine — transcript, execution lease, event log, run, pending outbox op and
// its attachment bytes — commits or rolls back with that one row.
func TestConversationDeleteCascadeCommitAndRollback(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	seedAgent(t, db, "agent-lc")

	t.Run("commit", func(t *testing.T) {
		c := seedDurableChain(t, db, "sess-lc-commit", "0001", sqlc.CreateConversationParams{
			ID:     "30000000-0000-0000-0000-000000000001",
			UserID: pgtype.Text{String: "user-lc", Valid: true}, AgentID: pgtype.Text{String: "agent-lc", Valid: true},
		})
		if _, err := db.Exec(ctx, `DELETE FROM ctx_conversation WHERE id = $1`, c.conversationID); err != nil {
			t.Fatalf("delete conversation: %v", err)
		}
		assertChainRows(t, db, c, 0)
	})

	t.Run("rollback", func(t *testing.T) {
		c := seedDurableChain(t, db, "sess-lc-rollback", "0002", sqlc.CreateConversationParams{
			ID:     "30000000-0000-0000-0000-000000000002",
			UserID: pgtype.Text{String: "user-lc", Valid: true}, AgentID: pgtype.Text{String: "agent-lc", Valid: true},
		})
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `DELETE FROM ctx_conversation WHERE id = $1`, c.conversationID); err != nil {
			t.Fatalf("delete in tx: %v", err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		assertChainRows(t, db, c, 1)
	})
}

// An unlinked guest principal owns its chat conversation: deleting the guest
// cascades the conversation and everything under it, and a rolled-back guest
// delete keeps the chain intact.
func TestGuestDeleteCascadeCommitAndRollback(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	seedAgent(t, db, "agent-lc")
	createChannel(t, db, "ch-lc")
	guestID := "40000000-0000-0000-0000-000000000001"
	if _, err := sqlc.New(db).CreateChannelGuest(ctx, sqlc.CreateChannelGuestParams{
		ID: guestID, ChannelID: "ch-lc", Platform: "test", ExternalID: "ext-1", MaxGuests: 10,
	}); err != nil {
		t.Fatalf("create guest: %v", err)
	}
	c := seedDurableChain(t, db, "sess-lc-guest", "0003", sqlc.CreateConversationParams{
		ID:     "30000000-0000-0000-0000-000000000003",
		UserID: pgtype.Text{String: guestID, Valid: true}, AgentID: pgtype.Text{String: "agent-lc", Valid: true},
		GuestID: pgtype.Text{String: guestID, Valid: true},
	})

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM channel_guest WHERE id = $1`, guestID); err != nil {
		t.Fatalf("delete guest in tx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	assertChainRows(t, db, c, 1)
	var guests int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_guest WHERE id = $1`, guestID).Scan(&guests); err != nil || guests != 1 {
		t.Fatalf("guest after rollback = %d, err=%v", guests, err)
	}

	if _, err := db.Exec(ctx, `DELETE FROM channel_guest WHERE id = $1`, guestID); err != nil {
		t.Fatalf("delete guest: %v", err)
	}
	assertChainRows(t, db, c, 0)
}

// Group-anchored outbox ops have no run_id; their lifecycle is the group row.
// Deleting the group drops pending sends and their attachment bytes — and a
// rolled-back delete leaves them sendable.
func TestGroupDeleteCascadeCommitAndRollback(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	groupID := "50000000-0000-0000-0000-000000000001"
	if _, err := sqlc.New(db).CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID: groupID, Platform: "test", PlatformGroupID: "pg-1", GroupName: "g",
	}); err != nil {
		t.Fatalf("create group: %v", err)
	}
	opRow, err := sqlc.New(db).CreateChannelOutbox(ctx, sqlc.CreateChannelOutboxParams{
		DeliveryKey: "del-group-1", OperationIndex: 0, OperationKind: OpSendAttachment,
		ChannelID: "ch-lc", Address: json.RawMessage(`{}`), Payload: json.RawMessage(`{}`),
		DependsOn: json.RawMessage(`[]`), GroupID: pgtype.Text{String: groupID, Valid: true},
	})
	if err != nil {
		t.Fatalf("create group op: %v", err)
	}
	if err := sqlc.New(db).CreateChannelOutboxAttachment(ctx, sqlc.CreateChannelOutboxAttachmentParams{
		OutboxID: opRow.ID, Data: []byte("GROUP-BYTES"),
	}); err != nil {
		t.Fatalf("create attachment: %v", err)
	}

	count := func() (ops, attachments int) {
		if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_outbox WHERE id = $1`, opRow.ID).Scan(&ops); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM channel_outbox_attachment WHERE outbox_id = $1`, opRow.ID).Scan(&attachments); err != nil {
			t.Fatal(err)
		}
		return
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM ctx_group_state WHERE id = $1`, groupID); err != nil {
		t.Fatalf("delete group in tx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if ops, attachments := count(); ops != 1 || attachments != 1 {
		t.Fatalf("after rollback: ops=%d attachments=%d", ops, attachments)
	}

	if _, err := db.Exec(ctx, `DELETE FROM ctx_group_state WHERE id = $1`, groupID); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	if ops, attachments := count(); ops != 0 || attachments != 0 {
		t.Fatalf("after commit: ops=%d attachments=%d", ops, attachments)
	}
}
