package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func privateBinding() ChannelRequest {
	return ChannelRequest{UserID: "u1", AgentID: "agent1", Channel: Channel("agent1:tg:ext-1:private")}
}

func groupBinding() ChannelRequest {
	return ChannelRequest{UserID: testGroupID, AgentID: "agent1", GroupID: testGroupID, Channel: Channel("group:chat-9")}
}

// TestResolveChatChannelCreatesGeneratedID proves a chat's first session is no
// longer pinned to its derived key: the id is generated, so the chat can rotate
// onto a successor later without changing identity.
func TestResolveChatChannelCreatesGeneratedID(t *testing.T) {
	r, _ := newTestRegistry(t)
	req := privateBinding()
	req.LegacyID = "agent1:tg:ext-1:private"

	info, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	if info.ID == req.LegacyID {
		t.Fatal("a new chat-channel session must not reuse its derived key as the session id")
	}
	if info.Channel != string(req.Channel) || info.Kind != string(KindChat) {
		t.Fatalf("info = %+v, want the binding's channel and kind=chat", info)
	}

	again, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("second ResolveChatChannel: %v", err)
	}
	if again.ID != info.ID {
		t.Fatalf("second resolve = %q, want the bound session %q", again.ID, info.ID)
	}
}

// TestResolveChatChannelFindsLegacyKeyAsIDSession is the continuity case for
// deployments upgraded in place: the pre-binding session's id IS the chat key and
// its channel already matches, so the binding query finds it and nothing new is
// created.
func TestResolveChatChannelFindsLegacyKeyAsIDSession(t *testing.T) {
	r, s := newTestRegistry(t)
	req := privateBinding()
	req.LegacyID = "agent1:tg:ext-1:private"
	legacy := NewInfo(req.LegacyID, "agent1", "u1", string(req.Channel), KindChat, "", time.Now().UTC())
	if err := s.save(context.Background(), legacy); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}

	info, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	if info.ID != legacy.ID {
		t.Fatalf("resolved %q, want the legacy session %q", info.ID, legacy.ID)
	}
	if len(s.sessions) != 1 {
		t.Fatalf("store holds %d sessions, want only the legacy one", len(s.sessions))
	}
}

// TestResolveChatChannelAdoptsBlankChannelLegacySession covers rows written
// before the channel was recorded (the column defaults to ”). The binding query
// cannot see them, so the exact-key fallback adopts the row and persists the
// binding — after which the binding query finds it directly.
func TestResolveChatChannelAdoptsBlankChannelLegacySession(t *testing.T) {
	r, s := newTestRegistry(t)
	req := privateBinding()
	req.LegacyID = "agent1:tg:ext-1:private"
	legacy := NewInfo(req.LegacyID, "agent1", "u1", "", KindChat, "", time.Now().UTC())
	if err := s.save(context.Background(), legacy); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}

	info, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	if info.ID != legacy.ID {
		t.Fatalf("resolved %q, want the legacy session %q", info.ID, legacy.ID)
	}
	if got := s.sessions[legacy.ID].Channel; got != string(req.Channel) {
		t.Fatalf("persisted channel = %q, want the binding %q", got, req.Channel)
	}

	// The binding is durable now: drop the legacy lookup and it still resolves.
	bare := privateBinding()
	direct, err := r.ResolveChatChannel(context.Background(), bare)
	if err != nil {
		t.Fatalf("ResolveChatChannel without the legacy id: %v", err)
	}
	if direct.ID != legacy.ID {
		t.Fatalf("binding query resolved %q, want %q", direct.ID, legacy.ID)
	}
}

// TestResolveChatChannelIgnoresArchivedLegacySession keeps a rotated-away chat
// from being resurrected through its old key.
func TestResolveChatChannelIgnoresArchivedLegacySession(t *testing.T) {
	r, s := newTestRegistry(t)
	req := privateBinding()
	req.LegacyID = "agent1:tg:ext-1:private"
	legacy := NewInfo(req.LegacyID, "agent1", "u1", "", KindChat, "", time.Now().UTC())
	legacy.Archived = true
	if err := s.save(context.Background(), legacy); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}

	info, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	if info.ID == legacy.ID {
		t.Fatal("an archived legacy session must not be adopted")
	}
}

// TestResolveChatChannelGroupBindsOnOwner proves the group binding ignores the
// channel: a group's channel varies with the reply channel a message arrives
// through, so binding on it would split one group across platforms.
func TestResolveChatChannelGroupBindsOnOwner(t *testing.T) {
	r, _ := newTestRegistry(t)
	req := groupBinding()
	first, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	if first.GroupID != testGroupID || first.UserID != testGroupID {
		t.Fatalf("info = %+v, want a session owned by the group", first)
	}

	viaOtherChannel := req
	viaOtherChannel.Channel = Channel("channel:tg-bot:group:chat-9")
	second, err := r.ResolveChatChannel(context.Background(), viaOtherChannel)
	if err != nil {
		t.Fatalf("ResolveChatChannel via another reply channel: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("a group must keep one session per agent across reply channels")
	}
}

// TestResolveChatChannelGroupAdoptsLegacyGroupID reattaches a legacy group row
// written before group_id was durable, matching validateResume's reconciliation.
func TestResolveChatChannelGroupAdoptsLegacyGroupID(t *testing.T) {
	r, s := newTestRegistry(t)
	req := groupBinding()
	req.LegacyID = "agent1:group:" + testGroupID
	legacy := NewInfo(req.LegacyID, "agent1", testGroupID, "", KindChat, "", time.Now().UTC())
	if err := s.save(context.Background(), legacy); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}

	info, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	if info.ID != legacy.ID || info.GroupID != testGroupID {
		t.Fatalf("info = %+v, want the legacy row reattached to the group", info)
	}
}

