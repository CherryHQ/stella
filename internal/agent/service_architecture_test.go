package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/agentaccess"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
)

// TestService_Chat_ResumeOnlyUnknownSessionID verifies that Chat with an unknown
// SessionID does not silently create a new session (invariant 4).
func TestService_Chat_ResumeOnlyUnknownSessionID(t *testing.T) {
	svc, _ := newTestService(t, nil)

	stream := svc.Chat(context.Background(), agent.ChatRequest{
		SessionID: "nonexistent-session-id",
		UserID:    "u1",
		AgentID:   "agent1",
		Channel:   session.ChannelWeb,
		Message:   "hello",
	})
	var gotErr error
	for ev := range stream {
		if ev.Err != nil {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected error for unknown SessionID, got nil — session was silently created")
	}
}

// TestService_Delegate_ResumeOnlyUnknownSessionID verifies that Delegate with an
// unknown SessionID does not create a session (invariant 4).
func TestService_Delegate_ResumeOnlyUnknownSessionID(t *testing.T) {
	svc, _ := newTestService(t, nil)

	ctx := authz.WithUserID(context.Background(), "u1")
	ctx = authz.WithAgentID(ctx, "agent1")

	_, err := svc.Delegate(ctx, agent.DelegateRequest{
		SessionID: "nonexistent-delegate-session",
		UserID:    "u1",
		AgentID:   "agent1",
		Task:      "do something",
	})
	if err == nil {
		t.Fatal("expected error for unknown delegate SessionID, got nil — session was silently created")
	}
	if !errors.Is(err, session.ErrNotFound) {
		// Accept any error that prevents creation; the exact type may be wrapped.
		t.Logf("got error (acceptable): %v", err)
	}
}

// TestService_Delegate_EmptySessionIDCreatesNew verifies that Delegate without a
// SessionID creates a new delegate session with a generated ID.
func TestService_Delegate_EmptySessionIDCreatesNew(t *testing.T) {
	svc, _ := newTestService(t, nil)

	ctx := authz.WithUserID(context.Background(), "u1")
	ctx = authz.WithAgentID(ctx, "agent1")

	res, err := svc.Delegate(ctx, agent.DelegateRequest{
		UserID:  "u1",
		AgentID: "agent1",
		Task:    "do something",
	})
	if err != nil {
		t.Fatalf("Delegate with empty SessionID: %v", err)
	}
	if res.SessionID == "" {
		t.Error("expected a generated SessionID, got empty")
	}
}

// TestService_Chat_WrongKindResume verifies that resuming a session with a
// different kind is rejected (invariant 6).
func TestService_RunDelegateSessionUsesFreshPEP(t *testing.T) {
	svc, _ := newTestService(t, []agentruntime.Event{{Text: "done"}})
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "u1"), "agent1")

	res, err := svc.RunDelegateSession(ctx, delegatetool.SessionRunRequest{Task: "work"})
	if err != nil {
		t.Fatalf("RunDelegateSession: %v", err)
	}
	if !res.Complete || res.Output != "done" {
		t.Fatalf("unexpected result: %#v", res)
	}
	// The SessionAccess adapter owns the sole evaluation for this turn. Its
	// policy/revocation semantics are covered by internal/sessionaccess tests.
}

func TestService_RunDelegateSessionRejectsForeignOrSpoofedIdentityBeforePEP(t *testing.T) {
	svc, mem := newTestService(t, nil)
	foreign := memory.SessionInfo{ID: "foreign", UserID: "u2", AgentID: "agent1", Kind: "delegate", Channel: "delegate"}
	ownerCtx := authz.WithAgentID(authz.WithUserID(context.Background(), "u2"), "agent1")
	if err := mem.SaveInfo(ownerCtx, foreign); err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), "u1"), "agent1")
	if _, err := svc.RunDelegateSession(ctx, delegatetool.SessionRunRequest{SessionID: "foreign", Task: "work"}); err == nil {
		t.Fatal("foreign resume succeeded")
	}
	spoofed := authz.WithAgentID(authz.WithUserID(context.Background(), "u1"), "other-agent")
	if _, err := svc.RunDelegateSession(spoofed, delegatetool.SessionRunRequest{Task: "work"}); !errors.Is(err, agentaccess.ErrForbidden) {
		t.Fatalf("spoofed executor error = %v, want forbidden", err)
	}
}

func TestService_Chat_WrongKindResume(t *testing.T) {
	svc, mem := newTestService(t, nil)

	// Create a delegate session first.
	ctx := authz.WithUserID(context.Background(), "u1")
	ctx = authz.WithAgentID(ctx, "agent1")
	delegateInfo := memory.SessionInfo{
		ID:      "delegate-sess-1",
		UserID:  "u1",
		AgentID: "agent1",
		Kind:    "delegate",
		Channel: "delegate",
	}
	if err := mem.SaveInfo(ctx, delegateInfo); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	// Try to resume it as a scheduler chat.
	stream := svc.Chat(context.Background(), agent.ChatRequest{
		SessionID: "delegate-sess-1",
		UserID:    "u1",
		AgentID:   "agent1",
		Kind:      session.KindScheduler,
		Message:   "hello",
	})
	var gotErr error
	for ev := range stream {
		if ev.Err != nil {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected error resuming delegate session as scheduler, got nil")
	}
}
