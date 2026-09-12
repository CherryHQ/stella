package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/sessionexecution"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestGroupAcceptRejectsOldSessionExecutionBeforeBusinessWrites(t *testing.T) {
	fx := newDispatcherFixture(t, "web", "")
	id := uuid.Must(uuid.NewV7()).String()
	if _, err := fx.q.CreateConversation(t.Context(), sqlc.CreateConversationParams{ID: id, SessionID: id, Kind: "chat", LastActive: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	store := sessionexecution.New(fx.db)
	ctx, old, err := store.Claim(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Finish("error") }()
	// Model a crashed writer: expired lease plus a dead recorded owner pid, so
	// the liveness fence allows the takeover this test exercises.
	if _, err := fx.db.Exec(t.Context(), "UPDATE ctx_session_execution SET lease_until=clock_timestamp()-interval '1 second', owner_pid=1073741824 WHERE session_id=$1", id); err != nil {
		t.Fatal(err)
	}
	_, next, err := store.Claim(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Finish("error") }()
	row := sqlc.CtxGroupDispatch{GroupID: fx.groupID, AgentID: "agent-1"}
	if _, err := fx.d.acceptGroupResponse(context.WithoutCancel(ctx), row, groupResponse{text: "stale", sessionID: id}, memory.DeferredGroupTurn{Complete: true}, nil); !errors.Is(err, sessionexecution.ErrLost) {
		t.Fatalf("stale group accept: %v", err)
	}
	var messages int
	if err := fx.db.QueryRow(t.Context(), "SELECT count(*) FROM ctx_group_message WHERE group_id=$1 AND actor_type='agent'", fx.groupID).Scan(&messages); err != nil || messages != 0 {
		t.Fatalf("stale replies=%d err=%v", messages, err)
	}
}
