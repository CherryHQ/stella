package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
)

// TestComposableChildCRUDUsesChildIdentity exercises the real plugin service,
// PostgreSQL schema, and age-backed Vault through the composable child API.
// Parent revisions are the only CAS token; child UUIDs own observations and
// credential namespaces.
func TestComposableChildCRUDUsesChildIdentity(t *testing.T) {
	svc, _, userID, _ := setupInternal(t)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAuthority(context.Background(), authority)

	def, parent, err := svc.CreateCustom(ctx, plugin.Definition{
		ID: "composable-child-crud", DisplayName: "Composable Child CRUD", Spec: mustPublishedMCPTestSpec(`{"description":"child CRUD integration"}`),
	}, CreateInput{
		Scope: ScopeUser, Name: "Composable Child CRUD", URL: "https://main.example.test",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeNone,
	})
	if err != nil {
		t.Fatalf("CreateCustom: %v", err)
	}
	if def.ID == "" || parent.ID == "" || len(parent.MCPServers) != 1 || parent.MCPServers[0].ServerKey != "main" {
		t.Fatalf("custom parent = %#v, want one main child", parent)
	}
	main := parent.MCPServers[0]
	if main.ID == parent.ID || main.ParentConfigID != parent.ID {
		t.Fatalf("main identity = %#v, parent %q", main, parent.ID)
	}

	access, err := NewAccess(svc, nil, nil).Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	child, err := access.CreateChild(ctx, parent.ID, "secondary", parent.Revision, CreateInput{
		URL: "https://secondary.example.test", Transport: TransportStreamableHTTP,
		AuthType: AuthTypeBearer, Token: "secondary-token",
	})
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if child.ID == main.ID || child.ID == parent.ID || child.ParentConfigID != parent.ID || child.ServerKey != "secondary" {
		t.Fatalf("secondary identity = %#v, main=%q parent=%q", child, main.ID, parent.ID)
	}
	if child.AuthType != AuthTypeBearer || child.CredentialRef != credentialName(child.ID) {
		t.Fatalf("secondary auth projection = %#v", child)
	}
	if got, err := svc.vault.GetScoped(ctx, ScopeUser, userID, "", credentialName(child.ID)); err != nil || got != "secondary-token" {
		t.Fatalf("secondary child credential = %q, err=%v", got, err)
	}
	if got, err := svc.vault.GetScoped(ctx, ScopeUser, userID, "", credentialName(main.ID)); err == nil || got != "" {
		t.Fatalf("main child unexpectedly has bearer credential %q, err=%v", got, err)
	}
	var childRefCount int
	if err := svc.pool.QueryRow(ctx, `
		SELECT count(*) FROM vault_entry WHERE scope = $1 AND user_id = $2::uuid AND name = $3
	`, ScopeUser, userID, credentialName(child.ID)).Scan(&childRefCount); err != nil {
		t.Fatal(err)
	}
	if childRefCount != 1 {
		t.Fatalf("secondary child vault rows = %d, want 1", childRefCount)
	}

	none := AuthTypeNone
	updated, err := access.UpdateChild(ctx, child.ID, child.ConfigRevision, UpdateInput{AuthType: &none})
	if err != nil {
		t.Fatalf("UpdateChild bearer to none: %v", err)
	}
	if updated.AuthType != AuthTypeNone || updated.CredentialRef != "" {
		t.Fatalf("updated child auth projection = %#v", updated)
	}
	if got, err := svc.vault.GetScoped(ctx, ScopeUser, userID, "", credentialName(child.ID)); err == nil || got != "" {
		t.Fatalf("bearer credential survived auth-none update: %q, err=%v", got, err)
	}
	if err := svc.pool.QueryRow(ctx, `SELECT count(*) FROM vault_entry WHERE name = $1`, credentialName(child.ID)).Scan(&childRefCount); err != nil {
		t.Fatal(err)
	}
	if childRefCount != 0 {
		t.Fatalf("updated child vault rows = %d, want 0", childRefCount)
	}

	secondBearer, err := access.CreateChild(ctx, parent.ID, "third", updated.ConfigRevision, CreateInput{
		URL: "https://third.example.test", Transport: TransportStreamableHTTP,
		AuthType: AuthTypeBearer, Token: "third-token",
	})
	if err != nil {
		t.Fatalf("CreateChild third: %v", err)
	}
	staleRevision := secondBearer.ConfigRevision - 1
	if err := access.DeleteChild(ctx, secondBearer.ID, staleRevision); !errors.Is(err, plugin.ErrConflict) {
		t.Fatalf("stale DeleteChild = %v, want conflict", err)
	}
	if err := access.DeleteChild(ctx, secondBearer.ID, secondBearer.ConfigRevision); err != nil {
		t.Fatalf("DeleteChild third: %v", err)
	}
	if got, err := svc.vault.GetScoped(ctx, ScopeUser, userID, "", credentialName(secondBearer.ID)); err == nil || got != "" {
		t.Fatalf("deleted child credential survived: %q, err=%v", got, err)
	}
	children, err := access.ListChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListChildren after target delete: %v", err)
	}
	if len(children) != 2 || children[0].ID == secondBearer.ID || children[1].ID == secondBearer.ID {
		t.Fatalf("siblings after target delete = %#v, want main and secondary", children)
	}

	if err := access.DeleteChild(ctx, main.ID, secondBearer.ConfigRevision); !errors.Is(err, plugin.ErrConflict) {
		t.Fatalf("stale sibling DeleteChild = %v, want conflict", err)
	}
	if err := access.DeleteChild(ctx, child.ID, secondBearer.ConfigRevision+1); err != nil {
		t.Fatalf("DeleteChild secondary: %v", err)
	}
	children, err = access.ListChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListChildren after secondary delete: %v", err)
	}
	if len(children) != 1 || children[0].ID != main.ID {
		t.Fatalf("remaining sibling = %#v, want main %q", children, main.ID)
	}

	if err := access.DeleteChild(ctx, main.ID, secondBearer.ConfigRevision+2); err != nil {
		t.Fatalf("DeleteChild final child: %v", err)
	}
	children, err = access.ListChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListChildren after final child delete: %v", err)
	}
	if len(children) != 0 {
		t.Fatalf("final child delete left children = %#v", children)
	}
	replacement, err := access.CreateChild(ctx, parent.ID, "replacement", secondBearer.ConfigRevision+3, CreateInput{URL: "https://replacement.example.test", AuthType: AuthTypeNone})
	if err != nil {
		t.Fatalf("create child after deleting all children: %v", err)
	}
	if replacement.ID == main.ID || replacement.ID == child.ID {
		t.Fatal("replacement reused a deleted credential namespace")
	}
	children, err = access.ListChildren(ctx, parent.ID)
	if err != nil || len(children) != 1 || children[0].ID != replacement.ID {
		t.Fatalf("replacement children = %#v, %v", children, err)
	}
}

