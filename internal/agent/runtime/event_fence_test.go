package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type fencedEventRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *fencedEventRunner) Chat(context.Context, []ai.Message, MessageContent) <-chan Event {
	out := make(chan Event, 1)
	close(r.started)
	go func() {
		<-r.release
		out <- Event{Text: "stale buffered delta"}
		close(out)
	}()
	return out
}

func (*fencedEventRunner) Alive() bool             { return true }
func (*fencedEventRunner) Busy() bool              { return false }
func (*fencedEventRunner) LastActivity() time.Time { return time.Now() }
func (*fencedEventRunner) SystemPrompt() string    { return "" }
func (*fencedEventRunner) Close() error            { return nil }

func newRuntimeAgentRunStore(t *testing.T, db *pgxpool.Pool) *agentrun.Store {
	t.Helper()
	bootID := agentrun.NewBootID()
	if _, err := sqlc.New(db).CreateExecutorBoot(t.Context(), sqlc.CreateExecutorBootParams{ID: bootID}); err != nil {
		t.Fatalf("create executor boot: %v", err)
	}
	return agentrun.NewStore(db, bootID)
}

func createRuntimeConversation(t *testing.T, db *pgxpool.Pool, sessionID string) {
	t.Helper()
	_, err := sqlc.New(db).CreateConversation(t.Context(), sqlc.CreateConversationParams{
		ID: uuid.Must(uuid.NewV7()).String(), SessionID: sessionID,
		Channel: "web", Kind: "chat", LastActive: time.Now().UTC(),
		AgentID: pgtype.Text{String: "agent-1", Valid: true},
		UserID:  pgtype.Text{String: uuid.Must(uuid.NewV7()).String(), Valid: true},
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
}

func TestRuntimeDropsBufferedEventsAtFinalFanoutAfterRemoteAbort(t *testing.T) {
	db := dbtest.New(t)
	const sessionID = "runtime-event-fence"
	createRuntimeConversation(t, db, sessionID)
	owner := newRuntimeAgentRunStore(t, db)
	remote := newRuntimeAgentRunStore(t, db)
	runner := &fencedEventRunner{started: make(chan struct{}), release: make(chan struct{})}
	rt, err := New(Config{
		Memory:    &recordingMemory{},
		AgentRuns: owner,
		NewRunner: func(context.Context, RunnerParams) (Runner, error) { return runner, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	stream, err := rt.ChatAdmitted(t.Context(), session.Info{
		ID: sessionID, UserID: "user-1", AgentID: "agent-1", Kind: "chat", Channel: "web",
	}, "hello")
	if err != nil {
		t.Fatalf("admit chat: %v", err)
	}
	attached, cancel := rt.Subscribe(sessionID)
	defer cancel()
	<-runner.started
	if runID, err := remote.RequestAbort(t.Context(), sessionID, "remote replacement"); err != nil || runID == "" {
		t.Fatalf("remote abort run=%q err=%v", runID, err)
	}
	close(runner.release)

	for event := range stream {
		t.Fatalf("initiating stream received stale event: %#v", event)
	}
	for event := range attached {
		t.Fatalf("hub subscriber received stale event: %#v", event)
	}
}
