package channel

import (
	"context"
	"errors"
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

// inertClaim is the no-guard receipt for tests that exercise rotation itself;
// a zero-value chat receipt is inert by construction.
func inertClaim() commandClaim { return chatCommandReceipt{} }

func allowRotation(context.Context) error { return nil }

type trackingClaim struct {
	claimed  bool
	releases int
}

func (c *trackingClaim) claim(context.Context) (bool, error) { return c.claimed, nil }
func (c *trackingClaim) release(context.Context)             { c.releases++ }

func TestRotateChatSessionDeniedBeforeResolveReleasesClaim(t *testing.T) {
	ctx := context.Background()
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}
	claim := &trackingClaim{claimed: true}
	if reply := rotateChatSession(ctx, rc, claim, nil, func(context.Context) error { return errors.New("denied") }); reply == pkgchannel.NewSessionStartedMessage {
		t.Fatalf("reply = %q, want authorization failure", reply)
	}
	if claim.releases != 1 {
		t.Fatalf("receipt releases = %d, want 1", claim.releases)
	}
	after, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("denied rotation changed session: %q -> %q", before.ID, after.ID)
	}
}

func TestRotateChatSessionDeniedAtDequeueReleasesClaim(t *testing.T) {
	ctx := context.Background()
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}
	claim := &trackingClaim{claimed: true}
	authorizations := 0
	authorize := func(context.Context) error {
		authorizations++
		if authorizations == 2 {
			return errors.New("binding changed")
		}
		return nil
	}
	if reply := rotateChatSession(ctx, rc, claim, newSessionQueue(), authorize); reply == pkgchannel.NewSessionStartedMessage {
		t.Fatalf("reply = %q, want authorization failure", reply)
	}
	if authorizations != 2 {
		t.Fatalf("authorization calls = %d, want 2", authorizations)
	}
	if claim.releases != 1 {
		t.Fatalf("receipt releases = %d, want 1", claim.releases)
	}
	after, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("dequeue denial changed session: %q -> %q", before.ID, after.ID)
	}
}

func TestRotateChatSessionRotatesMainSession(t *testing.T) {
	ctx := context.Background()
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})

	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}
	if reply := rotateChatSession(ctx, rc, inertClaim(), nil, allowRotation); reply != pkgchannel.NewSessionStartedMessage {
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

// TestRotateSessionDuplicateIsStale covers two `/new` commands racing on one
// chat: the second names a session the first already rotated away, so the CAS
// must refuse instead of resetting the successor. rotateChatSession pins its
// expected session before entering the queue, which is what turns a queued
// duplicate into exactly this stale call.
func TestRotateSessionDuplicateIsStale(t *testing.T) {
	ctx := context.Background()
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})

	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}
	if _, err := rc.RotateSession(ctx, before.ID); err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	first, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}

	if _, err := rc.RotateSession(ctx, before.ID); !errors.Is(err, session.ErrStaleRotation) {
		t.Fatalf("duplicate rotation error = %v, want ErrStaleRotation", err)
	}
	second, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession after duplicate: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("a duplicate /new must not rotate the session a second time")
	}
}

// TestRotateChatSessionRotatesPrivateChannelSession covers the non-main private
// chat channel (a chat whose key is not a linked user's private channel).
func TestRotateChatSessionRotatesPrivateChannelSession(t *testing.T) {
	ctx := context.Background()
	rc := newCompactTestChat(t, "", auth.User{ID: "user-1", Role: auth.RoleUser})

	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}
	if reply := rotateChatSession(ctx, rc, inertClaim(), nil, allowRotation); reply != pkgchannel.NewSessionStartedMessage {
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
//
// The turn and the `/new` deliberately arrive through chats with different
// derived SessionKeys (two channel instances of the same linked user): both
// resolve the same main session, so they must share one queue slot. Keying the
// queue on the raw SessionKey let a `/new` on one channel skip past a turn
// still running on another.
func TestHandleNewSessionCommandWaitsForActiveTurn(t *testing.T) {
	ctx := context.Background()
	c := &Coordinator{queue: newSessionQueue()}
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})

	turnChat := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	turnChat.Service = rc.Service
	turnChat.SessionKey = agent.BuildUserSessionKey(rc.AgentID, "user-1", "channel:bot-b:private")
	turnChat.Channel = session.Channel(turnChat.SessionKey)
	if turnChat.SessionKey == rc.SessionKey {
		t.Fatal("test needs two distinct channel instances")
	}
	if turnChat.queueKey() != rc.queueKey() {
		t.Fatalf("queue keys differ for one main session: %q vs %q", turnChat.queueKey(), rc.queueKey())
	}

	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}

	turnRunning := make(chan struct{})
	turnUnblock := make(chan struct{})
	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		stream, doneC, err := c.queue.Enqueue(ctx, turnChat.queueKey(), func(context.Context) (*pkgchannel.ChatStream, error) {
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
	go func() {
		replyC <- c.handleNewSessionCommand(ctx, rc, pkgchannel.IncomingMessage{Platform: "telegram"})
	}()

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

// TestQueueKeyMatchesSessionBoundary pins the queue boundary to the session
// boundary for every chat shape: linked users share one slot across channel
// instances; group and unlinked chats keep their session key, which for them
// IS the binding.
func TestQueueKeyMatchesSessionBoundary(t *testing.T) {
	linkedA := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	linkedB := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	linkedB.SessionKey = agent.BuildUserSessionKey(linkedA.AgentID, "user-1", "channel:bot-b:private")
	linkedB.Channel = session.Channel(linkedB.SessionKey)
	if linkedA.queueKey() != linkedB.queueKey() {
		t.Fatalf("one main session, two queue slots: %q vs %q", linkedA.queueKey(), linkedB.queueKey())
	}

	otherUser := newRotateTestChat(t, auth.User{ID: "user-2", Role: auth.RoleUser})
	if otherUser.queueKey() == linkedA.queueKey() {
		t.Fatal("different users must not share a queue slot")
	}

	unlinked := newCompactTestChat(t, "", auth.User{ID: "user-1", Role: auth.RoleUser})
	if unlinked.queueKey() != unlinked.SessionKey {
		t.Fatalf("unlinked chat queue key = %q, want its session key %q", unlinked.queueKey(), unlinked.SessionKey)
	}

	const groupID = "11111111-1111-4111-8111-111111111111"
	group := newCompactTestChat(t, groupID, auth.User{})
	if group.queueKey() != group.SessionKey {
		t.Fatalf("group chat queue key = %q, want its session key %q", group.queueKey(), group.SessionKey)
	}
}
