package agent

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
)

// compactSpy wraps the fake memory provider and counts Compact invocations so a
// test can assert whether the compactor was reached.
type compactSpy struct {
	*memorytest.Fake
	calls int
}

func (s *compactSpy) Compact(context.Context, memory.Session, memory.CompactionMode) (*memory.CompactionResult, error) {
	s.calls++
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

	_, err := svc.CompactSession(context.Background(), group)
	if !errors.Is(err, ErrGroupCompactionUnsupported) {
		t.Fatalf("group CompactSession err = %v, want ErrGroupCompactionUnsupported", err)
	}
	if spy.calls != 0 {
		t.Fatalf("compactor invoked %d times for a group session; want 0", spy.calls)
	}

	private := session.Info{ID: "s-priv", AgentID: "a", UserID: "user-1", Kind: string(session.KindChat)}
	if _, err := svc.CompactSession(context.Background(), private); err != nil {
		t.Fatalf("private CompactSession: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("compactor calls = %d for a private session; want 1", spy.calls)
	}
}
