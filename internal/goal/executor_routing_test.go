package goal

import (
	"context"
	"strings"
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

	ex := newWorkerExecutor(chat, nil, nil, allowAttempt)
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
	case err := <-returned:
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Execute did not return after one-shot session close")
	}
	// The callback runs synchronously on the chat goroutine before the event
	// channel closes, so it happens-before Execute returns. Checking it after
	// the return (instead of racing both channels in one select, where a
	// ready-ready random pick misreports correct ordering as a violation)
	// keeps the assertion deterministic.
	select {
	case <-callbackCalled:
	default:
		t.Fatal("Execute returned before sandbox callback")
	}
}

func TestExecutorRunsSandboxCallbackOnlyForTerminalSubmitTurn(t *testing.T) {
	turns := 0
	chat := func(_ context.Context, p TaskChatParams) <-chan agent.Event {
		turns++
		ch := make(chan agent.Event, 1)
		if turns == 1 {
			ch <- agent.Event{Text: "I did work but forgot the tool"}
		} else {
			if _, err := p.ExtraTools[0].Execute(context.Background(), map[string]any{"action": "submit", "summary": "done"}); err != nil {
				ch <- agent.Event{Err: err}
			}
		}
		if p.OnSandboxSession != nil {
			if err := p.OnSandboxSession(sandbox.NopSession()); err != nil {
				ch <- agent.Event{Err: err}
			}
		}
		close(ch)
		return ch
	}

	callbacks := 0
	ex := newWorkerExecutor(chat, nil, nil, allowAttempt)
	if _, err := ex.Execute(context.Background(), ExecutorRequest{
		Attempt: sqlc.AgentGoalAttempt{
			Purpose:         PurposeExecution,
			UserID:          "u",
			SessionID:       "s",
			ExecutorAgentID: pgtype.Text{String: "a", Valid: true},
		},
		Input: AttemptInput{Intent: "do the thing"},
		OnSandboxSession: func(sandbox.Session) error {
			callbacks++
			return nil
		},
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if turns != 2 || callbacks != 1 {
		t.Fatalf("turns=%d callbacks=%d, want two turns and callback only on terminal submit", turns, callbacks)
	}
}

func TestBuildAttemptPromptRendersHumanTimelineGuidance(t *testing.T) {
	prompt := buildAttemptPrompt(ExecutorRequest{Input: AttemptInput{
		Intent: "fetch the invalid domain",
		TimelineContext: []TimelineContextEvent{
			{EventType: GoalEventAttemptFinished, AttemptID: "old", Status: AttemptFailed, FailureClass: FailureClassEnvironment, Reason: "invalid domain failed", CreatedAt: "2026-07-03T00:00:00Z"},
			{EventType: GoalEventHumanMessage, Text: "换用 https://example.com", CreatedAt: "2026-07-03T00:01:00Z"},
		},
	}}, false)

	if !strings.Contains(prompt, "Human guidance for this attempt") {
		t.Fatalf("prompt missing human guidance heading:\n%s", prompt)
	}
	if !strings.Contains(prompt, "where it conflicts with the original framing above, the human guidance wins") {
		t.Fatalf("prompt missing conflict precedence instruction:\n%s", prompt)
	}
	if !strings.Contains(prompt, "换用 https://example.com") {
		t.Fatalf("prompt missing human message text:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Recent execution facts to account for") || !strings.Contains(prompt, "invalid domain failed") {
		t.Fatalf("prompt missing recent failure facts:\n%s", prompt)
	}
}

func TestBuildAttemptPromptOmitsHumanTimelineGuidanceWhenEmpty(t *testing.T) {
	prompt := buildAttemptPrompt(ExecutorRequest{Input: AttemptInput{Intent: "do the thing"}}, false)
	if strings.Contains(prompt, "Human guidance for this attempt") {
		t.Fatalf("prompt unexpectedly rendered human guidance:\n%s", prompt)
	}
}

func TestBuildDecompositionPromptRendersHumanTimelineGuidance(t *testing.T) {
	prompt := buildAttemptPrompt(ExecutorRequest{Input: AttemptInput{
		Intent: "split the work",
		TimelineContext: []TimelineContextEvent{
			{EventType: GoalEventHumanMessage, Text: "重新规划，先验证 example.com", CreatedAt: "2026-07-03T00:02:00Z"},
		},
	}}, true)
	if !strings.Contains(prompt, "Human guidance for this attempt") || !strings.Contains(prompt, "重新规划，先验证 example.com") {
		t.Fatalf("decomposition prompt missing human guidance:\n%s", prompt)
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

			ex := newWorkerExecutor(chat, nil, nil, allowAttempt)
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