func TestComposableChildCredentialFamiliesAndParentDelete(t *testing.T) {
	svc, _, userID, _ := setupInternal(t)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAuthority(context.Background(), authority)

	def, parent, err := svc.CreateCustom(ctx, plugin.Definition{
		ID: "composable-child-credentials", DisplayName: "Composable Child Credentials", Spec: mustPublishedMCPTestSpec(`{"description":"credential family integration"}`),
	}, CreateInput{
		Scope: ScopeUser, Name: "Composable Child Credentials", URL: "https://main.example.test",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeNone,
	})
	if err != nil {
		t.Fatalf("CreateCustom parent: %v", err)
	}
	access, err := NewAccess(svc, nil, nil).Begin(authority)
	if err != nil {
		t.Fatal(err)
	}

	oauthChild, err := access.CreateChild(ctx, parent.ID, "oauth", parent.Revision, CreateInput{
		URL: "https://oauth.example.test", Transport: TransportStreamableHTTP,
		AuthType: AuthTypeOAuth, OAuthClientID: "oauth-client-a", OAuthClientSecret: "oauth-secret-a",
	})
	if err != nil {
		t.Fatalf("CreateChild OAuth: %v", err)
	}
	bearerChild, err := access.CreateChild(ctx, parent.ID, "bearer", oauthChild.ConfigRevision, CreateInput{
		URL: "https://bearer.example.test", Transport: TransportStreamableHTTP,
		AuthType: AuthTypeBearer, Token: "bearer-token",
	})
	if err != nil {
		t.Fatalf("CreateChild bearer: %v", err)
	}
	if oauthChild.ID == bearerChild.ID || oauthChild.ParentConfigID != bearerChild.ParentConfigID || oauthChild.ParentConfigID != parent.ID {
		t.Fatalf("child identities = oauth %#v bearer %#v parent %q", oauthChild, bearerChild, parent.ID)
	}

	// Seed the OAuth bundle and an old bearer locator for the OAuth child. The
	// parent delete must revoke all reserved families even when refs are stale.
	for name, value := range map[string]string{
		oauthBundleName(oauthChild.ID):       "oauth-bundle-a",
		credentialName(oauthChild.ID):        "stale-bearer-a",
		oauthClientSecretName(oauthChild.ID): "oauth-secret-a",
	} {
		if err := svc.vault.SetScoped(ctx, ScopeUser, userID, "", name, value); err != nil {
			t.Fatalf("seed OAuth child credential %s: %v", name, err)
		}
	}
	assertChildCredentialFamilies := func(childID string, want int) {
		t.Helper()
		var got int
		if err := svc.pool.QueryRow(ctx, `
			SELECT count(*) FROM vault_entry
			WHERE scope = $1 AND user_id = $2::uuid
			  AND name IN ($3, $4, $5)
		`, ScopeUser, userID, credentialName(childID), oauthBundleName(childID), oauthClientSecretName(childID)).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("child %s credential families = %d, want %d", childID, got, want)
		}
	}
	assertChildCredentialFamilies(oauthChild.ID, 3)
	assertChildCredentialFamilies(bearerChild.ID, 1)

	oauth := AuthTypeOAuth
	clientID := "oauth-client-b"
	clientSecret := "oauth-secret-b"
	updated, err := access.UpdateChild(ctx, bearerChild.ID, bearerChild.ConfigRevision, UpdateInput{
		AuthType: &oauth, OAuthClientID: &clientID, OAuthClientSecret: &clientSecret,
	})
	if err != nil {
		t.Fatalf("UpdateChild bearer to OAuth: %v", err)
	}
	if updated.AuthType != AuthTypeOAuth || updated.OAuthClientID != clientID || updated.OAuthClientSecretRef != oauthClientSecretName(bearerChild.ID) {
		t.Fatalf("bearer to OAuth projection = %#v", updated)
	}
	assertChildCredentialFamilies(oauthChild.ID, 3)
	assertChildCredentialFamilies(bearerChild.ID, 1)
	if got, err := svc.vault.GetScoped(ctx, ScopeUser, userID, "", oauthClientSecretName(bearerChild.ID)); err != nil || got != clientSecret {
		t.Fatalf("new OAuth client secret = %q, err=%v", got, err)
	}
	if got, err := svc.vault.GetScoped(ctx, ScopeUser, userID, "", credentialName(bearerChild.ID)); err == nil || got != "" {
		t.Fatalf("old bearer credential survived OAuth transition: %q, err=%v", got, err)
	}
	if err := svc.vault.SetScoped(ctx, ScopeUser, userID, "", oauthBundleName(bearerChild.ID), "oauth-bundle-b"); err != nil {
		t.Fatalf("seed transitioned OAuth bundle: %v", err)
	}

	none := AuthTypeNone
	updatedNone, err := access.UpdateChild(ctx, bearerChild.ID, updated.ConfigRevision, UpdateInput{AuthType: &none})
	if err != nil {
		t.Fatalf("UpdateChild OAuth to none: %v", err)
	}
	if updatedNone.AuthType != AuthTypeNone || updatedNone.CredentialRef != "" {
		t.Fatalf("OAuth to none projection = %#v", updatedNone)
	}
	assertChildCredentialFamilies(oauthChild.ID, 3)
	assertChildCredentialFamilies(bearerChild.ID, 0)

	otherDef, otherParent, err := svc.CreateCustom(ctx, plugin.Definition{
		ID: "composable-child-other", DisplayName: "Composable Child Other", Spec: mustPublishedMCPTestSpec(`{"description":"other parent"}`),
	}, CreateInput{
		Scope: ScopeUser, Name: "Composable Child Other", URL: "https://other.example.test",
		Transport: TransportStreamableHTTP, AuthType: AuthTypeBearer, Token: "other-parent-token",
	})
	if err != nil {
		t.Fatalf("CreateCustom other parent: %v", err)
	}
	if otherDef.ID == def.ID || len(otherParent.MCPServers) != 1 {
		t.Fatalf("other parent identity = %#v", otherParent)
	}
	otherChildID := otherParent.MCPServers[0].ID

	pluginAccess, err := svc.plugins.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	current, err := pluginAccess.GetConfig(ctx, def.ID, parent.ID)
	if err != nil {
		t.Fatalf("read parent before DeleteConfig: %v", err)
	}
	if err := pluginAccess.DeleteConfig(ctx, def.ID, parent.ID, current.Revision); err != nil {
		t.Fatalf("DeleteConfig parent: %v", err)
	}
	var parentRows, childRows int
	if err := svc.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_config WHERE id = $1::uuid`, parent.ID).Scan(&parentRows); err != nil {
		t.Fatal(err)
	}
	if err := svc.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_config_mcp_server WHERE config_id = $1::uuid`, parent.ID).Scan(&childRows); err != nil {
		t.Fatal(err)
	}
	if parentRows != 0 || childRows != 0 {
		t.Fatalf("deleted parent rows = config %d/children %d, want 0/0", parentRows, childRows)
	}
	assertChildCredentialFamilies(oauthChild.ID, 0)
	assertChildCredentialFamilies(bearerChild.ID, 0)
	var otherRows int
	if err := svc.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_config_mcp_server WHERE id = $1::uuid`, otherChildID).Scan(&otherRows); err != nil {
		t.Fatal(err)
	}
	if otherRows != 1 {
		t.Fatalf("other parent child rows = %d, want 1", otherRows)
	}
	if got, err := svc.vault.GetScoped(ctx, ScopeUser, userID, "", credentialName(otherChildID)); err != nil || got != "other-parent-token" {
		t.Fatalf("other parent credential = %q, err=%v", got, err)
	}
}

func TestComposableBuiltinChildrenUseInheritedPayload(t *testing.T) {
	for _, operation := range []string{"create", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			svc, _, userID, _ := setupInternal(t)
			authority, err := authz.NewUserAuthority(authz.UserID(userID), true)
			if err != nil {
				t.Fatal(err)
			}
			ctx := authz.WithAuthority(t.Context(), authority)
			def := plugin.Definition{ID: "inherited-mcp", DisplayName: "Inherited MCP", Source: plugin.SourceBuiltin, Revision: 1, DefaultEnabled: true, Spec: mustPublishedMCPTestSpec(`{"mcp_servers":{"main":{"url":"https://main.example.test","transport":"streamable_http","auth_type":"none"},"search":{"url":"https://search.example.test","transport":"streamable_http","auth_type":"none"}}}`)}
			catalog := plugin.NewCatalog()
			if err := catalog.Register(def); err != nil {
				t.Fatal(err)
			}
			plugins := plugin.NewService(svc.pool, nil, catalog, NewMCPBackendPolicy(EndpointPolicy{AllowPrivate: true}), func(_ context.Context, fn func() error) error { return fn() })
			svc.SetPluginService(plugins)
			if err := plugins.SyncBuiltinDefaults(ctx); err != nil {
				t.Fatal(err)
			}
			pluginAccess, err := plugins.Begin(authority)
			if err != nil {
				t.Fatal(err)
			}
			configs, err := pluginAccess.ListConfigs(ctx, def.ID, plugin.ScopeSystem, "")
			if err != nil || len(configs) != 1 {
				t.Fatalf("configs=%#v err=%v", configs, err)
			}
			cfg := configs[0]
			if string(cfg.Payload) != "{}" {
				t.Fatalf("fixture must inherit resources: %s", cfg.Payload)
			}
			access, err := NewAccess(svc, nil, nil).Begin(authority)
			if err != nil {
				t.Fatal(err)
			}
			children, err := access.ListChildren(ctx, cfg.ID)
			if err != nil || len(children) != 2 {
				t.Fatalf("children=%#v err=%v", children, err)
			}
			main, search := children[0], children[1]
			if main.ServerKey != "main" {
				main, search = search, main
			}
			wantCount := 2
			switch operation {
			case "create":
				_, err = access.CreateChild(ctx, cfg.ID, "extra", cfg.Revision, CreateInput{URL: "https://extra.example.test", AuthType: AuthTypeNone})
				if !errors.Is(err, authz.ErrForbidden) {
					t.Fatalf("create fixed package child = %v, want forbidden", err)
				}
				err = nil
				wantCount = 2
			case "update":
				url := "https://updated.example.test"
				_, err = access.UpdateChild(ctx, main.ID, cfg.Revision, UpdateInput{URL: &url})
			case "delete":
				err = access.DeleteChild(ctx, main.ID, cfg.Revision)
				if !errors.Is(err, authz.ErrForbidden) {
					t.Fatalf("delete fixed package child = %v, want forbidden", err)
				}
				err = nil
				wantCount = 2
			}
			if err != nil {
				t.Fatalf("%s inherited child: %v", operation, err)
			}
			children, err = access.ListChildren(ctx, cfg.ID)
			if err != nil || len(children) != wantCount {
				t.Fatalf("after %s children=%#v err=%v", operation, children, err)
			}
			siblingPreserved := false
			for _, child := range children {
				if child.ID == search.ID && child.URL == search.URL {
					siblingPreserved = true
				}
			}
			if !siblingPreserved {
				t.Fatalf("%s lost inherited sibling: %#v", operation, children)
			}
		})
	}
}

func TestComposableChildDescriptionPreservesBearer(t *testing.T) {
	svc, _, userID, _ := setupInternal(t)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAuthority(t.Context(), authority)
	_, parent, err := svc.CreateCustom(ctx, plugin.Definition{ID: "description-mcp", DisplayName: "Description MCP", Spec: mustPublishedMCPTestSpec(`{}`)}, CreateInput{Scope: ScopeUser, Name: "Description MCP", URL: "https://example.test", AuthType: AuthTypeBearer, Transport: TransportStreamableHTTP, Token: "keep-this-token"})
	if err != nil {
		t.Fatal(err)
	}
	child := parent.MCPServers[0]
	access, err := NewAccess(svc, nil, nil).Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	description := "A new description"
	if _, err := access.UpdateChild(ctx, child.ID, parent.Revision, UpdateInput{Description: &description}); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.vault.GetScoped(ctx, ScopeUser, userID, "", credentialName(child.ID)); err != nil || got != "keep-this-token" {
		t.Fatalf("description edit removed bearer: value=%q err=%v", got, err)
	}
}

func TestComposableChildPermitRejectsSiblingMutation(t *testing.T) {
	svc, _, userID, _ := setupInternal(t)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAuthority(t.Context(), authority)
	_, parent, err := svc.CreateCustom(ctx, plugin.Definition{ID: "permit-mcp", DisplayName: "Permit MCP", Spec: mustPublishedMCPTestSpec(`{}`)}, CreateInput{Scope: ScopeUser, Name: "Permit MCP", URL: "https://main.example.test", AuthType: AuthTypeNone, Transport: TransportStreamableHTTP})
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewAccess(svc, nil, nil).Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.CreateChild(ctx, parent.ID, "search", parent.Revision, CreateInput{URL: "https://search.example.test", AuthType: AuthTypeNone}); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := access.parentConfig(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(cfg.Payload, &object); err != nil {
		t.Fatal(err)
	}
	servers := object["mcp_servers"].(map[string]any)
	servers["search"].(map[string]any)["url"] = "https://changed.example.test"
	payload, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	err = svc.withPluginMutation(ctx, authority, func(mutationCtx context.Context, bound *plugin.Access, tx pgx.Tx) error {
		_, err := updateCredentialChildConfig(mutationCtx, bound, tx, cfg, parent.MCPServers[0].ID, false, plugin.ConfigPatch{PayloadSet: true, Payload: payload})
		return err
	})
	if !errors.Is(err, plugin.ErrInvalidConfig) {
		t.Fatalf("main-only permit changed search: %v", err)
	}
	after, _, err := access.parentConfig(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != cfg.Revision || string(after.Payload) != string(cfg.Payload) {
		t.Fatalf("rejected sibling mutation did not roll back")
	}
}

func TestComposableAddChildToDisabledEmptyScope(t *testing.T) {
	svc, _, userID, _ := setupInternal(t)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authz.WithAuthority(t.Context(), authority)
	def := plugin.Definition{ID: "disabled-scope-mcp", DisplayName: "Disabled scope MCP", Source: plugin.SourceBuiltin, Revision: 1, DefaultEnabled: true, Spec: mustPublishedMCPTestSpec(`{"mcp_servers":{"main":{"url":"https://main.example.test","transport":"streamable_http","auth_type":"none"}}}`)}
	catalog := plugin.NewCatalog()
	if err := catalog.Register(def); err != nil {
		t.Fatal(err)
	}
	plugins := plugin.NewService(svc.pool, nil, catalog, NewMCPBackendPolicy(EndpointPolicy{AllowPrivate: true}), func(_ context.Context, fn func() error) error { return fn() })
	svc.SetPluginService(plugins)
	if err := plugins.SyncBuiltinDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	pluginAccess, err := plugins.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	cfg, err := pluginAccess.CreateConfig(ctx, plugin.Config{PluginID: def.ID, Scope: plugin.ScopeUser, Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Payload) != 0 || len(cfg.MCPServers) != 0 {
		t.Fatalf("fixture must be an empty negative config: %#v", cfg)
	}
	access, err := NewAccess(svc, nil, nil).Begin(authority)
	if err != nil {
		t.Fatal(err)
	}
	child, err := access.CreateChild(ctx, cfg.ID, "extra", cfg.Revision, CreateInput{URL: "https://extra.example.test", AuthType: AuthTypeNone})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("create child in fixed package = %v, want forbidden", err)
	}
	if child.ID != "" {
		t.Fatalf("forbidden child returned identity: %#v", child)
	}
	children, err := access.ListChildren(ctx, cfg.ID)
	if err != nil || len(children) != 0 {
		t.Fatalf("negative config inherited fixed children: %#v, %v", children, err)
	}
	enabled := true
	if _, err := pluginAccess.UpdateConfig(ctx, def.ID, cfg.ID, cfg.Revision, plugin.ConfigPatch{EnabledSet: true, Enabled: &enabled}); !errors.Is(err, plugin.ErrInvalidConfig) {
		t.Fatalf("enable empty fixed package = %v, want invalid config", err)
	}
}

func TestMCPExecutionIdentitiesEmptyResources(t *testing.T) {
	for _, raw := range []string{`{}`, `{"prompt":"CLI guidance"}`, `{"mcp_servers":{}}`} {
		t.Run(raw, func(t *testing.T) {
			identities, err := mcpExecutionIdentities(plugin.Definition{Spec: mustPublishedMCPTestSpec(`{}`)}, plugin.Config{Payload: []byte(raw)})
			if err != nil || len(identities) != 0 {
				t.Fatalf("empty resources invented identities: %#v, %v", identities, err)
			}
		})
	}
}
