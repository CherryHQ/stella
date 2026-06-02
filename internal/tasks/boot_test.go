package tasks

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"testing"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// fakeSessionProvider implements memory.Provider and memory.SessionManager.
// Only LoadInfo carries behavior; everything else is an inert stub.
type fakeSessionProvider struct {
	info    memory.SessionInfo
	loadErr error
	calls   int
}

func (f *fakeSessionProvider) Name() string                                    { return "fake" }
func (f *fakeSessionProvider) Bootstrap(context.Context, memory.Session) error { return nil }
func (f *fakeSessionProvider) Append(context.Context, memory.Session, ...ai.Message) error {
	return nil
}

func (f *fakeSessionProvider) Assemble(context.Context, memory.Session, int, int) ([]ai.Message, error) {
	return nil, nil
}

func (f *fakeSessionProvider) Stats(context.Context, memory.Session) (memory.SessionStats, error) {
	return memory.SessionStats{}, nil
}
func (f *fakeSessionProvider) Close() error { return nil }

func (f *fakeSessionProvider) SaveInfo(context.Context, memory.SessionInfo) error { return nil }
func (f *fakeSessionProvider) LoadInfo(_ context.Context, _ string) (memory.SessionInfo, error) {
	f.calls++
	if f.loadErr != nil {
		return memory.SessionInfo{}, f.loadErr
	}
	return f.info, nil
}

func (f *fakeSessionProvider) ListInfo(context.Context, memory.ListOptions) ([]memory.SessionInfo, error) {
	return nil, nil
}

func (f *fakeSessionProvider) LoadHistory(context.Context, string) ([]ai.Message, error) {
	return nil, nil
}

func taskWithSession(sessionID string) sqlc.AgentTask {
	t := sqlc.AgentTask{ID: "task-1"}
	if sessionID != "" {
		t.SessionID = sql.NullString{String: sessionID, Valid: true}
	}
	return t
}

func TestSessionResolver_SessionOwnerWins(t *testing.T) {
	prov := &fakeSessionProvider{info: memory.SessionInfo{AgentID: "owner-agent"}}
	r := sessionAndCreatorResolver(nil, prov, slog.Default())
	got, ok := r(context.Background(), taskWithSession("sess-1"))
	if !ok || got != "owner-agent" {
		t.Fatalf("got (%q,%v), want (owner-agent,true)", got, ok)
	}
	if prov.calls != 1 {
		t.Errorf("LoadInfo calls=%d want 1", prov.calls)
	}
}

func TestSessionResolver_NoSessionFalls(t *testing.T) {
	prov := &fakeSessionProvider{info: memory.SessionInfo{AgentID: "owner-agent"}}
	r := sessionAndCreatorResolver(nil, prov, slog.Default())
	if _, ok := r(context.Background(), taskWithSession("")); ok {
		t.Fatal("resolver should not resolve a task with no session_id")
	}
	if prov.calls != 0 {
		t.Errorf("LoadInfo should not be called without a session; calls=%d", prov.calls)
	}
}

func TestSessionResolver_LoadErrorFalls(t *testing.T) {
	prov := &fakeSessionProvider{loadErr: errors.New("boom")}
	r := sessionAndCreatorResolver(nil, prov, slog.Default())
	if _, ok := r(context.Background(), taskWithSession("sess-1")); ok {
		t.Fatal("resolver should fall back when LoadInfo errors")
	}
}

func TestSessionResolver_EmptyOwnerFalls(t *testing.T) {
	prov := &fakeSessionProvider{info: memory.SessionInfo{AgentID: ""}}
	r := sessionAndCreatorResolver(nil, prov, slog.Default())
	if _, ok := r(context.Background(), taskWithSession("sess-1")); ok {
		t.Fatal("resolver should fall back when session has no owning agent")
	}
}
