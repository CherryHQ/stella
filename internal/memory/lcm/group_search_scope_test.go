package lcm

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

// groupTurnCtx is the context a group turn actually runs under: the group id and
// the agent, and deliberately no user id (D9). The memory tool builds its search
// scope from exactly this, so these tests exercise the real gap.
func groupTurnCtx(groupID, agentID string) context.Context {
	return authz.WithAgentID(authz.WithGroupID(context.Background(), groupID), agentID)
}

// seedGroupSession persists a group conversation the way the runtime does —
// user_id = group_id — and appends one message to it.
func seedGroupSession(t *testing.T, p *Provider, groupID, agentID, sessionID, content string) memory.Session {
	t.Helper()
	ctx := groupTurnCtx(groupID, agentID)
	info := memory.SessionInfo{
		ID:      sessionID,
		AgentID: agentID,
		UserID:  groupID,
		GroupID: groupID,
		Channel: "group:" + groupID,
		Kind:    "chat",
	}
	if err := p.SaveInfo(ctx, info); err != nil {
		t.Fatalf("SaveInfo %s: %v", sessionID, err)
	}
	sess := memory.Session{ID: sessionID, AgentID: agentID, UserID: groupID, GroupID: groupID}
	if err := p.Append(ctx, sess, ai.UserMessage{Content: content}); err != nil {
		t.Fatalf("append to %s: %v", sessionID, err)
	}
	return sess
}

// searchAsGroupTurn searches with the scope the memory tool derives from a group
// turn's context: no explicit user, because a group turn has none.
func searchAsGroupTurn(t *testing.T, p *Provider, groupID, agentID, sessionID, pattern string) []memory.SearchResult {
	t.Helper()
	results, err := p.Search(groupTurnCtx(groupID, agentID), memory.Session{
		ID:      sessionID,
		AgentID: agentID,
	}, memory.SearchQuery{Text: pattern, Scope: memory.SearchScopeMessages})
	if err != nil {
		t.Fatalf("group search %q: %v", pattern, err)
	}
	return results
}

// TestGroupSearch_ExcludesArchivedPredecessorMessages proves a reset removes the
// archived predecessor from recall without deleting its stored transcript.
func TestGroupSearch_ExcludesArchivedPredecessorMessages(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	const agentID = "agent-a"
	gid := newEmptyGroupState(t, p)
	predecessorID := agentID + ":group:" + gid + ":s1"
	successorID := agentID + ":group:" + gid + ":s2"

	seedGroupSession(t, p, gid, agentID, predecessorID, "the quarterly kumquat ledger was approved")

	ctx := groupTurnCtx(gid, agentID)
	if err := p.RotateInfo(ctx, predecessorID, memory.SessionInfo{
		ID:      successorID,
		AgentID: agentID,
		UserID:  gid,
		GroupID: gid,
		Channel: "group:" + gid,
		Kind:    "chat",
	}); err != nil {
		t.Fatalf("RotateInfo: %v", err)
	}
	predecessor, err := p.LoadInfo(ctx, predecessorID)
	if err != nil {
		t.Fatalf("LoadInfo predecessor: %v", err)
	}
	if !predecessor.Archived {
		t.Fatal("predecessor should be archived after rotation")
	}

	// The turn now runs in the successor session. Content that exists only in the
	// archived predecessor must no longer be recalled.
	results := searchAsGroupTurn(t, p, gid, agentID, successorID, "kumquat")
	if len(results) != 0 {
		t.Fatalf("archived predecessor leaked into recall: %+v", results)
	}
}

// TestGroupSearch_IsolatedFromOtherGroupsAndUsers proves the group scope key is a
// boundary, not just a lookup: one group never sees another group's messages, and
// never sees a personal (non-group) user's messages.
func TestGroupSearch_IsolatedFromOtherGroupsAndUsers(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	const agentID = "agent-a"
	groupA := newEmptyGroupState(t, p)
	groupB := newEmptyGroupState(t, p)

	sessA := agentID + ":group:" + groupA
	sessB := agentID + ":group:" + groupB
	seedGroupSession(t, p, groupA, agentID, sessA, "shibboleth from group A")
	seedGroupSession(t, p, groupB, agentID, sessB, "shibboleth from group B")

	// A personal DM session for a real user, owned by that user id.
	const personalUser = "user-personal"
	personalCtx := authz.WithAgentID(authz.WithUserID(context.Background(), personalUser), agentID)
	personalSess := memory.Session{ID: agentID + ":dm:" + personalUser, AgentID: agentID, UserID: personalUser}
	if err := p.SaveInfo(personalCtx, memory.SessionInfo{
		ID:      personalSess.ID,
		AgentID: agentID,
		UserID:  personalUser,
		Channel: "test:user:" + personalUser + ":private",
		Kind:    "chat",
	}); err != nil {
		t.Fatalf("SaveInfo personal: %v", err)
	}
	if err := p.Append(personalCtx, personalSess, ai.UserMessage{Content: "shibboleth from a private chat"}); err != nil {
		t.Fatalf("append personal: %v", err)
	}

	results := searchAsGroupTurn(t, p, groupA, agentID, sessA, "shibboleth")
	if len(results) != 1 {
		t.Fatalf("group A search returned %d hits, want only its own: %+v", len(results), results)
	}
	if results[0].SessionID != sessA {
		t.Fatalf("group A search leaked session %q", results[0].SessionID)
	}
	if !strings.Contains(results[0].Content, "group A") {
		t.Fatalf("group A search leaked content %q", results[0].Content)
	}
}
