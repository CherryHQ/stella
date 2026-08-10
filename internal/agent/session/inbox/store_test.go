package inbox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	sessioninbox "github.com/CherryHQ/stella/internal/agent/session/inbox"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func seedInboxConversations(t *testing.T) (*sessioninbox.Store, *sqlc.Queries, string, string) {
	t.Helper()
	db := dbtest.New(t)
	source := "agent-1:main:user-1"
	target := "agent-1:chat:user-1:target"
	if _, err := db.Exec(t.Context(), `
		INSERT INTO ctx_conversation (id, session_id, kind, agent_id, user_id)
		VALUES ($1, $2, 'main', 'agent-1', 'user-1'), ($3, $4, 'chat', 'agent-1', 'user-1')
	`, uuid.NewString(), source, uuid.NewString(), target); err != nil {
		t.Fatalf("seed conversations: %v", err)
	}
	return sessioninbox.New(db), sqlc.New(db), source, target
}

func TestStoreEnqueueAndFailPendingCAS(t *testing.T) {
	store, q, source, target := seedInboxConversations(t)
	message, err := store.Enqueue(t.Context(), sessioninbox.Input{
		SourceSessionID: source,
		TargetSessionID: target,
		Actor: eventlog.MessageActor{
			Type: eventlog.ActorAgent, ID: "agent-1", SourceSessionID: source,
		},
		Content: "hello",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if message.ID == "" || message.EnqueueSeq <= 0 {
		t.Fatalf("message = %+v, want durable ID and sequence", message)
	}
	if applied, err := store.FailPending(t.Context(), message.ID, sessioninbox.ErrorCanceled); err != nil || !applied {
		t.Fatalf("first FailPending = %v, %v; want true, nil", applied, err)
	}
	if applied, err := store.FailPending(t.Context(), message.ID, sessioninbox.ErrorTimeout); err != nil || applied {
		t.Fatalf("second FailPending = %v, %v; want false, nil", applied, err)
	}
	row, err := q.GetSessionInbox(t.Context(), message.ID)
	if err != nil {
		t.Fatalf("GetSessionInbox: %v", err)
	}
	if !row.FailedAt.Valid || row.DeliveredAt.Valid || row.ErrorCode != string(sessioninbox.ErrorCanceled) {
		t.Fatalf("terminal row = %+v", row)
	}
}

func TestStoreRejectsUntrustedInputAndUnknownCode(t *testing.T) {
	store, _, source, target := seedInboxConversations(t)
	_, err := store.Enqueue(t.Context(), sessioninbox.Input{
		SourceSessionID: source,
		TargetSessionID: target,
		Actor:           eventlog.MessageActor{Type: eventlog.ActorHuman, ID: "user-1", SourceSessionID: source},
		Content:         "hello",
	})
	if err == nil {
		t.Fatal("Enqueue accepted human-authored inbox input")
	}
	if _, err := store.FailPending(t.Context(), uuid.NewString(), sessioninbox.ErrorCode("raw provider error")); err == nil {
		t.Fatal("FailPending accepted an unknown error code")
	}
}

func TestStoreDoesNotPersistAfterCanceledContext(t *testing.T) {
	store, _, source, target := seedInboxConversations(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.Enqueue(ctx, sessioninbox.Input{
		SourceSessionID: source,
		TargetSessionID: target,
		Actor: eventlog.MessageActor{
			Type: eventlog.ActorAgent, ID: "agent-1", SourceSessionID: source,
		},
		Content: "hello",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Enqueue error = %v, want context.Canceled", err)
	}
}
