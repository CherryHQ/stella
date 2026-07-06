package goal

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestExecutionAttemptsMintDedicatedSessions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	const n = 5
	goals := make([]sqlc.AgentGoal, 0, n)
	for range n {
		g := h.createRoot(KindLeaf, AcceptanceContract{})
		h.activate(g.ID)
		goals = append(goals, g)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for _, g := range goals {
		wg.Go(func() {
			att, err := h.svc.Claim(ctx, g.ID, "w-1", nil)
			if err != nil {
				errCh <- err
				return
			}
			if err := h.worker.Run(ctx, g.ID, att.ID, Actor{Type: ActorWorker}); err != nil {
				errCh <- err
			}
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("run attempt: %v", err)
		}
	}

	seen := map[string]string{}
	for _, g := range goals {
		attempts, err := h.q.ListAttemptByGoal(ctx, sqlc.ListAttemptByGoalParams{GoalID: g.ID, Purpose: pgnull.Text(PurposeExecution)})
		if err != nil {
			t.Fatalf("ListAttemptByGoal: %v", err)
		}
		if len(attempts) != 1 {
			t.Fatalf("goal %s attempts=%d want 1", g.ID, len(attempts))
		}
		sid := attempts[0].SessionID
		if sid == "" {
			t.Fatalf("attempt session is empty")
		}
		if prev, ok := seen[sid]; ok {
			t.Fatalf("session %q reused by attempts %s and %s", sid, prev, attempts[0].ID)
		}
		seen[sid] = attempts[0].ID
	}
}

func TestWorkerCancelLeavesAttemptReplayableByLeaseReap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root := h.createRoot(KindLeaf, AcceptanceContract{})
	h.activate(root.ID)
	att, err := h.svc.Claim(ctx, root.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	started := make(chan struct{})
	h.worker.exec = blockingExecutor{started: started}
	runCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		result <- h.worker.Run(runCtx, root.ID, att.ID, Actor{Type: ActorWorker})
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err=%v want context.Canceled", err)
	}

	reloaded, err := h.q.GetAttempt(ctx, att.ID)
	if err != nil {
		t.Fatalf("GetAttempt: %v", err)
	}
	if reloaded.Status != AttemptRunning {
		t.Fatalf("cancelled attempt status=%q want running for lease reap", reloaded.Status)
	}
	if err := h.svc.ReapAttempt(ctx, att.ID); err != nil {
		t.Fatalf("ReapAttempt: %v", err)
	}
	if got := h.get(root.ID); got.Lifecycle != LifecyclePending {
		t.Fatalf("after reap lifecycle=%q want ready", got.Lifecycle)
	}
	next, err := h.svc.Claim(ctx, root.ID, "w-1", nil)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if next.ID == att.ID || next.SessionID == att.SessionID {
		t.Fatalf("replay attempt id/session = %s/%s, previous %s/%s", next.ID, next.SessionID, att.ID, att.SessionID)
	}
}

type blockingExecutor struct {
	started chan<- struct{}
}

func (e blockingExecutor) Execute(ctx context.Context, _ ExecutorRequest) (ExecutorResult, error) {
	close(e.started)
	<-ctx.Done()
	return ExecutorResult{}, ctx.Err()
}
