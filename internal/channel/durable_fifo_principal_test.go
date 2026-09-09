package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// A channel identity can be rebound after admission. The payload still has
// the old platform sender, so dequeue resolution may select a new session. The
// AgentRun link must reject that source-principal drift in its admission
// transaction, before the runtime can invoke a runner.
func TestDurableIngressRejectsPrincipalDriftBeforeRun(t *testing.T) {
	c, db, _, originalUserID := newDurableRuntimeAcceptanceCoordinator(t)
	d := NewDurableIngress(db, "principal-drift")
	d.BindCoordinator(c)
	ctx := context.Background()
	_, handled, stream, err := d.Admit(ctx, pkgchannel.IncomingMessage{
		MessageID: "principal-drift-1", Platform: "telegram", ChannelID: "telegram", SenderID: "runtime-sender",
		ChatID: "runtime-chat", Content: []ai.ContentBlock{ai.TextContent{Text: "old principal"}},
	}, "", "")
	if err != nil || handled || stream == nil {
		t.Fatalf("Admit = handled:%v stream:%v err:%v", handled, stream != nil, err)
	}

	var agentID string
	if err := db.QueryRow(ctx, `SELECT agent_id FROM auth_user_agent WHERE user_id = $1 LIMIT 1`, originalUserID).Scan(&agentID); err != nil {
		t.Fatalf("read original agent assignment: %v", err)
	}
	newUserID := uuid.NewString()
	if _, err := db.Exec(ctx, `
INSERT INTO auth_user (id, email, name, default_agent_id)
VALUES ($1, $2, 'Rebound User', $3)`,
		newUserID, "principal-drift-rebound-"+newUserID+"@example.test", agentID); err != nil {
		t.Fatalf("create rebound user: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO auth_user_agent (user_id, agent_id) VALUES ($1, $2)`, newUserID, agentID); err != nil {
		t.Fatalf("assign rebound user: %v", err)
	}
	if _, err := db.Exec(ctx, `
UPDATE channel_identity
SET user_id = $1, updated_at = now()
WHERE platform = 'telegram' AND external_id = 'runtime-sender'`, newUserID); err != nil {
		t.Fatalf("rebind platform identity: %v", err)
	}

	processDone := make(chan error, 1)
	go func() { processDone <- d.processOne(ctx) }()
	select {
	case event := <-stream.Events:
		if event.Err == nil {
			t.Fatalf("principal drift event = %+v, want admission error", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("principal drift did not surface an admission error")
	}
	if err := stream.Completion.Ack(ctx, pkgchannel.EgressUnknown); err != nil {
		if !errors.Is(err, errDurableSourcePrincipal) {
			t.Fatalf("principal drift Ack: %v", err)
		}
	}
	select {
	case err := <-processDone:
		if err != nil && !errors.Is(err, errDurableSourcePrincipal) {
			t.Fatalf("processOne after principal drift: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("principal drift consumer did not settle")
	}
	// The rejected waiter has no model stream. Remove it so the test does not
	// retain a listener-owned entry after the durable item is blocked.
	var item sqlc.ChannelFifoItem
	bindingID := mustBindingID(t, db, "runtime-chat")
	item, err = sqlc.New(db).GetChannelFIFOItemBySource(ctx, sqlc.GetChannelFIFOItemBySourceParams{
		BindingID: bindingID, SourceKey: "message:principal-drift-1",
	})
	if err != nil {
		t.Fatalf("read drifted FIFO item: %v", err)
	}
	d.takeWaiter(item.ID)
	if item.State != "blocked" {
		t.Fatalf("drifted FIFO state = %q, want blocked", item.State)
	}
	if item.ErrorCode != "egress_unknown" {
		t.Fatalf("drifted FIFO error code = %q, want egress_unknown", item.ErrorCode)
	}

	var runs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM agent_run WHERE source LIKE 'runtime:%'`).Scan(&runs); err != nil {
		t.Fatalf("count AgentRuns: %v", err)
	}
	if runs != 0 {
		t.Fatalf("principal drift created %d AgentRuns, want zero", runs)
	}
	select {
	case event, ok := <-stream.Events:
		if ok {
			t.Fatalf("principal drift emitted model event: %+v", event)
		}
	case <-time.After(20 * time.Millisecond):
	}
}
