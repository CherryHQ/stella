package channel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type rotationAgentStore struct{ enabled bool }

func (s rotationAgentStore) GetAgent(context.Context, string) (config.Agent, error) {
	return config.Agent{ID: "cmd-agent", Scope: config.AgentScopeSystem, Enabled: s.enabled}, nil
}

func (s rotationAgentStore) ListAgents(context.Context) ([]config.Agent, error) { return nil, nil }

func newRotationAgentAccess(enabled bool) *agentaccess.Service {
	return agentaccess.NewService(rotationAgentStore{enabled: enabled}, nil)
}

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
	c := &Coordinator{queue: newSessionQueue(), agentAccess: newRotationAgentAccess(true)}
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

// TestDurableNewSessionCommandIsALiveFIFOBarrier proves the production path,
// not only the process-local fairness queue: a /new accepted by another
// replica cannot rotate while an earlier durable item for the ChatBinding is
// still running.
func TestDurableNewSessionCommandIsALiveFIFOBarrier(t *testing.T) {
	ctx := t.Context()
	db := dbtest.New(t)
	c := &Coordinator{db: db, queue: newSessionQueue(), agentAccess: newRotationAgentAccess(true)}
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	configureNewCommandChannel(t, c, rc, "durable-new-channel")

	before, err := rc.CurrentSessionForRotation(ctx)
	if err != nil {
		t.Fatalf("CurrentSessionForRotation: %v", err)
	}
	q := sqlc.New(db)
	predecessor, err := q.CreateChannelBindingFIFO(ctx, sqlc.CreateChannelBindingFIFOParams{
		ID: "51000000-0000-0000-0000-000000000001", ChannelID: rc.ChatCtx.ChannelID,
		BindingKey: rc.queueKey(), PrincipalID: rc.sessionUserID(), SourceKey: "message:predecessor",
		Kind: "message", Payload: []byte(`[{"kind":"text","text":"earlier"}]`), ImmutableMedia: []byte(`[]`),
	})
	if err != nil {
		t.Fatalf("create predecessor: %v", err)
	}
	claimed, err := q.ClaimChannelBindingFIFOHead(ctx, predecessor.ID)
	if err != nil {
		t.Fatalf("claim predecessor: %v", err)
	}

	replyC := make(chan string, 1)
	go func() {
		replyC <- c.handleNewSessionCommand(ctx, rc, pkgchannel.IncomingMessage{
			Platform: "telegram", ChannelID: rc.ChatCtx.ChannelID,
			ChatID: "physical-chat", SenderID: "sender", MessageID: "new-live-barrier",
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		var count int
		err := db.QueryRow(ctx, `SELECT count(*) FROM channel_binding_fifo WHERE kind = 'new' AND binding_key = $1`, rc.queueKey()).Scan(&count)
		if err != nil {
			t.Fatalf("count queued /new: %v", err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("/new was not durably accepted behind its predecessor")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case reply := <-replyC:
		t.Fatalf("durable /new passed its live FIFO predecessor: %q", reply)
	case <-time.After(100 * time.Millisecond):
	}
	current, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession before predecessor completion: %v", err)
	}
	if current.ID != before.ID {
		t.Fatalf("durable /new rotated early: %q -> %q", before.ID, current.ID)
	}

	rows, err := q.CompleteChannelBindingFIFOControl(ctx, sqlc.CompleteChannelBindingFIFOControlParams{
		ID: predecessor.ID, ClaimToken: claimed.ClaimToken,
	})
	if err != nil || rows != 1 {
		t.Fatalf("complete predecessor: rows=%d err=%v", rows, err)
	}
	select {
	case reply := <-replyC:
		if reply != pkgchannel.NewSessionStartedMessage {
			t.Fatalf("/new reply = %q, want %q", reply, pkgchannel.NewSessionStartedMessage)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("durable /new did not run after its predecessor completed")
	}
	rotated, err := rc.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("ResolveSession after /new: %v", err)
	}
	if rotated.ID == before.ID {
		t.Fatal("durable /new did not rotate at the FIFO head")
	}

	row, err := q.GetChannelBindingFIFOBySource(ctx, sqlc.GetChannelBindingFIFOBySourceParams{
		ChannelID: rc.ChatCtx.ChannelID, SourceKey: "command:physical-chat:new-live-barrier",
	})
	if err != nil {
		t.Fatalf("get durable /new receipt: %v", err)
	}
	if row.Status != "completed" || !row.ExpectedSessionID.Valid || row.ExpectedSessionID.String != before.ID {
		t.Fatalf("durable /new = status %q expected_session_id %#v", row.Status, row.ExpectedSessionID)
	}
}

func TestSessionRotationAuthorizationRejectsDisabledAgent(t *testing.T) {
	rc := newRotateTestChat(t, auth.User{ID: "user-1", Role: auth.RoleUser})
	if err := rc.AuthorizeUse(context.Background(), newRotationAgentAccess(false)); !errors.Is(err, agentaccess.ErrForbidden) {
		t.Fatalf("AuthorizeUse disabled agent = %v, want forbidden", err)
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
