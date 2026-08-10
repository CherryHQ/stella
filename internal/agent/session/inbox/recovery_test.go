package inbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	sessioninbox "github.com/CherryHQ/stella/internal/agent/session/inbox"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type recoveryResolver struct {
	target agentsession.Info
	err    error
	calls  int
}

func (r *recoveryResolver) ResolveInboxDelivery(context.Context, string, string, string) (agentsession.Info, error) {
	r.calls++
	return r.target, r.err
}

func newRecoveryTest(t *testing.T) (*sessioninbox.Store, *lcm.Provider, *sqlc.Queries, memory.Session, memory.Session) {
	t.Helper()
	db := dbtest.New(t)
	provider, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("lcm.New: %v", err)
	}
	userID := uuid.NewString()
	source := memory.Session{ID: "agent-1:main:" + userID, UserID: userID, AgentID: "agent-1", Channel: "web"}
	target := memory.Session{ID: "agent-1:chat:" + userID + ":target", UserID: userID, AgentID: "agent-1", Channel: "web"}
	now := time.Now().UTC()
	ctx := authz.WithAgentID(authz.WithUserID(t.Context(), userID), "agent-1")
	manager := memory.SessionManager(provider)
	for _, item := range []struct {
		session memory.Session
		kind    string
	}{{source, string(agentsession.KindMain)}, {target, string(agentsession.KindChat)}} {
		if err := manager.SaveInfo(ctx, memory.SessionInfo{
			ID: item.session.ID, UserID: item.session.UserID, AgentID: item.session.AgentID,
			Channel: item.session.Channel, Kind: item.kind, CreatedAt: now, LastActive: now,
		}); err != nil {
			t.Fatalf("SaveInfo(%s): %v", item.session.ID, err)
		}
	}
	return sessioninbox.New(db), provider, sqlc.New(db), source, target
}

func enqueueRecoveryMessage(t *testing.T, store *sessioninbox.Store, source, target memory.Session, content string) sessioninbox.Message {
	t.Helper()
	message, err := store.Enqueue(t.Context(), sessioninbox.Input{
		SourceSessionID: source.ID,
		TargetSessionID: target.ID,
		Actor: eventlog.MessageActor{
			Type: eventlog.ActorAgent, ID: source.AgentID, SourceSessionID: source.ID,
		},
		Content: content,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return message
}

func recoveryTargetInfo(target memory.Session) agentsession.Info {
	return agentsession.Info{
		ID: target.ID, UserID: target.UserID, AgentID: target.AgentID,
		Channel: target.Channel, Kind: string(agentsession.KindChat),
	}
}

func TestRecoverAppendsPendingInputsInOrderWithoutReplay(t *testing.T) {
	store, provider, q, source, target := newRecoveryTest(t)
	first := enqueueRecoveryMessage(t, store, source, target, "first")
	second := enqueueRecoveryMessage(t, store, source, target, "second")
	resolver := &recoveryResolver{target: recoveryTargetInfo(target)}

	if err := store.Recover(t.Context(), resolver, provider); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	// A second startup pass is a no-op, proving terminal rows are not replayed.
	if err := store.Recover(t.Context(), resolver, provider); err != nil {
		t.Fatalf("second Recover: %v", err)
	}
	history, err := provider.LoadHistory(authz.WithAgentID(authz.WithUserID(t.Context(), target.UserID), target.AgentID), target.ID)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(history) != 2 || !strings.Contains(memory.MessageText(history[0]), `"content":"first"`) || !strings.Contains(memory.MessageText(history[1]), `"content":"second"`) {
		t.Fatalf("history = %#v, want ordered information-only first/second inputs", history)
	}
	for _, id := range []string{first.ID, second.ID} {
		row, err := q.GetSessionInbox(t.Context(), id)
		if err != nil {
			t.Fatalf("GetSessionInbox(%s): %v", id, err)
		}
		if !row.DeliveredAt.Valid || row.FailedAt.Valid {
			t.Fatalf("row %s state = delivered:%v failed:%v", id, row.DeliveredAt.Valid, row.FailedAt.Valid)
		}
	}
	if resolver.calls != 2 {
		t.Fatalf("resolver calls = %d, want 2; second recovery must skip terminal rows", resolver.calls)
	}
}

func TestRecoverTerminalizesPermanentlyUnavailableTarget(t *testing.T) {
	store, _, q, source, target := newRecoveryTest(t)
	message := enqueueRecoveryMessage(t, store, source, target, "cannot deliver")
	resolver := &recoveryResolver{err: sessioninbox.ErrTargetUnavailable}

	if err := store.Recover(t.Context(), resolver, &failingRecoveryAppender{err: errors.New("must not append")}); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	row, err := q.GetSessionInbox(t.Context(), message.ID)
	if err != nil {
		t.Fatalf("GetSessionInbox: %v", err)
	}
	if !row.FailedAt.Valid || row.ErrorCode != string(sessioninbox.ErrorTargetUnavailable) || row.DeliveredAt.Valid {
		t.Fatalf("row state = %+v", row)
	}
}

func TestRecoverTransientAuthorizationFailureAbortsAndLeavesPending(t *testing.T) {
	store, _, q, source, target := newRecoveryTest(t)
	message := enqueueRecoveryMessage(t, store, source, target, "retry next startup")
	transient := errors.New("database unavailable")
	resolver := &recoveryResolver{err: transient}

	err := store.Recover(t.Context(), resolver, &failingRecoveryAppender{err: errors.New("must not append")})
	if !errors.Is(err, transient) {
		t.Fatalf("Recover error = %v, want transient error", err)
	}
	row, err := q.GetSessionInbox(t.Context(), message.ID)
	if err != nil {
		t.Fatalf("GetSessionInbox: %v", err)
	}
	if row.DeliveredAt.Valid || row.FailedAt.Valid {
		t.Fatalf("transient failure changed row: %+v", row)
	}
}

type failingRecoveryAppender struct{ err error }

func (a *failingRecoveryAppender) AppendInboxInput(context.Context, memory.Session, string, ai.Message) error {
	return a.err
}
