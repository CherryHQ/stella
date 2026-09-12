// internal/agent/result_history_test.go
package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	agentrun "github.com/CherryHQ/stella/internal/agent/run"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	sessioninbox "github.com/CherryHQ/stella/internal/agent/session/inbox"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/sessionexecution"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	rhUserID  = "u-result-history"
	rhAgentID = "result-history-agent"
)

// resultHistoryEnv wires the whole durable chain over real PostgreSQL:
// run.Worker -> executor -> agent.Service -> runtime on lcm canonical memory,
// the durable session inbox, and sessionexecution leases. Only the model
// runner is fake.
type resultHistoryEnv struct {
	db  *pgxpool.Pool
	mem *lcm.Provider
	reg *session.Registry
	svc *agent.Service
}

func newResultHistoryEnv(t *testing.T, newRunner agentruntime.NewRunnerFunc) *resultHistoryEnv {
	t.Helper()
	db := dbtest.New(t)
	if _, err := sqlc.New(db).CreateAgent(t.Context(), sqlc.CreateAgentParams{
		ID: rhAgentID, Name: "result-history", Scope: "system", Enabled: true,
		Workspace: t.TempDir(), Sandbox: []byte(`{}`),
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	mem, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("lcm.New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	reg, err := session.NewRegistry(mem, rhAgentID)
	if err != nil {
		t.Fatalf("session.NewRegistry: %v", err)
	}
	rt, err := agentruntime.New(agentruntime.Config{
		Memory:    mem,
		Execution: sessionexecution.New(db),
		NewRunner: newRunner,
	})
	if err != nil {
		t.Fatalf("agentruntime.New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return &resultHistoryEnv{
		db:  db,
		mem: mem,
		reg: reg,
		svc: &agent.Service{
			Sessions:      reg,
			Runtime:       rt,
			SessionAccess: fakeSessionAccessSvc{reg: reg},
			SessionInbox:  sessioninbox.New(db),
			AgentID:       rhAgentID,
		},
	}
}

func (e *resultHistoryEnv) scopedCtx() context.Context {
	return authz.WithAgentID(authz.WithUserID(context.Background(), rhUserID), rhAgentID)
}

func (e *resultHistoryEnv) createSession(t *testing.T, id string, kind session.Kind, channel session.Channel) session.Info {
	t.Helper()
	info, err := e.reg.Ensure(e.scopedCtx(), session.Request{
		ID: id, UserID: rhUserID, AgentID: rhAgentID,
		Kind: kind, Channel: channel,
		CreateIfMissing:    true,
		AllowExactIDCreate: true,
		RequireKind:        kind,
	})
	if err != nil {
		t.Fatalf("create session %s: %v", id, err)
	}
	return info
}

func (e *resultHistoryEnv) enqueueRun(t *testing.T, sessionID, requestKey, text string) sqlc.AgentRun {
	t.Helper()
	tx, err := e.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	r, _, err := agentrun.New(e.db).Enqueue(t.Context(), tx, agentrun.EnqueueParams{
		SessionID:  sessionID,
		AgentID:    rhAgentID,
		RequestKey: requestKey,
		Actor:      agentrun.Actor{V: 1, Kind: "user", UserID: rhUserID},
		Input:      agentrun.Input{V: 1, Text: text},
		// No reply channel: this turn's reply returns only through final
		// history, like a web/API run.
		ReplyAddress: agentrun.ReplyAddress{V: 1},
	})
	if err != nil {
		t.Fatalf("enqueue run: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *resultHistoryEnv) history(t *testing.T, sessionID string) []ai.Message {
	t.Helper()
	msgs, err := e.mem.LoadHistory(e.scopedCtx(), sessionID)
	if err != nil {
		t.Fatalf("load history %s: %v", sessionID, err)
	}
	return msgs
}

// serviceRunExecutor mirrors channel.runExecutor: identity is re-derived from
// the run row, the turn runs under the adopted lease, and the per-call
// final-history sink collects the terminal reply for the finish transaction.
type serviceRunExecutor struct {
	svc    *agent.Service
	result *agentrun.Result
}

func (e *serviceRunExecutor) Execute(ctx context.Context, r sqlc.AgentRun) (*agentrun.Result, error) {
	var actor agentrun.Actor
	var input agentrun.Input
	if err := json.Unmarshal(r.Actor, &actor); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(r.Input, &input); err != nil {
		return nil, err
	}
	res := &agentrun.Result{SessionID: r.SessionID}
	ctx = authz.WithAgentID(authz.WithUserID(ctx, actor.UserID), r.AgentID)
	var turnErr error
	for evt := range e.svc.Chat(ctx, agent.ChatRequest{
		SessionID: r.SessionID,
		UserID:    actor.UserID,
		AgentID:   r.AgentID,
		Message:   input.Text,
		RuntimeOpts: []agentruntime.Option{
			agentruntime.WithRunID(r.ID),
			agentruntime.WithFinalHistorySink(&res.History),
		},
	}) {
		if evt.Err != nil && turnErr == nil {
			turnErr = evt.Err
		}
	}
	if turnErr != nil {
		return nil, turnErr
	}
	e.result = res
	return res, nil
}

// delegateParentRunner is the parent's model stand-in: inside the parent turn
// it invokes the real DelegateTool -> Service.RunDelegateSession -> Delegate ->
// turnqueue -> child admission chain, then records whether the parent's
// execution lease survived the child's completion.
type delegateParentRunner struct {
	tool     *delegatetool.DelegateTool
	db       *pgxpool.Pool
	parentID string
	childID  string
	task     string

	delegateRaw   string
	delegateErr   error
	parentExecErr error // nil while the child ran = parent lease still held
	childExecErr  error // pgx.ErrNoRows = the child released its own lease
}

func (r *delegateParentRunner) Chat(ctx context.Context, _ []ai.Message, _ agentruntime.MessageContent) <-chan agentruntime.Event {
	out := make(chan agentruntime.Event, 1)
	go func() {
		defer close(out)
		raw, err := r.tool.Execute(ctx, map[string]any{
			"tasks": []any{map[string]any{"task": r.task, "session_id": r.childID}},
		})
		r.delegateRaw, r.delegateErr = raw, err
		q := sqlc.New(r.db)
		_, r.parentExecErr = q.GetSessionExecution(ctx, r.parentID)
		_, r.childExecErr = q.GetSessionExecution(ctx, r.childID)
		out <- agentruntime.Event{Text: "parent final"}
	}()
	return out
}

func (r *delegateParentRunner) Alive() bool             { return true }
func (r *delegateParentRunner) Busy() bool              { return false }
func (r *delegateParentRunner) LastActivity() time.Time { return time.Now() }
func (r *delegateParentRunner) SystemPrompt() string    { return "" }
func (r *delegateParentRunner) PluginContext() agentruntime.PluginContext {
	return agentruntime.PluginContext{}
}
func (r *delegateParentRunner) Close() error { return nil }

// The durable run executor is the only WithFinalHistorySink caller: its sink
// collects the parent's terminal reply for the finish transaction. A
// synchronous child admitted through Service.RunDelegateSession -> Delegate ->
// turnqueue -> child Runtime admission must neither see that sink nor finish
// the parent's execution lease — its intermediate and final rows are its own.
//
// Covered: real claim/adopt/finish of both leases, the durable inbox, lcm
// canonical appends, and the finish-tx history commit. Not covered: the
// durable ctx_session_event log (no EventSink here) and multi-replica
// takeover.
func TestDelegateChildKeepsParentLeaseAndFinalHistory(t *testing.T) {
	const (
		parentID  = "rh-parent"
		childID   = "rh-child"
		childTask = "child task"
	)
	var parentRunner *delegateParentRunner
	childRunner := &fakeRunnerSvc{events: []agentruntime.Event{
		{Store: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "child-call-1", Name: "search", Arguments: map[string]any{"q": "x"}},
		}}},
		{
			ToolUse: &agentruntime.ToolUseEvent{ID: "child-call-1", Tool: "search", Status: "done"},
			Store: ai.ToolResultMessage{
				ToolCallID: "child-call-1", ToolName: "search",
				Content: []ai.ContentBlock{ai.TextContent{Text: "child tool output"}},
			},
		},
		{Text: "child final"},
	}}
	env := newResultHistoryEnv(t, func(_ context.Context, p agentruntime.RunnerParams) (agentruntime.Runner, error) {
		switch p.SessionID {
		case parentID:
			return parentRunner, nil
		case childID:
			return childRunner, nil
		default:
			return nil, fmt.Errorf("unexpected runner session %q", p.SessionID)
		}
	})
	env.createSession(t, parentID, session.KindChat, session.ChannelWeb)
	env.createSession(t, childID, session.KindDelegate, session.ChannelDelegate)
	parentRunner = &delegateParentRunner{
		tool:     delegatetool.NewDelegateTool(delegatetool.DelegateConfig{SessionRunner: env.svc}),
		db:       env.db,
		parentID: parentID,
		childID:  childID,
		task:     childTask,
	}
	r := env.enqueueRun(t, parentID, "rh-request-1", "parent input")

	exec := &serviceRunExecutor{svc: env.svc}
	w := agentrun.NewWorker(env.db, "rh-worker-1", exec, nil,
		agentrun.WithTurnAppender(func(ctx context.Context, tx pgx.Tx, sess memory.Session, msgs []ai.Message) error {
			return env.mem.AppendSessionTurn(ctx, sqlc.New(tx), sess, msgs...)
		}))
	claimed, err := w.ProcessOnce(t.Context())
	if err != nil || !claimed {
		t.Fatalf("process once: claimed=%v err=%v", claimed, err)
	}

	if parentRunner.delegateErr != nil {
		t.Fatalf("delegate tool: %v", parentRunner.delegateErr)
	}
	var envelope struct {
		Results map[string]struct {
			Output    string `json:"output"`
			SessionID string `json:"session_id"`
			Error     string `json:"error"`
			Complete  bool   `json:"complete"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(parentRunner.delegateRaw), &envelope); err != nil {
		t.Fatalf("delegate output: %v", err)
	}
	childResult := envelope.Results["task_0"]
	if childResult.Error != "" || !childResult.Complete ||
		childResult.SessionID != childID || !strings.Contains(childResult.Output, "child final") {
		t.Fatalf("child result = %+v", childResult)
	}

	// While the child turn ran it finished only its own lease; the parent's
	// stayed held until the worker's finish transaction committed.
	if parentRunner.parentExecErr != nil {
		t.Fatalf("parent lease lost during child turn: %v", parentRunner.parentExecErr)
	}
	if !errors.Is(parentRunner.childExecErr, pgx.ErrNoRows) {
		t.Fatalf("child lease after child finish = %v, want released", parentRunner.childExecErr)
	}
	if _, err := sqlc.New(env.db).GetSessionExecution(t.Context(), parentID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("parent lease after worker finish = %v, want released exactly once", err)
	}

	// The parent's final reply went only to the sink, then the finish
	// transaction — never mixed with the child's transcript.
	if exec.result == nil || len(exec.result.History) != 1 {
		t.Fatalf("final history sink = %+v, want exactly the parent reply", exec.result)
	}
	if memory.MessageRole(exec.result.History[0]) != "assistant" ||
		memory.MessageText(exec.result.History[0]) != "parent final" {
		t.Fatalf("final history sink[0] = %#v", exec.result.History[0])
	}

	parentHistory := env.history(t, parentID)
	if len(parentHistory) != 2 {
		t.Fatalf("parent history = %#v, want user+final only", parentHistory)
	}
	if memory.MessageRole(parentHistory[0]) != "user" || memory.MessageText(parentHistory[0]) != "parent input" {
		t.Fatalf("parent history[0] = %#v", parentHistory[0])
	}
	if memory.MessageRole(parentHistory[1]) != "assistant" || memory.MessageText(parentHistory[1]) != "parent final" {
		t.Fatalf("parent history[1] = %#v", parentHistory[1])
	}

	childHistory := env.history(t, childID)
	if len(childHistory) != 4 {
		t.Fatalf("child history = %#v, want user+toolcall+toolresult+final", childHistory)
	}
	if memory.MessageRole(childHistory[0]) != "user" {
		t.Fatalf("child history[0] role = %#v, want user", childHistory[0])
	}
	// Non-human input is wrapped in the stella_actor envelope: the task text
	// rides as content, and the actor carries the delegating agent + parent
	// session with information-only authority.
	var childInput struct {
		StellaActor struct {
			Type            string `json:"type"`
			ID              string `json:"id"`
			SourceSessionID string `json:"source_session_id"`
			Authority       string `json:"authority"`
		} `json:"stella_actor"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(memory.MessageText(childHistory[0])), &childInput); err != nil {
		t.Fatalf("child history[0] is not the actor envelope: %v", err)
	}
	if childInput.Content != childTask ||
		childInput.StellaActor.Type != "agent" || childInput.StellaActor.ID != rhAgentID ||
		childInput.StellaActor.SourceSessionID != parentID ||
		childInput.StellaActor.Authority != "information_only" {
		t.Fatalf("child input envelope = %#v", childInput)
	}
	call, ok := childHistory[1].(ai.AssistantMessage)
	if !ok || len(call.Content) != 1 {
		t.Fatalf("child history[1] = %#v, want assistant tool call", childHistory[1])
	}
	if tc, ok := call.Content[0].(ai.ToolCall); !ok || tc.ID != "child-call-1" || tc.Name != "search" {
		t.Fatalf("child tool call block = %#v", call.Content[0])
	}
	tr, ok := childHistory[2].(ai.ToolResultMessage)
	if !ok || tr.ToolCallID != "child-call-1" || memory.MessageText(tr) != "child tool output" {
		t.Fatalf("child history[2] = %#v", childHistory[2])
	}
	if memory.MessageRole(childHistory[3]) != "assistant" || memory.MessageText(childHistory[3]) != "child final" {
		t.Fatalf("child history[3] = %#v", childHistory[3])
	}

	// The child's user input arrived through the durable session inbox and was
	// claimed into its transcript by AppendInboxInput.
	var delivered bool
	if err := env.db.QueryRow(t.Context(),
		`SELECT delivered_at IS NOT NULL FROM ctx_session_inbox WHERE target_session_id = $1`, childID,
	).Scan(&delivered); err != nil || !delivered {
		t.Fatalf("child inbox delivered=%v err=%v", delivered, err)
	}

	got, err := agentrun.New(env.db).Get(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "completed" {
		t.Fatalf("run state = %s, want completed", got.State)
	}
}

// A turn's intermediate assistant/tool rows commit as they arrive, before the
// final reply exists. When the finish transaction fails at the history gate,
// those rows must survive while the buffered final reply — collected in the
// sink but never committed — leaves no trace.
func TestIntermediateToolRowsOutliveFinalHistoryRollback(t *testing.T) {
	const sessionID = "rh-failed-finish"
	runner := &fakeRunnerSvc{events: []agentruntime.Event{
		{Store: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ToolCall{ID: "call-1", Name: "search", Arguments: map[string]any{"q": "x"}},
		}}},
		{
			ToolUse: &agentruntime.ToolUseEvent{ID: "call-1", Tool: "search", Status: "done"},
			Store: ai.ToolResultMessage{
				ToolCallID: "call-1", ToolName: "search",
				Content: []ai.ContentBlock{ai.TextContent{Text: "tool ok"}},
			},
		},
		{Text: "final answer"},
	}}
	env := newResultHistoryEnv(t, func(context.Context, agentruntime.RunnerParams) (agentruntime.Runner, error) {
		return runner, nil
	})
	env.createSession(t, sessionID, session.KindChat, session.ChannelWeb)
	r := env.enqueueRun(t, sessionID, "rh-request-fail", "work input")

	exec := &serviceRunExecutor{svc: env.svc}
	w := agentrun.NewWorker(env.db, "rh-worker-2", exec, nil,
		agentrun.WithTurnAppender(func(context.Context, pgx.Tx, memory.Session, []ai.Message) error {
			return errors.New("finish gate: history append failed")
		}))
	claimed, err := w.ProcessOnce(t.Context())
	if !claimed || err == nil || !strings.Contains(err.Error(), "finish gate") {
		t.Fatalf("process once = claimed %v, err %v; want the injected finish failure", claimed, err)
	}

	// The sink collected the terminal reply — the stream ended cleanly — but
	// the failed finish transaction must not have committed it.
	if exec.result == nil || len(exec.result.History) != 1 ||
		memory.MessageText(exec.result.History[0]) != "final answer" {
		t.Fatalf("final history sink = %+v, want the uncommitted reply", exec.result)
	}
	history := env.history(t, sessionID)
	if len(history) != 3 {
		t.Fatalf("history = %#v, want user+toolcall+toolresult only", history)
	}
	if memory.MessageRole(history[0]) != "user" || memory.MessageText(history[0]) != "work input" {
		t.Fatalf("history[0] = %#v", history[0])
	}
	call, ok := history[1].(ai.AssistantMessage)
	if !ok || len(call.Content) != 1 {
		t.Fatalf("history[1] = %#v, want assistant tool call", history[1])
	}
	if _, ok := call.Content[0].(ai.ToolCall); !ok {
		t.Fatalf("history[1] block = %#v, want ai.ToolCall", call.Content[0])
	}
	tr, ok := history[2].(ai.ToolResultMessage)
	if !ok || tr.ToolCallID != "call-1" || memory.MessageText(tr) != "tool ok" {
		t.Fatalf("history[2] = %#v", history[2])
	}

	got, err := agentrun.New(env.db).Get(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "running" {
		t.Fatalf("run state = %s, want the claim to outlive the rolled-back finish", got.State)
	}
	if _, err := sqlc.New(env.db).GetSessionExecution(t.Context(), sessionID); err != nil {
		t.Fatalf("execution row after failed finish = %v, want still held", err)
	}
}
