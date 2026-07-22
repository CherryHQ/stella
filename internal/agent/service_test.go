package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/pkg/ai"
)

// --- fake runner for service tests ------------------------------------------

type fakeRunnerSvc struct {
	events []agentruntime.Event
}

type fakeSessionAccessSvc struct{ reg *session.Registry }

type fakeSessionAccess struct{ reg *session.Registry }

func (s fakeSessionAccessSvc) Begin(context.Context, authz.Authority) (agent.SessionAccess, error) {
	return fakeSessionAccess(s), nil
}

func (a fakeSessionAccess) Create(ctx context.Context, userID, agentID, projectID string, kind session.Kind, channel session.Channel) (session.Info, error) {
	return a.reg.Ensure(ctx, session.Request{UserID: userID, AgentID: agentID, ProjectID: projectID, Kind: kind, Channel: channel, CreateIfMissing: true})
}

func (a fakeSessionAccess) ResolveMain(ctx context.Context, userID, agentID string) (session.Info, error) {
	return a.reg.ResolveMain(ctx, session.MainRequest{UserID: userID, AgentID: agentID})
}

func (a fakeSessionAccess) RotateMain(ctx context.Context, userID, agentID, expectedSessionID string) (session.Info, error) {
	return a.reg.RotateMain(ctx, session.MainRequest{UserID: userID, AgentID: agentID, ExpectedSessionID: expectedSessionID})
}

func (a fakeSessionAccess) ResolveChatChannel(ctx context.Context, req session.ChannelRequest) (session.Info, error) {
	return a.reg.ResolveChatChannel(ctx, req)
}

func (a fakeSessionAccess) RotateChannel(ctx context.Context, req session.ChannelRequest) (session.Info, error) {
	return a.reg.RotateChannel(ctx, req)
}

func (a fakeSessionAccess) Use(ctx context.Context, agentID, sessionID string) (session.Info, error) {
	return a.reg.Get(ctx, session.Scope{AgentID: agentID, System: true}, sessionID)
}

func (a fakeSessionAccess) EnsureRead(ctx context.Context, req session.Request) (session.Info, error) {
	return a.reg.Ensure(ctx, req)
}

func (a fakeSessionAccess) EnsureUse(ctx context.Context, req session.Request) (session.Info, error) {
	return a.reg.Ensure(ctx, req)
}

func (a fakeSessionAccess) Delete(ctx context.Context, agentID, sessionID string) (session.Info, error) {
	return a.reg.Get(ctx, session.Scope{AgentID: agentID}, sessionID)
}

func (a fakeSessionAccess) Archive(ctx context.Context, info session.Info) error {
	return a.reg.Archive(ctx, session.Scope{UserID: info.UserID, AgentID: info.AgentID}, info.ID)
}

func (r *fakeRunnerSvc) Chat(_ context.Context, _ []ai.Message, _ agentruntime.MessageContent) <-chan agentruntime.Event {
	ch := make(chan agentruntime.Event, len(r.events)+1)
	for _, e := range r.events {
		ch <- e
	}
	close(ch)
	return ch
}
func (r *fakeRunnerSvc) Alive() bool             { return true }
func (r *fakeRunnerSvc) Busy() bool              { return false }
func (r *fakeRunnerSvc) LastActivity() time.Time { return time.Now() }
func (r *fakeRunnerSvc) SystemPrompt() string    { return "" }
func (r *fakeRunnerSvc) Close() error            { return nil }

// newTestService builds a Service backed by memorytest.Fake and a fake runner.
func newTestService(t *testing.T, events []agentruntime.Event) (*agent.Service, *memorytest.Fake) {
	t.Helper()

	mem := memorytest.New()
	factory := func(_ context.Context, _ agentruntime.RunnerParams) (agentruntime.Runner, error) {
		return &fakeRunnerSvc{events: events}, nil
	}
	rt, err := agentruntime.New(agentruntime.Config{
		NewRunner: factory,
		Memory:    mem,
	})
	if err != nil {
		t.Fatalf("agentruntime.New: %v", err)
	}

	reg, err := session.NewRegistry(mem, "agent1")
	if err != nil {
		t.Fatalf("session.NewRegistry: %v", err)
	}

	svc := &agent.Service{
		Sessions:      reg,
		Runtime:       rt,
		SessionAccess: fakeSessionAccessSvc{reg: reg},
		AgentID:       "agent1",
	}
	return svc, mem
}

// TestService_Chat_SessionEnsuredBeforeRuntime verifies that Chat resolves a
// session through the registry before dispatching to runtime.
func TestService_Chat_SessionEnsuredBeforeRuntime(t *testing.T) {
	svc, mem := newTestService(t, nil)

	stream := svc.Chat(context.Background(), agent.ChatRequest{
		UserID:  "u1",
		AgentID: "agent1",
		Channel: session.ChannelWeb,
		Message: "hello",
	})
	for range stream {
	}

	// A session must exist in the store.
	ctx := authz.WithUserID(context.Background(), "u1")
	ctx = authz.WithAgentID(ctx, "agent1")
	infos, err := mem.ListInfo(ctx, memory.ListOptions{UserID: "u1", AgentID: "agent1"})
	if err != nil {
		t.Fatalf("ListInfo: %v", err)
	}
	if len(infos) == 0 {
		t.Error("expected at least one session in store")
	}
}

// TestService_Chat_PropagatesEvents verifies events reach the caller.
func TestService_Chat_PropagatesEvents(t *testing.T) {
	events := []agentruntime.Event{{Text: "hello"}, {Text: " world"}}
	svc, _ := newTestService(t, events)

	stream := svc.Chat(context.Background(), agent.ChatRequest{
		UserID:  "u1",
		AgentID: "agent1",
		Message: "hi",
	})

	var got string
	for ev := range stream {
		got += ev.Text
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

// TestService_Chat_MissingUser returns an error event when no UserID is supplied.
func TestService_ChatAdmitted_ReportsPreTurnResolutionFailure(t *testing.T) {
	svc, _ := newTestService(t, nil)
	stream, err := svc.ChatAdmitted(context.Background(), agent.ChatRequest{AgentID: "agent1", Message: "hi"})
	if err == nil || stream != nil {
		t.Fatalf("admission = %v, %v; want nil stream and error", stream, err)
	}

	// The legacy API retains its stream-only error behavior for existing callers.
	for event := range svc.Chat(context.Background(), agent.ChatRequest{AgentID: "agent1", Message: "hi"}) {
		if event.Err != nil {
			return
		}
	}
	t.Fatal("Chat did not preserve its immediate error event")
}

func TestService_Chat_MissingUser(t *testing.T) {
	svc, _ := newTestService(t, nil)

	stream := svc.Chat(context.Background(), agent.ChatRequest{
		AgentID: "agent1",
		Message: "hi",
	})
	var gotErr error
	for ev := range stream {
		if ev.Err != nil {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Error("expected error event for missing UserID")
	}
}
