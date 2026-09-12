package channel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	choutbox "github.com/CherryHQ/stella/internal/channel/outbox"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// The accept transaction rolls back as one unit: a failure after the real
// group-message, memory, outbox and attachment writes must leave none of them
// behind. An AFTER INSERT trigger on the attachment table is the injected
// fault — it sees the bytea row it just inserted, then fails the tx.
func TestAcceptRollbackDropsMessageMemoryOpsAndAttachment(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	ctx := context.Background()
	lcmP, err := lcm.New(fx.db, nil, nil)
	if err != nil {
		t.Fatalf("lcm provider: %v", err)
	}
	fx.d.SetGroupTurnCommitter(lcmP)

	if _, err := fx.db.Exec(ctx, `
CREATE OR REPLACE FUNCTION fail_outbox_attachment() RETURNS trigger AS $$
BEGIN
  PERFORM 1 FROM channel_outbox_attachment WHERE outbox_id = NEW.outbox_id;
  IF NOT FOUND THEN RAISE EXCEPTION 'attachment row missing inside own insert trigger'; END IF;
  RAISE EXCEPTION 'E2E_INJECTED_ATTACHMENT_FAILURE';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER fail_outbox_attachment AFTER INSERT ON channel_outbox_attachment
FOR EACH ROW EXECUTE FUNCTION fail_outbox_attachment()`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fx.db.Exec(context.Background(),
			`DROP TRIGGER IF EXISTS fail_outbox_attachment ON channel_outbox_attachment; DROP FUNCTION IF EXISTS fail_outbox_attachment()`)
	})

	file := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(file, []byte("ORIGINAL-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-0000000000b1", fx.message.ID, fx.groupID, "agent-1", "running", 0, pgtype.Timestamptz{})
	row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-0000000000b1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = commitGroupReply(t, fx, row,
		[]agentruntime.Event{
			{Text: "group reply"},
			{File: &agentruntime.FileEvent{Path: file}},
		},
		memory.DeferredGroupTurn{
			Complete:   true,
			Session:    memory.Session{ID: "agent-1:group:" + fx.groupID, GroupID: fx.groupID, AgentID: "agent-1", UserID: fx.groupID},
			OwnRows:    []ai.Message{ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "group reply"}}}},
			TriggerSeq: fx.message.Seq,
		})
	if err == nil {
		t.Fatal("accept must surface the injected attachment failure")
	}
	if !strings.Contains(err.Error(), "E2E_INJECTED_ATTACHMENT_FAILURE") {
		t.Fatalf("accept failed before reaching the attachment write: %v", err)
	}

	if n := countAgentGroupMessages(t, fx.db); n != 0 {
		t.Fatalf("accepted group messages = %d, want 0 after rollback", n)
	}
	var ops, attachments, convs int
	if err := fx.db.QueryRow(ctx, `SELECT count(*) FROM channel_outbox WHERE delivery_key = 'group:d15a0000-0000-0000-0000-0000000000b1'`).Scan(&ops); err != nil {
		t.Fatal(err)
	}
	if err := fx.db.QueryRow(ctx, `SELECT count(*) FROM channel_outbox_attachment`).Scan(&attachments); err != nil {
		t.Fatal(err)
	}
	if err := fx.db.QueryRow(ctx, `SELECT count(*) FROM ctx_conversation WHERE session_id = 'agent-1:group:' || $1`, fx.groupID).Scan(&convs); err != nil {
		t.Fatal(err)
	}
	if ops != 0 || attachments != 0 || convs != 0 {
		t.Fatalf("rollback left rows: ops=%d attachments=%d conversations=%d", ops, attachments, convs)
	}
	dispatch, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-0000000000b1")
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.ResultMessageID != "" || dispatch.Status != "running" {
		t.Fatalf("dispatch moved after rollback: status=%s result=%q", dispatch.Status, dispatch.ResultMessageID)
	}
}

// The committed op chain is the only copy a recovering sender needs: the
// workspace path is consumed at prepare, so deleting or rewriting the source
// file afterwards cannot change what another replica's send boundary reads.
func TestGroupReplyDeliversCommittedAttachmentAfterSourceChange(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	ctx := context.Background()

	file := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(file, []byte("ORIGINAL-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	insertGroupDispatch(t, fx.db, "d15a0000-0000-0000-0000-0000000000b2", fx.message.ID, fx.groupID, "agent-1", "running", 0, pgtype.Timestamptz{})
	row, err := fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-0000000000b2")
	if err != nil {
		t.Fatal(err)
	}
	result, err := commitGroupReply(t, fx, row,
		[]agentruntime.Event{
			{Text: "group reply"},
			{File: &agentruntime.FileEvent{Path: file}},
		},
		memory.DeferredGroupTurn{Complete: true})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !result.Enqueued {
		t.Fatal("accepted reply did not commit its outbox chain")
	}
	ops, err := fx.d.publish.outbox.ListByDelivery(ctx, "group:d15a0000-0000-0000-0000-0000000000b2")
	if err != nil || len(ops) != 2 {
		t.Fatalf("ops = %d, err = %v, want text + attachment", len(ops), err)
	}

	// The source file mutates, then disappears — the committed bytes must not.
	if err := os.WriteFile(file, []byte("REWRITTEN"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}

	// A different replica's send boundary: a fresh Coordinator whose only
	// state is the shared database.
	otherOpener := &Coordinator{db: fx.db}
	sender := &attachmentSender{handler: otherOpener}
	// Ops are chained: the attachment only becomes due once the text op lands.
	sent := 0
	for range 4 {
		n, err := fx.d.publish.outbox.ProcessDue(ctx, "ch-1", "", sender)
		if err != nil {
			t.Fatalf("ProcessDue: %v", err)
		}
		sent += n
		if n == 0 {
			break
		}
	}
	if sent != 2 {
		t.Fatalf("sent ops = %d, want text + attachment", sent)
	}
	got, ok := sender.attachments["group:d15a0000-0000-0000-0000-0000000000b2"]
	if !ok {
		t.Fatalf("attachment op never reached the sender: %#v", sender.attachments)
	}
	if string(got) != "ORIGINAL-BYTES" {
		t.Fatalf("delivered bytes = %q, want the committed ORIGINAL-BYTES", got)
	}
	if err := fx.d.pollPublishOutcomes(ctx); err != nil {
		t.Fatalf("poll outcomes: %v", err)
	}
	row, _ = fx.q.GetGroupDispatch(ctx, "d15a0000-0000-0000-0000-0000000000b2")
	if row.Status != "completed" {
		t.Fatalf("dispatch = %s, want completed after both ops sent", row.Status)
	}
	msg, err := fx.q.GetGroupMessage(ctx, result.Accepted.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.DeliveryState != "delivered" {
		t.Fatalf("delivery = %s", msg.DeliveryState)
	}
}

// attachmentSender resolves a file op's bytes the way a platform adapter does:
// through the send boundary's AttachmentOpener, never the workspace path.
type attachmentSender struct {
	handler     pkgchannel.Handler
	attachments map[string][]byte
}

func (s *attachmentSender) SendOperation(ctx context.Context, op pkgchannel.OutboundOp) (pkgchannel.SendResult, error) {
	if s.attachments == nil {
		s.attachments = map[string][]byte{}
	}
	if op.Kind == choutbox.OpSendAttachment {
		data, err := pkgchannel.OpenAttachmentOp(ctx, s.handler, op)
		if err != nil {
			return pkgchannel.SendResult{}, err
		}
		s.attachments[op.DeliveryKey] = data
	}
	return pkgchannel.SendResult{PlatformMessageID: "sent"}, nil
}
