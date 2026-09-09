package lcm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/runcontrol"
)

func terminalRunGuard(t *testing.T, db *pgxpool.Pool, sessionID string) agentrun.Guard {
	t.Helper()
	q := sqlc.New(db)
	bootID, runID := uuid.NewString(), uuid.NewString()
	if _, err := q.CreateExecutorBoot(t.Context(), bootID); err != nil {
		t.Fatalf("create executor boot: %v", err)
	}
	if _, err := q.CreateAgentRun(t.Context(), sqlc.CreateAgentRunParams{
		ID:             runID,
		SessionID:      sessionID,
		ExecutorBootID: bootID,
		Source:         "run-fence-test",
		LeaseSeconds:   60,
	}); err != nil {
		t.Fatalf("create agent run: %v", err)
	}
	completedSession, err := q.CompleteAgentRunWithActivity(t.Context(), sqlc.CompleteAgentRunWithActivityParams{
		RunID:             runID,
		ExecutorBootID:    bootID,
		Status:            agentrun.StatusCompleted,
		Reason:            "run-fence-test",
		CompletionOutcome: string(runcontrol.OutcomeDelivered),
		TurnResult:        pgtype.Text{String: "success", Valid: true},
	})
	if err != nil {
		t.Fatalf("complete agent run: %v", err)
	}
	if completedSession != sessionID {
		t.Fatalf("completed session = %q, want %q", completedSession, sessionID)
	}
	return agentrun.Guard{RunID: runID, SessionID: sessionID, ExecutorBootID: bootID}
}

func TestRunFenceCoversTranscriptAndProfileWrites(t *testing.T) {
	db := newLCMTestDB(t)
	t.Cleanup(db.Close)
	p, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	session := newLCMTestSession("run-fence-" + uuid.NewString())
	base := authz.WithAgentID(authz.WithUserID(t.Context(), session.UserID), session.AgentID)
	info := memory.SessionInfo{
		ID:         session.ID,
		AgentID:    session.AgentID,
		UserID:     session.UserID,
		Channel:    session.Channel,
		Kind:       "chat",
		LastActive: time.Now().UTC(),
	}
	if err := p.SaveInfo(base, info); err != nil {
		t.Fatalf("unguarded SaveInfo: %v", err)
	}

	terminalGuard := terminalRunGuard(t, db, session.ID)
	missingGuard := agentrun.Guard{
		RunID:          uuid.NewString(),
		SessionID:      session.ID,
		ExecutorBootID: uuid.NewString(),
	}
	malformedGuard := agentrun.Guard{SessionID: session.ID, ExecutorBootID: uuid.NewString()}
	guarded := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "terminal", ctx: agentrun.WithGuard(base, terminalGuard), want: agentrun.ErrLeaseLost},
		{name: "missing", ctx: agentrun.WithGuard(base, missingGuard), want: agentrun.ErrLeaseLost},
		{name: "malformed", ctx: agentrun.WithGuard(base, malformedGuard), want: agentrun.ErrInvalidGuard},
	}

	q := sqlc.New(db)
	conversation, err := q.GetConversationForSessionAccess(base, session.ID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	for _, tc := range guarded {
		t.Run(tc.name, func(t *testing.T) {
			if err := p.Append(tc.ctx, session, ai.UserMessage{Content: "blocked-" + tc.name}); !errors.Is(err, tc.want) {
				t.Fatalf("guarded Append = %v, want %v", err, tc.want)
			}
			count, err := q.GetMessageCount(base, conversation.ID)
			if err != nil {
				t.Fatalf("count transcript rows: %v", err)
			}
			if count != 0 {
				t.Fatalf("guarded Append left %d transcript rows", count)
			}

			if err := p.SetProfile(tc.ctx, session.UserID, session.AgentID, "blocked-"+tc.name); !errors.Is(err, tc.want) {
				t.Fatalf("guarded SetProfile = %v, want %v", err, tc.want)
			}
			if _, err := q.GetUserAgentMemory(base, sqlc.GetUserAgentMemoryParams{UserID: session.UserID, AgentID: session.AgentID}); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("guarded SetProfile memory row error = %v, want pgx.ErrNoRows", err)
			}
		})
	}

	if err := p.Append(base, session, ai.UserMessage{Content: "allowed"}); err != nil {
		t.Fatalf("unguarded Append: %v", err)
	}
	messages, err := q.GetMessagesByConversation(base, conversation.ID)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "allowed" {
		t.Fatalf("unguarded transcript = %#v, want one allowed message", messages)
	}

	if err := p.SetProfile(base, session.UserID, session.AgentID, "allowed profile"); err != nil {
		t.Fatalf("unguarded SetProfile: %v", err)
	}
	if got, err := p.GetProfile(base, session.UserID, session.AgentID); err != nil || got != "allowed profile" {
		t.Fatalf("unguarded profile = %q, %v", got, err)
	}

	// MarkSessionViewed is presentation-only. A stale execution guard must not
	// block it because it does not mutate model-authored durable state.
	viewed, err := p.MarkSessionViewed(guarded[0].ctx, session)
	if err != nil || !viewed {
		t.Fatalf("stale MarkSessionViewed = %v, %v", viewed, err)
	}
	loaded, err := p.LoadInfo(base, session.ID)
	if err != nil {
		t.Fatalf("load viewed session: %v", err)
	}
	if loaded.LastViewedAt.IsZero() {
		t.Fatal("stale MarkSessionViewed did not update presentation watermark")
	}
}
