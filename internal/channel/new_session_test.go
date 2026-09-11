package channel

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/auth"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/platform/config"
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
