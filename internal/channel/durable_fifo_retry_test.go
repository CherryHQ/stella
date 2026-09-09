package channel

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/sessionmedia"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// A FIFO claim is a Go snapshot. AgentRun linking commits in a separate
// transaction before model dispatch, so RetryChannelFIFOItem must inspect the
// row's run_id itself and refuse to make linked work pending again.
func TestRetryLinkedSnapshotBlocksFromDatabaseRunID(t *testing.T) {
	fx := newDispatcherFixture(t, "telegram", `{}`)
	ctx := context.Background()
	d := &DurableIngress{db: fx.db, owner: "linked-retry-test"}
	payload := json.RawMessage(`{"message":{"platform":"telegram"},"content":[]}`)
	item, inserted, err := d.enqueue(ctx, uuid.NewString(), "linked-retry-source", "ch-1", "telegram", "linked-retry-chat", "", "linked-retry-principal", payload, nil, sessionmedia.Owner{}, nil, "", "chat", "", "", nil)
	if err != nil || !inserted {
		t.Fatalf("enqueue = %s, %v, %v", item.ID, inserted, err)
	}
	claimed, err := sqlc.New(fx.db).ClaimNextChannelFIFOItem(ctx, sqlc.ClaimNextChannelFIFOItemParams{
		LeaseOwner: d.owner, LeaseSeconds: 60,
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.RunID.Valid {
		t.Fatal("claim unexpectedly carried an AgentRun")
	}

	q := sqlc.New(fx.db)
	bootID, runID := uuid.NewString(), uuid.NewString()
	if _, err := q.CreateExecutorBoot(ctx, bootID); err != nil {
		t.Fatalf("create executor boot: %v", err)
	}
	sessionID := "linked-retry-session-" + runID
	if _, err := fx.db.Exec(ctx, `INSERT INTO ctx_conversation (session_id) VALUES ($1)`, sessionID); err != nil {
		t.Fatalf("create run conversation: %v", err)
	}
	run, err := q.CreateAgentRun(ctx, sqlc.CreateAgentRunParams{
		ID: runID, SessionID: sessionID, ExecutorBootID: bootID,
		Source: "channel", LeaseSeconds: 60,
	})
	if err != nil {
		t.Fatalf("create AgentRun: %v", err)
	}
	if _, err := fx.db.Exec(ctx, `UPDATE channel_fifo_item SET run_id = $1 WHERE id = $2`, run.ID, claimed.ID); err != nil {
		t.Fatalf("link AgentRun: %v", err)
	}

	if err := d.retry(ctx, claimed, "dispatch_failed", "model failed after link", false); err != nil {
		t.Fatalf("retry linked snapshot: %v", err)
	}
	var state string
	if err := fx.db.QueryRow(ctx, `SELECT state FROM channel_fifo_item WHERE id = $1`, claimed.ID).Scan(&state); err != nil {
		t.Fatalf("read FIFO state: %v", err)
	}
	if state != "blocked" {
		t.Fatalf("linked retry state = %q, want blocked", state)
	}
}
