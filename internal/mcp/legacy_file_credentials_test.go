package mcp

import (
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
)

func TestMigrationAuthorityUsesTrustedOwnerAdapters(t *testing.T) {
	tests := []struct {
		name      string
		scope     plugin.Scope
		userID    string
		agentID   string
		kind      authz.ActorKind
		wantUser  string
		wantAgent string
	}{
		{name: "system maintenance", scope: plugin.ScopeSystem, kind: authz.ActorSystem},
		{name: "shared system agent maintenance", scope: plugin.ScopeSystemAgent, agentID: "agent", kind: authz.ActorSystem},
		{name: "durable user owner", scope: plugin.ScopeUser, userID: "user", kind: authz.ActorUser, wantUser: "user"},
		{name: "durable user agent owner", scope: plugin.ScopeUserAgent, userID: "user", agentID: "agent", kind: authz.ActorAgent, wantUser: "user", wantAgent: "agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := migrationAuthority(tt.scope, tt.userID, tt.agentID)
			if err != nil {
				t.Fatalf("migrationAuthority: %v", err)
			}
			if got.Kind() != tt.kind || string(got.UserID()) != tt.wantUser || string(got.AgentID()) != tt.wantAgent {
				t.Fatalf("authority=(kind=%s user=%q agent=%q), want=(kind=%s user=%q agent=%q)", got.Kind(), got.UserID(), got.AgentID(), tt.kind, tt.wantUser, tt.wantAgent)
			}
			if tt.kind == authz.ActorSystem && got.Component() != "legacy-file-credential-migration" {
				t.Fatalf("system component=%q, want named migration component", got.Component())
			}
		})
	}
}

func TestMigrationAuthorityRejectsMissingDurableOwner(t *testing.T) {
	for _, tt := range []struct {
		name   string
		scope  plugin.Scope
		userID string
		agent  string
	}{
		{name: "user without user id", scope: plugin.ScopeUser},
		{name: "user agent without user id", scope: plugin.ScopeUserAgent, agent: "agent"},
		{name: "user agent without agent id", scope: plugin.ScopeUserAgent, userID: "user"},
		{name: "system with owner", scope: plugin.ScopeSystem, userID: "user"},
		{name: "system agent without agent id", scope: plugin.ScopeSystemAgent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := migrationAuthority(tt.scope, tt.userID, tt.agent); err == nil {
				t.Fatal("migrationAuthority succeeded without a valid durable owner")
			}
		})
	}
}

func TestLegacyTargetOwnersSystemPerUserOAuthIncludesEveryUserAndClientOwner(t *testing.T) {
	migration := legacyFileCredentialMigration{
		old:  Registration{ID: "old", AuthType: AuthTypeOAuth, CredentialMode: CredentialModePerUser},
		file: Registration{ID: "file", Scope: ScopeSystem, AuthType: AuthTypeOAuth, CredentialMode: CredentialModePerUser, OAuthClientSecretRef: "CLIENT"},
		rows: []legacyVaultOwner{
			{owner: CredentialOwner{Scope: ScopeUser, UserID: "u2"}, name: oauthBundleName("old")},
			{owner: CredentialOwner{Scope: ScopeUser, UserID: "u1"}, name: oauthBundleName("old")},
		},
	}
	got := migration.targetOwners()
	if len(got) != 3 {
		t.Fatalf("owners=%+v, want two user grants plus client owner", got)
	}
	want := []CredentialOwner{
		{Scope: ScopeSystem},
		{Scope: ScopeUser, UserID: "u1"},
		{Scope: ScopeUser, UserID: "u2"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("owners[%d]=%+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLegacyTargetOwnersPrivateBearerConvertsToUserGrant(t *testing.T) {
	migration := legacyFileCredentialMigration{
		old:  Registration{ID: "old", Scope: ScopeUserAgent, UserID: "u1", AgentID: "a1", AuthType: AuthTypeBearer, CredentialMode: CredentialModeShared},
		file: Registration{ID: "file", Scope: ScopeUserAgent, UserID: "u1", AgentID: "a1", AuthType: AuthTypeBearer, CredentialMode: CredentialModePerUser, FileKey: plugin.ResourceKey{Scope: plugin.ScopeUserAgent, UserID: "u1", AgentID: "a1", Kind: plugin.ResourceMCP, Name: "x"}},
		rows: []legacyVaultOwner{{owner: CredentialOwner{Scope: ScopeUserAgent, UserID: "u1", AgentID: "a1"}, name: "TOKEN"}},
	}
	got := migration.targetOwners()
	want := []CredentialOwner{{Scope: ScopeUser, UserID: "u1"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("owners=%+v, want %+v", got, want)
	}
}
