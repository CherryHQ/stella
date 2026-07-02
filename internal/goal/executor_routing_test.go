package goal

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// TestExecutorWaitsForOneShotSessionClose pins the one-shot session contract:
// after goal_control records a terminal action, Execute waits for the session's
// pre-close sandbox callback to run before returning.
func TestExecutorWaitsForOneShotSessionClose(t *testing.T) {
	eventConsumed := make(chan struct{})
	releaseClose := make(chan struct{})
	callbackCalled := make(chan struct{})
	returned := make(chan error, 1)

	chat := func(ctx context.Context, p TaskChatParams) <-chan agent.Event {
		ch := make(chan agent.Event)
		go func() {
			defer close(ch)
			if _, err := p.ExtraTools[0].Execute(ctx, map[string]any{"action": "submit", "summary": "done"}); err != nil {
				ch <- agent.Event{Err: err}
				return
			}
			ch <- agent.Event{Text: "terminal recorded"}
			close(eventConsumed)
			<-releaseClose
			if p.OnSandboxSession != nil {
				if err := p.OnSandboxSession(sandbox.NopSession()); err != nil {
					ch <- agent.Event{Err: err}
					return
				}
			}
		}()
		return ch
	}

	ex := newWorkerExecutor(chat, nil)
	go func() {
		_, err := ex.Execute(context.Background(), ExecutorRequest{
			Attempt: sqlc.AgentGoalAttempt{
				Purpose:         PurposeExecution,
				UserID:          "u",
				SessionID:       "s",
				ExecutorAgentID: pgtype.Text{String: "a", Valid: true},
			},
			Input: AttemptInput{Intent: "do the thing"},
			OnSandboxSession: func(sandbox.Session) error {
				close(callbackCalled)
				return nil
			},
		})
		returned <- err
	}()

	select {
	case <-eventConsumed:
	case err := <-returned:
		t.Fatalf("Execute returned before terminal event was consumed: %v", err)
	case <-time.After(time.Second):
		t.Fatal("terminal event was not consumed")
	}

	select {
	case err := <-returned:
		t.Fatalf("Execute returned before one-shot session close: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseClose)
	select {
	case <-callbackCalled:
	case err := <-returned:
		t.Fatalf("Execute returned before sandbox callback: %v", err)
	case <-time.After(time.Second):
		t.Fatal("sandbox callback was not called")
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Execute did not return after one-shot session close")
	}
}

// TestExecutorRoutesDecomposeFlag pins the executor->chat contract the
// session-kind routing depends on: a purpose=decomposition attempt MUST set
// TaskChatParams.Decompose so stellad runs the turn on the KindDelegate planning
// session, while an execution attempt MUST leave it false (KindTask worker
// session). Dropping the flag does not crash — every decomposition turn fails
// with "session kind mismatch: got delegate, want task", silently draining the
// composite's decomposition budget until it blocks(budget_exhausted) with no
// plan. This guard makes that regression loud.
func TestExecutorRoutesDecomposeFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		purpose string
		action  string
		want    bool
	}{
		{"decomposition", PurposeDecomposition, "decompose", true},
		{"execution", PurposeExecution, "submit", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got bool
			chat := func(_ context.Context, p TaskChatParams) <-chan agent.Event {
				got = p.Decompose
				// Drive a terminal action through the injected goal_control tool so
				// the attempt resolves without a real agent runtime.
				args := map[string]any{"action": tc.action}
				if tc.action == "decompose" {
					// A minimal plan that clears in-turn ValidateDecomposition (>=1
					// required child); this test only pins the Decompose routing flag.
					args["decomposition"] = map[string]any{
						"children": []any{map[string]any{
							"key": "c1", "title": "t", "kind": "leaf", "required": true,
						}},
						"edges": []any{},
					}
				}
				if _, err := p.ExtraTools[0].Execute(context.Background(), args); err != nil {
					t.Errorf("control tool execute: %v", err)
				}
				ch := make(chan agent.Event)
				close(ch)
				return ch
			}

			ex := newWorkerExecutor(chat, nil)
			if _, err := ex.Execute(context.Background(), ExecutorRequest{
				Attempt: sqlc.AgentGoalAttempt{
					Purpose:         tc.purpose,
					UserID:          "u",
					SessionID:       "s",
					ExecutorAgentID: pgtype.Text{String: "a", Valid: true},
				},
				Input: AttemptInput{Intent: "do the thing"},
			}); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Decompose = %v, want %v", got, tc.want)
			}
		})
	}
}