// TestResolveChatChannelRejectsForeignGroupOwner fails closed on the ownership
// invariant: a row that names a different durable group is never rebound.
func TestResolveChatChannelRejectsForeignGroupOwner(t *testing.T) {
	r, s := newTestRegistry(t)
	req := groupBinding()
	foreign := NewInfo("sess-foreign", "agent1", testGroupID, "group:chat-9", KindChat, "", time.Now().UTC())
	foreign.GroupID = "22222222-2222-4222-8222-222222222222"
	if err := s.save(context.Background(), foreign); err != nil {
		t.Fatalf("seed foreign session: %v", err)
	}

	if _, err := r.ResolveChatChannel(context.Background(), req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ResolveChatChannel = %v, want ErrForbidden", err)
	}
}

// TestResolveChatChannelRejectsGroupRowForPrivateBinding is the mirror case: a
// private chat must never resolve onto a group-owned session.
func TestResolveChatChannelRejectsGroupRowForPrivateBinding(t *testing.T) {
	r, s := newTestRegistry(t)
	req := ChannelRequest{UserID: testGroupID, AgentID: "agent1", Channel: Channel("group:chat-9")}
	owned := NewInfo("sess-group", "agent1", testGroupID, "group:chat-9", KindChat, "", time.Now().UTC())
	owned.GroupID = testGroupID
	if err := s.save(context.Background(), owned); err != nil {
		t.Fatalf("seed group session: %v", err)
	}

	if _, err := r.ResolveChatChannel(context.Background(), req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ResolveChatChannel = %v, want ErrForbidden", err)
	}
}

// TestRotateChannelArchivesAndSucceeds is the `/new` path for a chat channel.
func TestRotateChannelArchivesAndSucceeds(t *testing.T) {
	r, s := newTestRegistry(t)
	req := groupBinding()
	before, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}

	rotate := req
	rotate.ExpectedSessionID = before.ID
	successor, err := r.RotateChannel(context.Background(), rotate)
	if err != nil {
		t.Fatalf("RotateChannel: %v", err)
	}
	if successor.ID == before.ID {
		t.Fatal("rotation must create a successor")
	}
	if !s.sessions[before.ID].Archived {
		t.Fatal("rotation must archive the predecessor")
	}
	if successor.GroupID != testGroupID {
		t.Fatalf("successor = %+v, want the group binding carried over", successor)
	}

	current, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel after rotation: %v", err)
	}
	if current.ID != successor.ID {
		t.Fatalf("binding resolves %q, want the successor %q", current.ID, successor.ID)
	}
}

// TestRotateChannelStaleExpectedIsNoOp covers a duplicate `/new`: the second one
// names a session the first already archived, so it must change nothing.
func TestRotateChannelStaleExpectedIsNoOp(t *testing.T) {
	r, _ := newTestRegistry(t)
	req := groupBinding()
	before, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	rotate := req
	rotate.ExpectedSessionID = before.ID
	first, err := r.RotateChannel(context.Background(), rotate)
	if err != nil {
		t.Fatalf("RotateChannel: %v", err)
	}

	if _, err := r.RotateChannel(context.Background(), rotate); !errors.Is(err, ErrStaleRotation) {
		t.Fatalf("duplicate RotateChannel = %v, want ErrStaleRotation", err)
	}
	current, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	if current.ID != first.ID {
		t.Fatal("a stale rotation must not rotate the chat a second time")
	}
}

// TestRotateChannelRollsBackFailedSuccessor proves a failed rotation leaves the
// chat on its existing session rather than on nothing.
func TestRotateChannelRollsBackFailedSuccessor(t *testing.T) {
	r, s := newTestRegistry(t)
	req := groupBinding()
	before, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	s.rotateErr = errors.New("insert failed")

	rotate := req
	rotate.ExpectedSessionID = before.ID
	if _, err := r.RotateChannel(context.Background(), rotate); err == nil {
		t.Fatal("RotateChannel must report the store failure")
	}
	s.rotateErr = nil
	current, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel: %v", err)
	}
	if current.ID != before.ID || current.Archived {
		t.Fatalf("current = %+v, want the original session still active", current)
	}
}

// TestRotateChannelWithoutExistingSessionCreates covers `/new` in a chat that
// never ran a turn: there is nothing to archive, so it is just a create.
func TestRotateChannelWithoutExistingSessionCreates(t *testing.T) {
	r, _ := newTestRegistry(t)
	info, err := r.RotateChannel(context.Background(), groupBinding())
	if err != nil {
		t.Fatalf("RotateChannel: %v", err)
	}
	if info.ID == "" || info.Archived {
		t.Fatalf("info = %+v, want a fresh active session", info)
	}
}

// TestChannelBindingRequiresIdentity fails closed on a malformed binding rather
// than resolving something adjacent.
func TestChannelBindingRequiresIdentity(t *testing.T) {
	r, _ := newTestRegistry(t)
	cases := map[string]ChannelRequest{
		"no user":              {AgentID: "agent1", Channel: "c"},
		"no channel":           {UserID: "u1", AgentID: "agent1"},
		"group owner mismatch": {UserID: "u1", AgentID: "agent1", GroupID: testGroupID, Channel: "c"},
	}
	for name, req := range cases {
		if _, err := r.ResolveChatChannel(context.Background(), req); err == nil {
			t.Errorf("%s: ResolveChatChannel must reject the binding", name)
		}
	}
}
