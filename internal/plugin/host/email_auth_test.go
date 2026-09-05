package host_test

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin/host"
)

func TestResolveEmailUserAuthorityMatrix(t *testing.T) {
	user, err := authz.NewUserAuthority("user-1", false)
	if err != nil {
		t.Fatal(err)
	}
	delegated, err := authz.NewAgentAuthority("owner-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	group, err := authz.NewGroupAgentAuthority("group-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	guest, err := authz.NewGuestAuthority("guest-1", "channel-1")
	if err != nil {
		t.Fatal(err)
	}
	system, err := authz.NewSystemAuthority("maintenance")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		authority authz.Authority
		wantUser  string
		wantErr   error
	}{
		{name: "user", authority: user, wantUser: "user-1"},
		{name: "delegated", authority: delegated, wantUser: "owner-1"},
		{name: "group", authority: group, wantErr: authz.ErrUnauthenticated},
		{name: "guest", authority: guest, wantErr: authz.ErrUnauthenticated},
		{name: "system", authority: system, wantErr: authz.ErrUnauthenticated},
		{name: "invalid", authority: authz.Authority{}, wantErr: authz.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := host.ResolveEmailUser(authz.WithAuthority(context.Background(), tt.authority))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err=%v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.wantUser {
				t.Fatalf("got user=%q err=%v, want %q", got, err, tt.wantUser)
			}
		})
	}
	if _, err := host.ResolveEmailUser(context.Background()); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("missing authority err=%v, want forbidden", err)
	}
}

func TestAuthorizeEmailToolPreservesIdentityErrorsAndAuthority(t *testing.T) {
	const tool = "email_message_list"
	const discover = "email_account_list"
	if _, err := host.AuthorizeEmailTool(context.Background(), tool, discover); err == nil || err.Error() != mustToolIdentityError(t, context.Background(), tool) {
		t.Fatalf("missing user err=%v, want raw ToolIdentity error", err)
	}
	noAgent := authz.WithUserID(context.Background(), "user-1")
	if _, err := host.AuthorizeEmailTool(noAgent, tool, discover); err == nil || err.Error() != mustToolIdentityError(t, noAgent, tool) {
		t.Fatalf("missing agent err=%v, want raw ToolIdentity error", err)
	}
	valid := authz.WithAgentID(authz.WithUserID(context.Background(), "user-1"), "agent-1")
	got, err := host.AuthorizeEmailTool(valid, tool, discover)
	if err != nil {
		t.Fatalf("valid identity: %v", err)
	}
	authority, ok := authz.AuthorityFromContext(got)
	if !ok || authority.Kind() != authz.ActorAgent || authority.UserID() != "user-1" || authority.AgentID() != "agent-1" {
		t.Fatalf("authorized context authority=%+v present=%v, want delegated user/agent", authority, ok)
	}
	group := authz.WithAgentID(authz.WithGroupID(context.Background(), "group-1"), "agent-1")
	if _, err := host.AuthorizeEmailTool(group, tool, discover); err == nil || err.Error() != mustToolIdentityError(t, group, tool) {
		t.Fatalf("group identity err=%v, want no-user refusal", err)
	}
}

func mustToolIdentityError(t *testing.T, ctx context.Context, tool string) string {
	t.Helper()
	_, err := authz.ToolIdentity(ctx, tool)
	if err == nil {
		t.Fatal("ToolIdentity unexpectedly accepted test context")
	}
	return err.Error()
}
