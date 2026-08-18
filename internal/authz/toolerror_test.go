package authz_test

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

func TestToolAuthoritySupportsConfinedGroupAgent(t *testing.T) {
	ctx := authz.WithGroupID(context.Background(), "group-1")
	ctx = authz.WithAgentID(ctx, "agent-1")
	authority, err := authz.ToolAuthority(ctx, "memory")
	if err != nil {
		t.Fatalf("ToolAuthority: %v", err)
	}
	if authority.Kind() != authz.ActorGroupAgent || authority.GroupID() != "group-1" || authority.AgentID() != "agent-1" || authority.UserID() != "" {
		t.Fatalf("group authority = %#v", authority)
	}
}

func TestToolAuthorityRejectsMixedGroupIdentity(t *testing.T) {
	ctx := authz.WithGroupID(context.Background(), "group-1")
	ctx = authz.WithUserID(ctx, "user-1")
	ctx = authz.WithAgentID(ctx, "agent-1")
	if _, err := authz.ToolAuthority(ctx, "memory"); err == nil {
		t.Fatal("mixed user/group context minted a tool authority")
	}
}

func TestToolAuthorityKeepsDelegatedDMAuthority(t *testing.T) {
	ctx := authz.WithUserID(context.Background(), "user-1")
	ctx = authz.WithAgentID(ctx, "agent-1")
	authority, err := authz.ToolAuthority(ctx, "memory")
	if err != nil {
		t.Fatalf("ToolAuthority: %v", err)
	}
	if authority.Kind() != authz.ActorAgent || authority.UserID() != "user-1" || authority.AgentID() != "agent-1" || authority.GroupID() != "" {
		t.Fatalf("DM authority = %#v", authority)
	}
}
