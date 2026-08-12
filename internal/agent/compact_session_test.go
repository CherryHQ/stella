package agent

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

// compactSpy wraps the fake memory provider and counts Compact invocations so a
// test can assert whether the compactor was reached.
type compactSpy struct {
	*memorytest.Fake
	calls    int
	contexts []context.Context
	sessions []memory.Session
}

func (s *compactSpy) Compact(ctx context.Context, session memory.Session, _ memory.CompactionMode) (*memory.CompactionResult, error) {
	s.calls++
	s.contexts = append(s.contexts, ctx)
	s.sessions = append(s.sessions, session)
	return &memory.CompactionResult{}, nil
}

func newCompactService(t *testing.T, mem memory.Provider) *Service {
	t.Helper()
	rt, err := agentruntime.New(agentruntime.Config{
		Memory:    mem,
		NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	return &Service{Runtime: rt}
}

// TestCompactSession_GroupRejectedBeforeCompactor proves a group session is
// rejected with ErrGroupCompactionUnsupported and never reaches the compactor;
// a private session is the control that proves the spy would have recorded a call.
func TestCompactSession_GroupRejectedBeforeCompactor(t *testing.T) {
	spy := &compactSpy{Fake: memorytest.New()}
	svc := newCompactService(t, spy)

	const groupID = "11111111-1111-4111-8111-111111111111"
	group := session.Info{ID: "a:group:" + groupID, AgentID: "a", UserID: groupID, GroupID: groupID, Kind: string(session.KindChat)}

	_, err := svc.CompactAuthorizedSession(context.Background(), group)
	if !errors.Is(err, ErrGroupCompactionUnsupported) {
		t.Fatalf("group CompactSession err = %v, want ErrGroupCompactionUnsupported", err)
	}
	if spy.calls != 0 {
		t.Fatalf("compactor invoked %d times for a group session; want 0", spy.calls)
	}

	private := session.Info{ID: "s-priv", AgentID: "a", UserID: "user-1", Kind: string(session.KindChat)}
	if _, err := svc.CompactAuthorizedSession(context.Background(), private); err != nil {
		t.Fatalf("private CompactSession: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("compactor calls = %d for a private session; want 1", spy.calls)
	}

	const guestID = "22222222-2222-4222-8222-222222222222"
	guest := session.Info{ID: "a:guest:" + guestID, AgentID: "a", UserID: guestID, GuestID: guestID, Kind: string(session.KindChat)}
	if _, err := svc.CompactAuthorizedSession(context.Background(), guest); err != nil {
		t.Fatalf("guest CompactSession: %v", err)
	}
	if spy.calls != 2 {
		t.Fatalf("compactor calls = %d after guest session; want 2", spy.calls)
	}
	if got := authz.GuestIDFromContext(spy.contexts[1]); got != guestID {
		t.Fatalf("guest compaction context GuestID = %q, want %q", got, guestID)
	}
	if got := authz.AgentIDFromContext(spy.contexts[1]); got != guest.AgentID {
		t.Fatalf("guest compaction context AgentID = %q, want %q", got, guest.AgentID)
	}
	if got := spy.sessions[1].GuestID; got != guestID {
		t.Fatalf("guest compaction session GuestID = %q, want %q", got, guestID)
	}
}
