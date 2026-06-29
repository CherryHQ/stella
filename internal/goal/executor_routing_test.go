package goal

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

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
