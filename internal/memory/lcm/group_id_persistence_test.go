package lcm

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// newEmptyGroupState creates an FK-valid ctx_group_state row and returns its id.
func newEmptyGroupState(t *testing.T, p *Provider) string {
	t.Helper()
	gid := uuid.Must(uuid.NewV7()).String()
	if _, err := p.q.CreateGroupState(context.Background(), sqlc.CreateGroupStateParams{
		ID: gid, Platform: "test", PlatformGroupID: "grp-" + gid,
	}); err != nil {
		t.Fatalf("create group state: %v", err)
	}
	return gid
}

// TestGroupID_DurableAcrossReload proves the group identity is now persisted on
// ctx_conversation.group_id and survives both a re-read and a fresh Provider
// (restart-style), rather than living only on the in-flight runtime Info.
func TestGroupID_DurableAcrossReload(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	const agentID = "agent-a"
	gid := newEmptyGroupState(t, p)
	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), gid), agentID)

	info := memory.SessionInfo{
		ID:      agentID + ":group:" + gid,
		AgentID: agentID,
		UserID:  gid, // group session owned by the group
		GroupID: gid,
		Channel: "group:" + gid,
		Kind:    "chat",
	}
	if err := p.SaveInfo(ctx, info); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}

	got, err := p.LoadInfo(ctx, info.ID)
	if err != nil {
		t.Fatalf("LoadInfo: %v", err)
	}
	if got.GroupID != gid {
		t.Fatalf("GroupID after reload = %q, want %q", got.GroupID, gid)
	}

	// Restart-style: a fresh Provider over the same pool still sees the durable id.
	p2, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new (restart): %v", err)
	}
	got2, err := p2.LoadInfo(ctx, info.ID)
	if err != nil {
		t.Fatalf("LoadInfo (restart): %v", err)
	}
	if got2.GroupID != gid {
		t.Fatalf("GroupID after restart = %q, want %q", got2.GroupID, gid)
	}
}

// TestGroupID_SaveMakesLegacyNullRowDurable proves that a legacy canonical group
// conversation whose group_id is still NULL adopts the supplied group_id on the
// next SaveInfo update, without a migration backfill.
func TestGroupID_SaveMakesLegacyNullRowDurable(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	const agentID = "agent-a"
	gid := newEmptyGroupState(t, p)
	sessionID := agentID + ":group:" + gid

	// Legacy shape: a group conversation persisted with group_id NULL.
	if _, err := p.q.CreateConversation(context.Background(), sqlc.CreateConversationParams{
		ID:        uuid.Must(uuid.NewV7()).String(),
		SessionID: sessionID,
		Channel:   "group:" + gid,
		Kind:      "chat",
		AgentID:   pgnull.Text(agentID),
		UserID:    pgtype.Text{String: gid, Valid: true},
		GroupID:   pgtype.Text{}, // NULL
	}); err != nil {
		t.Fatalf("seed legacy group conversation: %v", err)
	}

	ctx := authz.WithAgentID(authz.WithUserID(context.Background(), gid), agentID)
	// An ordinary SaveInfo update (existing row) carrying the group id.
	if err := p.SaveInfo(ctx, memory.SessionInfo{
		ID: sessionID, AgentID: agentID, UserID: gid, GroupID: gid, Kind: "chat",
	}); err != nil {
		t.Fatalf("SaveInfo update: %v", err)
	}

	got, err := p.LoadInfo(ctx, sessionID)
	if err != nil {
		t.Fatalf("LoadInfo: %v", err)
	}
	if got.GroupID != gid {
		t.Fatalf("legacy row group_id after save = %q, want %q", got.GroupID, gid)
	}
}

// TestGroupID_CheckRejectsUserGroupMismatch proves the DB CHECK constraint
// refuses a row whose group_id is set but whose user_id is not the group id.
func TestGroupID_CheckRejectsUserGroupMismatch(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	const agentID = "agent-a"
	gid := newEmptyGroupState(t, p)

	_, err = p.q.CreateConversation(context.Background(), sqlc.CreateConversationParams{
		ID:        uuid.Must(uuid.NewV7()).String(),
		SessionID: agentID + ":group:" + gid,
		Channel:   "group:" + gid,
		Kind:      "chat",
		AgentID:   pgnull.Text(agentID),
		UserID:    pgtype.Text{String: "some-other-user", Valid: true}, // != group id
		GroupID:   pgnull.Text(gid),
	})
	if err == nil {
		t.Fatal("expected the group-owner CHECK to reject a mismatched user_id/group_id")
	}
	if !strings.Contains(err.Error(), "ctx_conversation_group_owner_check") {
		t.Fatalf("error should mention the group-owner check, got: %v", err)
	}
}

// TestListInfoForReview_ExcludesOwnerlessRows proves the production review SQL
// skips legacy rows with a NULL/empty user_id (review is user-scoped) while
// returning owned rows for the agent.
func TestListInfoForReview_ExcludesOwnerlessRows(t *testing.T) {
	db := openTestDB(t)
	p, err := New(db, nil, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	const agentID = "review-agent"

	if _, err := p.q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID:        uuid.Must(uuid.NewV7()).String(),
		SessionID: "s-owned",
		Channel:   "web",
		Kind:      "chat",
		AgentID:   pgnull.Text(agentID),
		UserID:    pgtype.Text{String: "user-1", Valid: true},
	}); err != nil {
		t.Fatalf("seed owned conversation: %v", err)
	}
	// Legacy ownerless row: user_id NULL.
	if _, err := p.q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID:        uuid.Must(uuid.NewV7()).String(),
		SessionID: "s-ownerless",
		Channel:   "web",
		Kind:      "chat",
		AgentID:   pgnull.Text(agentID),
		UserID:    pgtype.Text{}, // NULL
	}); err != nil {
		t.Fatalf("seed ownerless conversation: %v", err)
	}

	recs, err := p.ListInfoForReview(ctx, memory.ListOptions{AgentID: agentID})
	if err != nil {
		t.Fatalf("ListInfoForReview: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "s-owned" {
		t.Fatalf("expected only the owned row, got %+v", recs)
	}
}
