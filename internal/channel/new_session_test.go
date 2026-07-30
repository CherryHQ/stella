package channel

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// newRotateTestChat builds a DM chat pinned to the user's main session — the
// only shape `/new` rotates in this phase.
func newRotateTestChat(t *testing.T, user auth.User) *ResolvedChat {
	t.Helper()
	rc := newCompactTestChat(t, "", user)
	rc.Channel = session.Channel(rc.AgentID + ":user:" + user.ID + ":private")
	rc.SessionKey = agent.BuildUserSessionKey(rc.AgentID, user.ID, "private")
	return rc
}

func TestNewSessionReplyRotatesMainSession(t *testing.T) {
	ctx := context.Background()
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})

	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}
	if reply := NewSessionReply(ctx, rc, before.ID); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("reply = %q, want %q", reply, pkgchannel.NewSessionStartedMessage)
	}

	after, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if after.ID == before.ID {
		t.Fatal("/new must move the chat onto a new session")
	}
	if after.Archived {
		t.Fatal("the successor must be active")
	}
}

// TestNewSessionReplyDuplicateIsNoOp covers two `/new` commands racing on one
// chat: the second names a session the first already rotated away, so it must
// report the reset as already done instead of resetting again.
func TestNewSessionReplyDuplicateIsNoOp(t *testing.T) {
	ctx := context.Background()
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})

	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}
	if reply := NewSessionReply(ctx, rc, before.ID); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("first reply = %q", reply)
	}
	first, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}

	if reply := NewSessionReply(ctx, rc, before.ID); reply != pkgchannel.SessionAlreadyResetMessage {
		t.Fatalf("duplicate reply = %q, want %q", reply, pkgchannel.SessionAlreadyResetMessage)
	}
	second, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession after duplicate: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("a duplicate /new must not rotate the session a second time")
	}
}

// TestNewSessionReplyRotatesGroupSession proves a group chat rotates like a DM
// now that its session is resolved by binding rather than pinned to its key.
func TestNewSessionReplyRotatesGroupSession(t *testing.T) {
	const groupID = "11111111-1111-4111-8111-111111111111"
	ctx := context.Background()
	rc := newCompactTestChat(t, groupID, auth.User{})

	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}
	if before.GroupID != groupID {
		t.Fatalf("resolved session GroupID = %q, want the group", before.GroupID)
	}
	if reply := NewSessionReply(ctx, rc, before.ID); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("group reply = %q, want %q", reply, pkgchannel.NewSessionStartedMessage)
	}

	after, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if after.ID == before.ID {
		t.Fatal("group /new must move the chat onto a new session")
	}
	if after.GroupID != groupID || after.Archived {
		t.Fatalf("successor = %+v, want an active session owned by the group", after)
	}

	// A duplicate /new names a session that is already archived.
	if reply := NewSessionReply(ctx, rc, before.ID); reply != pkgchannel.SessionAlreadyResetMessage {
		t.Fatalf("duplicate group reply = %q, want %q", reply, pkgchannel.SessionAlreadyResetMessage)
	}
}

// TestNewSessionReplyRotatesPrivateChannelSession covers the non-main private
// chat channel (a chat whose key is not a linked user's private channel).
func TestNewSessionReplyRotatesPrivateChannelSession(t *testing.T) {
	ctx := context.Background()
	rc := newCompactTestChat(t, "", auth.User{ID: "user-1", Role: auth.RoleUser})

	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}
	if reply := NewSessionReply(ctx, rc, before.ID); reply != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("reply = %q, want %q", reply, pkgchannel.NewSessionStartedMessage)
	}
	after, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if after.ID == before.ID {
		t.Fatal("/new must move the private channel chat onto a new session")
	}
	if after.Channel != string(rc.Channel) {
		t.Fatalf("successor channel = %q, want the chat binding %q", after.Channel, rc.Channel)
	}
}

// TestHandleNewSessionCommandWaitsForActiveTurn proves `/new` is queued rather
// than immediate: rotating underneath an in-flight turn would land its reply in
// a session the user has already left.
func TestHandleNewSessionCommandWaitsForActiveTurn(t *testing.T) {
	ctx := context.Background()
	c := &Coordinator{queue: newSessionQueue()}
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})

	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}

	turnRunning := make(chan struct{})
	turnUnblock := make(chan struct{})
	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		stream, doneC, err := c.queue.Enqueue(ctx, rc.SessionKey, func(context.Context) (*pkgchannel.ChatStream, error) {
			close(turnRunning)
			<-turnUnblock
			return makeStream(pkgchannel.Event{Text: "answer"}), nil
		})
		if err != nil {
			t.Errorf("enqueue turn: %v", err)
			return
		}
		for range stream.Events {
		}
		close(doneC)
	}()
	<-turnRunning

	replyC := make(chan string, 1)
	go func() { replyC <- c.handleNewSessionCommand(ctx, rc) }()

	select {
	case reply := <-replyC:
		t.Fatalf("/new ran ahead of the in-flight turn (reply %q)", reply)
	case <-time.After(100 * time.Millisecond):
	}

	// The session must still be the one the running turn is writing to.
	current, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if current.ID != before.ID {
		t.Fatal("/new rotated the session while a turn was still running")
	}

	close(turnUnblock)
	<-turnDone

	select {
	case reply := <-replyC:
		if reply != pkgchannel.NewSessionStartedMessage {
			t.Fatalf("reply = %q, want %q", reply, pkgchannel.NewSessionStartedMessage)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/new did not run after the turn finished")
	}

	rotated, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession after rotation: %v", err)
	}
	if rotated.ID == before.ID {
		t.Fatal("/new must rotate once the turn completes")
	}
}
