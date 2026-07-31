package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/agentctx"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
)

// ctxCapturingRunner records the context a turn actually reached the model loop
// with, which is where tools read the chat binding from.
type ctxCapturingRunner struct{ turnCtx context.Context } //nolint:containedctx // the captured value is the assertion

func (r *ctxCapturingRunner) Chat(ctx context.Context, _ []ai.Message, _ agentruntime.MessageContent) <-chan agentruntime.Event {
	r.turnCtx = ctx
	ch := make(chan agentruntime.Event, 1)
	ch <- agentruntime.Event{Text: "done"}
	close(ch)
	return ch
}

func (r *ctxCapturingRunner) Alive() bool             { return true }
func (r *ctxCapturingRunner) Busy() bool              { return false }
func (r *ctxCapturingRunner) LastActivity() time.Time { return time.Now() }
func (r *ctxCapturingRunner) SystemPrompt() string    { return "" }
func (r *ctxCapturingRunner) Close() error            { return nil }

// TestService_DelegateDropsParentChatBinding pins the second half of the
// chat -> delegate boundary. The delegate tool clears the binding when it builds
// the child context, but Delegate is the chokepoint every delegate turn passes,
// including ones a future caller wires up differently. A delegate runs in its
// own session, so carrying the parent chat's binding into it would let a
// nested run act on a conversation the delegate is not part of.
func TestService_DelegateDropsParentChatBinding(t *testing.T) {
	runner := &ctxCapturingRunner{}
	mem := memorytest.New()
	rt, err := agentruntime.New(agentruntime.Config{
		Memory: mem,
		NewRunner: func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
			return runner, nil
		},
	})
	if err != nil {
		t.Fatalf("agentruntime.New: %v", err)
	}
	reg, err := session.NewRegistry(mem, "agent1")
	if err != nil {
		t.Fatalf("session.NewRegistry: %v", err)
	}
	svc := &agent.Service{Sessions: reg, Runtime: rt, SessionAccess: fakeSessionAccessSvc{reg: reg}, AgentID: "agent1"}

	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "u1"), "agent1")
	ctx = agentctx.WithChatBinding(ctx, agentctx.ChatBinding{Main: true, SessionKey: "agent1:main:u1"})

	if _, err := svc.Delegate(ctx, agent.DelegateRequest{UserID: "u1", AgentID: "agent1", Task: "work"}); err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if runner.turnCtx == nil {
		t.Fatal("delegate turn never reached the runner")
	}
	if binding, ok := agentctx.ChatBindingFromContext(runner.turnCtx); ok {
		t.Fatalf("delegate turn carried the parent chat binding %+v", binding)
	}
}
