package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/vault"
)

// This exercises the cutover against PostgreSQL and the age-backed Vault. It
// covers a per-user OAuth bundle for two users, an existing revoked target
// grant, a client secret, an in-flight OAuth flow, and an idempotent retry.
func TestMigrateLegacyFileCredentialsRealPGVault(t *testing.T) {
	svc, _, userA, _ := setupInternal(t)
	_, ok := svc.vault.(*vault.Service)
	if !ok {
		t.Fatal("setup did not install the age-backed Vault")
	}
	userB := newMigrationUser(t, svc, "legacy-migration-b@example.test")

	ctx := t.Context()
	configID := uuid.NewString()
	childID := uuid.NewString()
	pluginID := "legacy-file-" + configID[:8]
	clientID := "legacy-client"
	bundleName := oauthBundleName(childID)
	clientSecretName := oauthClientSecretName(childID)
	targetBundleName := "MCP_FILE_BUNDLE_TARGET"
	targetClientSecretName := "MCP_FILE_CLIENT_TARGET"
	payload := json.RawMessage(`{"mcp_servers":{"main":{"url":"https://oauth.example","transport":"streamable_http","auth_type":"oauth","credential_mode":"per_user","metadata":{"oauth":{"client_id":"legacy-client"}}}}}`)
	refs := json.RawMessage(`{"mcp_servers":{"main":{"oauth_bundle":{"name":"` + bundleName + `","mode":"per_user","owner":"per_user"},"oauth_client_secret":{"name":"` + clientSecretName + `","scope":"system","user_id":"","agent_id":""}}}}`)
	spec, err := plugin.PublishDefinitionSpec([]byte(`{"origin":"remote_mcp","mcp_servers":{"main":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pool.Exec(ctx, `
		INSERT INTO plugin_definition(id,display_name,source,spec,default_enabled,revision,creator_user_id)
		VALUES($1,'legacy file migration','custom',$2::jsonb,false,1,NULL)`, pluginID, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pool.Exec(ctx, `
		INSERT INTO plugin_config(id,plugin_id,scope,enabled,config,credential_refs,revision)
		VALUES($1,$2,'system',true,$3::jsonb,$4::jsonb,1)`, configID, pluginID, payload, refs); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pool.Exec(ctx, `INSERT INTO plugin_config_mcp_server(id,config_id,server_key) VALUES($1,$2,'main')`, childID, configID); err != nil {
		t.Fatal(err)
	}

	bundleA := OAuthBundle{Version: 1, ClientID: clientID, TokenEndpoint: "https://oauth.example/token", AccessToken: "token-a", RefreshToken: "refresh-a", AccessExpiresAt: time.Now().UTC().Add(time.Hour)}
	bundleB := OAuthBundle{Version: 1, ClientID: clientID, TokenEndpoint: "https://oauth.example/token", AccessToken: "token-b", RefreshToken: "refresh-b", AccessExpiresAt: time.Now().UTC().Add(time.Hour)}
	putVaultJSON := func(owner CredentialOwner, name string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := setMigrationVault(ctx, svc, owner, name, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	putVaultJSON(CredentialOwner{Scope: ScopeUser, UserID: userA}, bundleName, bundleA)
	putVaultJSON(CredentialOwner{Scope: ScopeUser, UserID: userB}, bundleName, bundleB)
	if err := svc.vault.SetSystemScoped(ctx, ScopeSystem, "", clientSecretName, "client-secret"); err != nil {
		t.Fatal(err)
	}

	manager, err := home.NewWorkspaceManager(svc.pool, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	resources := plugin.NewResourceStore(manager)
	resourceKey := plugin.ResourceKey{Scope: plugin.ScopeSystem, Kind: plugin.ResourceMCP, Name: "migrated-oauth"}
	declaration := mcpconfig.Declaration{URL: "https://oauth.example", Transport: TransportStreamableHTTP, Authentication: mcpconfig.Authentication{
		Type: AuthTypeOAuth, Mode: CredentialModePerUser, ClientID: clientID, CredentialRef: targetBundleName, ClientSecretRef: targetClientSecretName,
	}}
	data, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resources.WriteMCP(ctx, resourceKey, "", data); err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewSystemAuthority("legacy-file-migration-test")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := resources.Get(ctx, resourceKey)
	if err != nil {
		t.Fatal(err)
	}
	target, err := RegistrationFromFileResource(resource, "", authority)
	if err != nil {
		t.Fatal(err)
	}

	flowID := uuid.NewString()
	if _, err := svc.pool.Exec(ctx, `INSERT INTO mcp_oauth_flow(id,server_id,user_id,credential_scope,credential_user_id,pkce_verifier,oauth_config,expires_at) VALUES($1,$2,$3,'user',$3,'verifier','{}',now()+interval '1 hour')`, flowID, childID, userA); err != nil {
		t.Fatal(err)
	}

	run := func() {
		tx, err := svc.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.MigrateLegacyFileCredentials(ctx, tx, resources, []plugin.MCPMigrationEvidence{{OldConfigID: configID, OldServerKey: "main", NewResource: resourceKey}}); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	run()

	assertBundle := func(userID, wantToken string) string {
		raw, err := svc.vault.GetScoped(ctx, ScopeUser, userID, "", fileGrantName(target, CredentialOwner{Scope: ScopeUser, UserID: userID}))
		if err != nil {
			t.Fatal(err)
		}
		var grant fileGrant
		if err := json.Unmarshal([]byte(raw), &grant); err != nil || grant.Bundle == nil || grant.Bundle.AccessToken != wantToken || grant.Generation == "" || grant.Revoked {
			t.Fatalf("target grant for %s = %s, parsed=%+v, err=%v", userID, raw, grant, err)
		}
		return grant.Generation
	}
	generationA := assertBundle(userA, "token-a")
	userAuthorityA, err := authz.NewUserAuthority(authz.UserID(userA), false)
	if err != nil {
		t.Fatal(err)
	}
	readyA, err := svc.FileCredentialReady(ctx, target, userAuthorityA)
	if err != nil || !readyA {
		t.Fatalf("migrated user A credential ready=%v err=%v", readyA, err)
	}
	userAuthorityB, err := authz.NewUserAuthority(authz.UserID(userB), false)
	if err != nil {
		t.Fatal(err)
	}
	readyB, err := svc.FileCredentialReady(ctx, target, userAuthorityB)
	if err != nil {
		t.Fatal(err)
	}
	if !readyB {
		t.Fatal("migrated user B grant is not runtime-ready")
	}
	assertBundle(userB, "token-b")
	keep := fileGrant{Generation: "keep-existing", Revoked: true}
	keepRaw, _ := json.Marshal(keep)
	if err := svc.vault.SetScoped(ctx, ScopeUser, userB, "", fileGrantName(target, CredentialOwner{Scope: ScopeUser, UserID: userB}), string(keepRaw)); err != nil {
		t.Fatal(err)
	}
	keptRaw, err := svc.vault.GetScoped(ctx, ScopeUser, userB, "", fileGrantName(target, CredentialOwner{Scope: ScopeUser, UserID: userB}))
	if err != nil || keptRaw != string(keepRaw) {
		t.Fatalf("revoked target grant changed: %q err=%v", keptRaw, err)
	}
	if got, err := svc.vault.GetScoped(ctx, ScopeSystem, "", "", targetClientSecretName); err != nil || got != "client-secret" {
		t.Fatalf("client secret = %q err=%v", got, err)
	}
	var consumed int
	if err := svc.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_oauth_flow WHERE id=$1 AND consumed_at IS NOT NULL AND expires_at <= now()`, flowID).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("pending flow consumed=%d err=%v", consumed, err)
	}

	run()
	if got := assertBundle(userA, "token-a"); got != generationA {
		t.Fatalf("retry rotated target generation from %q to %q", generationA, got)
	}
	readyBAfter, err := svc.FileCredentialReady(ctx, target, userAuthorityB)
	if !errors.Is(err, errFileMCPGrantRevoked) || readyBAfter {
		t.Fatalf("revoked user B runtime state ready=%v err=%v", readyBAfter, err)
	}
	keptAfter, err := svc.vault.GetScoped(ctx, ScopeUser, userB, "", fileGrantName(target, CredentialOwner{Scope: ScopeUser, UserID: userB}))
	if err != nil || keptAfter != string(keepRaw) {
		t.Fatalf("retry changed revoked user B grant: %q err=%v", keptAfter, err)
	}
}

func TestMigrateLegacyPrivateBearerToPerUserFileGrantRealPGVault(t *testing.T) {
	svc, _, userID, agentID := setupInternal(t)
	ctx := t.Context()
	configID := uuid.NewString()
	childID := uuid.NewString()
	pluginID := "legacy-bearer-" + configID[:8]
	sourceTokenName := credentialName(childID)
	targetTokenName := "MCP_FILE_TOKEN_TARGET"
	spec, err := plugin.PublishDefinitionSpec([]byte(`{"origin":"remote_mcp","mcp_servers":{"main":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"mcp_servers":{"main":{"url":"https://bearer.example","transport":"streamable_http","auth_type":"bearer","credential_mode":"shared"}}}`)
	refs := json.RawMessage(`{"mcp_servers":{"main":{"bearer":{"name":"` + sourceTokenName + `","scope":"user_agent","user_id":"` + userID + `","agent_id":"` + agentID + `"}}}}`)
	if _, err := svc.pool.Exec(ctx, `INSERT INTO plugin_definition(id,display_name,source,spec,default_enabled,revision,creator_user_id) VALUES($1,'Private Bearer','custom',$2::jsonb,false,1,$3::uuid)`, pluginID, spec, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pool.Exec(ctx, `INSERT INTO plugin_config(id,plugin_id,scope,user_id,agent_id,enabled,config,credential_refs,revision) VALUES($1,$2,'user_agent',$3,$4,true,$5::jsonb,$6::jsonb,1)`, configID, pluginID, userID, agentID, payload, refs); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pool.Exec(ctx, `INSERT INTO plugin_config_mcp_server(id,config_id,server_key) VALUES($1,$2,'main')`, childID, configID); err != nil {
		t.Fatal(err)
	}
	if err := svc.vault.SetScoped(ctx, ScopeUserAgent, userID, agentID, sourceTokenName, "private-token"); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(svc.pool, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	resources := plugin.NewResourceStore(manager)
	resourceKey := plugin.ResourceKey{Scope: plugin.ScopeUserAgent, UserID: userID, AgentID: agentID, Kind: plugin.ResourceMCP, Name: "private-bearer"}
	data, err := json.Marshal(mcpconfig.Declaration{URL: "https://bearer.example", Transport: TransportStreamableHTTP, Authentication: mcpconfig.Authentication{Type: AuthTypeBearer, Mode: CredentialModePerUser, CredentialRef: targetTokenName}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resources.WriteMCP(ctx, resourceKey, "", data); err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := resources.Get(ctx, resourceKey)
	if err != nil {
		t.Fatal(err)
	}
	target, err := RegistrationFromFileResource(resource, "", authority)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := svc.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MigrateLegacyFileCredentials(ctx, tx, resources, []plugin.MCPMigrationEvidence{{OldConfigID: configID, OldServerKey: "main", NewResource: resourceKey}}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := svc.vault.GetScoped(ctx, ScopeUser, userID, "", targetTokenName)
	if err != nil || got != "private-token" {
		t.Fatalf("migrated private bearer token=%q err=%v", got, err)
	}
	ready, err := svc.FileCredentialReady(ctx, target, authority)
	if err != nil || !ready {
		t.Fatalf("private bearer runtime ready=%v err=%v", ready, err)
	}
}

func TestMigrateLegacySystemAgentSharedOAuthRealPGVault(t *testing.T) {
	svc, _, _, agentID := setupInternal(t)
	ctx := t.Context()
	configID := uuid.NewString()
	childID := uuid.NewString()
	pluginID := "legacy-system-agent-" + configID[:8]
	clientID := "system-agent-client"
	sourceBundleName := oauthBundleName(childID)
	targetBundleName := "MCP_FILE_SYSTEM_AGENT_BUNDLE_TARGET"
	spec, err := plugin.PublishDefinitionSpec([]byte(`{"origin":"remote_mcp","mcp_servers":{"main":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"mcp_servers":{"main":{"url":"https://system-agent.example","transport":"streamable_http","auth_type":"oauth","credential_mode":"shared","metadata":{"oauth":{"client_id":"system-agent-client"}}}}}`)
	refs := json.RawMessage(`{"mcp_servers":{"main":{"oauth_bundle":{"name":"` + sourceBundleName + `","mode":"shared","scope":"system_agent","user_id":"","agent_id":"` + agentID + `"}}}}`)
	if _, err := svc.pool.Exec(ctx, `INSERT INTO plugin_definition(id,display_name,source,spec,default_enabled,revision,creator_user_id) VALUES($1,'System agent migration','custom',$2::jsonb,false,1,NULL)`, pluginID, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pool.Exec(ctx, `INSERT INTO plugin_config(id,plugin_id,scope,agent_id,enabled,config,credential_refs,revision) VALUES($1,$2,'system_agent',$3,true,$4::jsonb,$5::jsonb,1)`, configID, pluginID, agentID, payload, refs); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pool.Exec(ctx, `INSERT INTO plugin_config_mcp_server(id,config_id,server_key) VALUES($1,$2,'main')`, childID, configID); err != nil {
		t.Fatal(err)
	}
	bundle := OAuthBundle{Version: 1, ClientID: clientID, TokenEndpoint: "https://system-agent.example/token", AccessToken: "system-agent-token", RefreshToken: "system-agent-refresh", AccessExpiresAt: time.Now().UTC().Add(time.Hour)}
	rawBundle, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.vault.SetSystemScoped(ctx, ScopeSystemAgent, agentID, sourceBundleName, string(rawBundle)); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(svc.pool, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	resources := plugin.NewResourceStore(manager)
	resourceKey := plugin.ResourceKey{Scope: plugin.ScopeSystemAgent, AgentID: agentID, Kind: plugin.ResourceMCP, Name: "system-agent-oauth"}
	data, err := json.Marshal(mcpconfig.Declaration{URL: "https://system-agent.example", Transport: TransportStreamableHTTP, Authentication: mcpconfig.Authentication{Type: AuthTypeOAuth, Mode: CredentialModeShared, ClientID: clientID, CredentialRef: targetBundleName}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resources.WriteMCP(ctx, resourceKey, "", data); err != nil {
		t.Fatal(err)
	}
	resource, err := resources.Get(ctx, resourceKey)
	if err != nil {
		t.Fatal(err)
	}
	target, err := registrationFromFileResource(resource, "")
	if err != nil {
		t.Fatalf("migration-only system-agent projection: %v", err)
	}
	tx, err := svc.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MigrateLegacyFileCredentials(ctx, tx, resources, []plugin.MCPMigrationEvidence{{OldConfigID: configID, OldServerKey: "main", NewResource: resourceKey}}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	gotRaw, err := svc.vault.GetScoped(ctx, ScopeSystemAgent, "", agentID, fileGrantName(target, CredentialOwner{Scope: ScopeSystemAgent, AgentID: agentID}))
	if err != nil {
		t.Fatal(err)
	}
	var got fileGrant
	if err := json.Unmarshal([]byte(gotRaw), &got); err != nil || got.Bundle == nil || got.Bundle.AccessToken != bundle.AccessToken || got.Generation == "" || got.Revoked {
		t.Fatalf("system-agent target grant=%s parsed=%+v err=%v", gotRaw, got, err)
	}
}

func newMigrationUser(t *testing.T, svc *Service, email string) string {
	t.Helper()
	store := appdb.NewOIDCStore(svc.pool)
	user, err := store.CreateUser(t.Context(), auth.User{ID: uuid.NewString(), Email: email, Name: email})
	if err != nil {
		t.Fatal(err)
	}
	vaultSvc := svc.vault.(*vault.Service)
	pub, encPriv, err := vault.GenerateUserKeys(vaultSvc.MasterRecipient())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserAgeKeys(t.Context(), user.ID, pub, encPriv); err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func setMigrationVault(ctx context.Context, svc *Service, owner CredentialOwner, name, value string) error {
	if IsSystemScope(owner.Scope) {
		return svc.vault.SetSystemScoped(ctx, owner.Scope, owner.AgentID, name, value)
	}
	return svc.vault.SetScoped(ctx, owner.Scope, owner.UserID, owner.AgentID, name, value)
}
