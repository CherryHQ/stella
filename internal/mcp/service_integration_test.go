package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// vaultDB adapts OIDCStore (auth_user age keys) + sqlc.Queries (vault_entry) to
// the vault.DB interface, mirroring the vault package's own test harness.
type vaultDB struct {
	oidc *appdb.OIDCStore
	q    *sqlc.Queries
}

func (d *vaultDB) GetVaultUser(ctx context.Context, id string) (sqlc.VaultUser, error) {
	u, err := d.oidc.GetUser(ctx, id)
	if err != nil {
		return sqlc.VaultUser{}, err
	}
	return sqlc.VaultUser{AgePublicKey: u.AgePublicKey, AgePrivateKey: u.AgePrivateKey}, nil
}

func (d *vaultDB) GetVaultEntryByScope(ctx context.Context, arg sqlc.GetVaultEntryByScopeParams) (sqlc.VaultEntry, error) {
	return d.q.GetVaultEntryByScope(ctx, arg)
}

func (d *vaultDB) ListVaultEntriesByScope(ctx context.Context, arg sqlc.ListVaultEntriesByScopeParams) ([]sqlc.VaultEntry, error) {
	return d.q.ListVaultEntriesByScope(ctx, arg)
}

func (d *vaultDB) ListVaultEntriesForRuntime(ctx context.Context, arg sqlc.ListVaultEntriesForRuntimeParams) ([]sqlc.VaultEntry, error) {
	return d.q.ListVaultEntriesForRuntime(ctx, arg)
}

func (d *vaultDB) UpsertVaultEntryByScope(ctx context.Context, arg sqlc.UpsertVaultEntryByScopeParams) (sqlc.VaultEntry, error) {
	return d.q.UpsertVaultEntryByScope(ctx, arg)
}

func (d *vaultDB) DeleteVaultEntryByScope(ctx context.Context, arg sqlc.DeleteVaultEntryByScopeParams) error {
	return d.q.DeleteVaultEntryByScope(ctx, arg)
}

// setup builds an MCP service backed by a real database and vault, plus a
// provisioned user and agent so all four scopes are usable.
func setup(t *testing.T) (svc *mcp.Service, q *sqlc.Queries, userID, agentID string) {
	t.Helper()
	db := dbtest.New(t)
	oidc := appdb.NewOIDCStore(db)
	q = sqlc.New(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("master identity: %v", err)
	}
	vaultSvc, err := vault.NewService(&vaultDB{oidc: oidc, q: q}, masterID.String(), nil)
	if err != nil {
		t.Fatalf("vault.NewService: %v", err)
	}

	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "u@mcp.test", Name: "U"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	pub, encPriv, err := vault.GenerateUserKeys(vaultSvc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if err := oidc.UpdateUserAgeKeys(ctx, user.ID, pub, encPriv); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}

	agent, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "mcp-test-agent", Name: "Agent", Workspace: "/tmp/agent",
		Sandbox: json.RawMessage(`{}`),
		Scope:   "system", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	return mcp.NewService(q, vaultSvc), q, user.ID, agent.ID
}

// TestCredentialEncryptedAtRest proves the bearer token is stored age-encrypted
// in vault_entry (unreadable at rest) and never lands in the mcp_server row.
func TestCredentialEncryptedAtRest(t *testing.T) {
	svc, q, userID, _ := setup(t)
	ctx := context.Background()
	const token = "ghp_super_secret_value_1234567890"

	reg, err := svc.Create(ctx, mcp.CreateInput{
		Scope: mcp.ScopeUser, UserID: userID, Name: "gh", URL: "https://mcp.example.com",
		Transport: mcp.TransportStreamableHTTP, AuthType: mcp.AuthTypeBearer, Token: token,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The registration row must carry only a reference, no secret.
	row, err := q.GetMCPServerByID(ctx, reg.ID)
	if err != nil {
		t.Fatalf("GetMCPServerByID: %v", err)
	}
	if row.CredentialRef == "" || row.CredentialRef == token {
		t.Fatalf("row credential_ref = %q; must reference the vault, not hold the token", row.CredentialRef)
	}

	// The vault ciphertext must not reveal the token.
	entry, err := q.GetVaultEntryByScope(ctx, sqlc.GetVaultEntryByScopeParams{
		Scope: mcp.ScopeUser, UserID: pgnull.Text(userID), Name: reg.CredentialRef,
	})
	if err != nil {
		t.Fatalf("GetVaultEntryByScope: %v", err)
	}
	if strings.Contains(entry.Ciphertext, token) {
		t.Fatal("ciphertext leaks the plaintext token")
	}
	if !strings.Contains(entry.Ciphertext, "BEGIN AGE ENCRYPTED FILE") {
		t.Fatalf("ciphertext is not age-armored: %q", entry.Ciphertext)
	}

	// The service still decrypts it back for connecting.
	got, err := svc.BearerToken(ctx, reg)
	if err != nil {
		t.Fatalf("BearerToken: %v", err)
	}
	if got != token {
		t.Fatalf("BearerToken = %q, want %q", got, token)
	}
}

// TestUpdateRedactsMalformedLegacyEndpoint verifies that a metadata-only
// Settings update cannot surface a raw legacy URL when validation reuses it.
// mcp_server predates URL validation, so the row is inserted through SQL to
// model a deployment that already contains malformed, secret-bearing data.
func TestUpdateRedactsMalformedLegacyEndpoint(t *testing.T) {
	svc, q, userID, _ := setup(t)
	ctx := context.Background()
	const raw = "https://legacy-user:legacy-pass@example.test/%zz?token=legacy-query#legacy-fragment"
	legacyID := uuid.NewString()
	if _, err := q.CreateMCPServer(ctx, sqlc.CreateMCPServerParams{
		ID: legacyID, Scope: mcp.ScopeUser, UserID: pgnull.Text(userID), Name: "legacy",
		Url: raw, Transport: mcp.TransportStreamableHTTP, AuthType: mcp.AuthTypeNone, Enabled: true, Metadata: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("seed legacy registration: %v", err)
	}
	legacy, err := svc.Get(ctx, legacyID, mcp.ScopeUser, userID, "")
	if err != nil {
		t.Fatalf("read legacy registration: %v", err)
	}

	var updateSpec mcp.SettingsMcpActionTool
	for _, spec := range mcp.SettingsMcpActionTools() {
		if spec.Action == "update" {
			updateSpec = spec
			break
		}
	}
	if updateSpec.Name == "" {
		t.Fatal("mcp update tool spec not found")
	}
	access := mcp.NewAccess(svc, nil, nil)
	tool := mcp.NewManagementTool(updateSpec, func() *mcp.Access { return access })
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	ctx = authz.WithAuthority(authz.WithUserID(ctx, userID), authority)
	_, err = tool.Execute(ctx, map[string]any{
		"id": legacyID, "expected_version": legacy.Version(), "enabled": false,
	})
	if err == nil {
		t.Fatal("tool update with malformed legacy URL succeeded")
	}
	got := err.Error()
	for _, secret := range []string{"legacy-user", "legacy-pass", "legacy-query", "legacy-fragment", raw} {
		if strings.Contains(got, secret) {
			t.Fatalf("update error leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "mcp: invalid endpoint url: malformed URL") {
		t.Fatalf("update error lost safe validation diagnosis: %s", got)
	}
}

// TestScopeVisibilityPrecedence proves the DB query resolves the effective
// server per name as user_agent > user > system_agent > system.
func TestScopeVisibilityPrecedence(t *testing.T) {
	svc, _, userID, agentID := setup(t)
	ctx := context.Background()

	// Four registrations named "gh", one per scope.
	mustCreate(t, svc, mcp.CreateInput{Scope: mcp.ScopeSystem, Name: "gh", URL: "http://system"})
	mustCreate(t, svc, mcp.CreateInput{Scope: mcp.ScopeSystemAgent, AgentID: agentID, Name: "gh", URL: "http://system_agent"})
	mustCreate(t, svc, mcp.CreateInput{Scope: mcp.ScopeUser, UserID: userID, Name: "gh", URL: "http://user"})
	mustCreate(t, svc, mcp.CreateInput{Scope: mcp.ScopeUserAgent, UserID: userID, AgentID: agentID, Name: "gh", URL: "http://user_agent"})
	// A system-only server visible to everyone.
	mustCreate(t, svc, mcp.CreateInput{Scope: mcp.ScopeSystem, Name: "search", URL: "http://search"})

	// Full context: the most specific "gh" wins.
	regs, err := svc.ResolveForContext(ctx, userID, agentID)
	if err != nil {
		t.Fatalf("ResolveForContext: %v", err)
	}
	got := byName(regs)
	if len(got) != 2 {
		t.Fatalf("want 2 effective servers, got %d (%v)", len(got), urls(regs))
	}
	if got["gh"].URL != "http://user_agent" {
		t.Fatalf("gh resolved to %q, want http://user_agent", got["gh"].URL)
	}
	if got["search"].URL != "http://search" {
		t.Fatalf("search resolved to %q", got["search"].URL)
	}

	// User context without an agent: user scope wins over system.
	regs, err = svc.ResolveForContext(ctx, userID, "")
	if err != nil {
		t.Fatalf("ResolveForContext (no agent): %v", err)
	}
	if byName(regs)["gh"].URL != "http://user" {
		t.Fatalf("no-agent gh = %q, want http://user", byName(regs)["gh"].URL)
	}

	// Anonymous context: only system servers are visible.
	regs, err = svc.ResolveForContext(ctx, "", "")
	if err != nil {
		t.Fatalf("ResolveForContext (anon): %v", err)
	}
	if byName(regs)["gh"].URL != "http://system" {
		t.Fatalf("anon gh = %q, want http://system", byName(regs)["gh"].URL)
	}
}

func mustCreate(t *testing.T, svc *mcp.Service, in mcp.CreateInput) {
	t.Helper()
	if in.URL == "" {
		in.URL = "http://x"
	}
	if _, err := svc.Create(context.Background(), in); err != nil {
		t.Fatalf("Create(%s): %v", in.Scope, err)
	}
}

func byName(regs []mcp.Registration) map[string]mcp.Registration {
	m := make(map[string]mcp.Registration, len(regs))
	for _, r := range regs {
		m[r.Name] = r
	}
	return m
}

func urls(regs []mcp.Registration) []string {
	out := make([]string, len(regs))
	for i, r := range regs {
		out[i] = r.Scope + ":" + r.URL
	}
	return out
}

func namePtr(s string) *string { return &s }

// TestRenameMigratesToolOverrides proves a registration rename rewrites every
// tool_override row with the old namespaced prefix, including other owners'
// rows on a system server, and never bleeds into a longer server name.
func TestRenameMigratesToolOverrides(t *testing.T) {
	svc, q, userID, agentID := setup(t)
	ctx := context.Background()

	reg, err := svc.Create(ctx, mcp.CreateInput{
		Scope: mcp.ScopeSystem, Name: "fo", URL: "https://mcp.example.com",
		Transport: mcp.TransportStreamableHTTP, AuthType: mcp.AuthTypeNone,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const (
		oldPrefix = "mcp__fo__"
		newPrefix = "mcp__fo2__"
	)
	// Overrides across owners and scopes, all keyed by the old prefix, plus a
	// look-alike row for a longer server name that must survive untouched.
	seed := []sqlc.UpsertToolOverrideParams{
		{ToolName: oldPrefix + "list", Scope: agent.ToolOverrideScopeSystem},
		{ToolName: oldPrefix + "list", Scope: agent.ToolOverrideScopeUser, UserID: pgnull.Text(userID)},
		{ToolName: oldPrefix + "list", Scope: agent.ToolOverrideScopeSystemAgent, AgentID: pgnull.Text(agentID)},
		{ToolName: "mcp__foo__list", Scope: agent.ToolOverrideScopeSystem},
	}
	for _, arg := range seed {
		if _, err := q.UpsertToolOverride(ctx, arg); err != nil {
			t.Fatalf("seed override %+v: %v", arg, err)
		}
	}

	updated, err := svc.Update(ctx, mcp.UpdateInput{ID: reg.ID, Scope: mcp.ScopeSystem, Name: namePtr("fo2")})
	if err != nil {
		t.Fatalf("rename Update: %v", err)
	}
	if updated.Name != "fo2" {
		t.Fatalf("name = %q", updated.Name)
	}

	rows, err := q.ListToolOverridesForAgentContext(ctx, sqlc.ListToolOverridesForAgentContextParams{
		UserID: pgnull.Text(userID), AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		t.Fatalf("list overrides: %v", err)
	}
	got := map[string]int{}
	for _, row := range rows {
		got[row.ToolName]++
	}
	// 3 migrated rows under the new prefix + 1 untouched look-alike; the total
	// count is unchanged — a rename rewrites, it never creates or deletes.
	if got[newPrefix+"list"] != 3 || got["mcp__foo__list"] != 1 || len(rows) != 4 {
		t.Fatalf("override rows after rename = %v, want 3 migrated + 1 untouched", got)
	}
	for _, row := range rows {
		if strings.HasPrefix(row.ToolName, oldPrefix) {
			t.Fatalf("stale prefix survived rename: %q", row.ToolName)
		}
	}
}

// TestDeleteRemovesToolOverrides proves a registration delete removes every
// override row for that server's tools, across all owners.
func TestDeleteRemovesToolOverrides(t *testing.T) {
	svc, q, userID, agentID := setup(t)
	ctx := context.Background()

	reg, err := svc.Create(ctx, mcp.CreateInput{
		Scope: mcp.ScopeSystem, Name: "gh", URL: "https://mcp.example.com",
		Transport: mcp.TransportStreamableHTTP, AuthType: mcp.AuthTypeNone,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, arg := range []sqlc.UpsertToolOverrideParams{
		{ToolName: "mcp__gh__list", Scope: agent.ToolOverrideScopeSystem},
		{ToolName: "mcp__gh__list", Scope: agent.ToolOverrideScopeUser, UserID: pgnull.Text(userID)},
		{ToolName: "mcp__gh__list", Scope: agent.ToolOverrideScopeSystemAgent, AgentID: pgnull.Text(agentID)},
		{ToolName: "mcp__other__list", Scope: agent.ToolOverrideScopeSystem},
	} {
		if _, err := q.UpsertToolOverride(ctx, arg); err != nil {
			t.Fatalf("seed override %+v: %v", arg, err)
		}
	}

	if err := svc.Delete(ctx, reg.ID, mcp.ScopeSystem, "", ""); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	rows, err := q.ListToolOverridesForAgentContext(ctx, sqlc.ListToolOverridesForAgentContextParams{
		UserID: pgnull.Text(userID), AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		t.Fatalf("list overrides: %v", err)
	}
	for _, row := range rows {
		if strings.HasPrefix(row.ToolName, "mcp__gh__") {
			t.Fatalf("override survived delete: %q", row.ToolName)
		}
	}
	if len(rows) != 1 || rows[0].ToolName != "mcp__other__list" {
		t.Fatalf("unrelated overrides damaged: %v", rows)
	}
}

// TestResolveForContextWithShadowedRecordsShadowedScopes proves the shadow
// list names the losing scopes for each winner.
func TestResolveForContextWithShadowedRecordsShadowedScopes(t *testing.T) {
	svc, _, userID, agentID := setup(t)
	ctx := context.Background()

	mustCreate(t, svc, mcp.CreateInput{Scope: mcp.ScopeSystem, Name: "gh", URL: "http://system"})
	mustCreate(t, svc, mcp.CreateInput{Scope: mcp.ScopeSystemAgent, AgentID: agentID, Name: "gh", URL: "http://system_agent"})
	mustCreate(t, svc, mcp.CreateInput{Scope: mcp.ScopeUser, UserID: userID, Name: "other", URL: "http://other"})

	resolved, err := svc.ResolveForContextWithShadowed(ctx, userID, agentID)
	if err != nil {
		t.Fatalf("ResolveForContextWithShadowed: %v", err)
	}
	byName := map[string]mcp.ResolvedRegistration{}
	for _, r := range resolved {
		byName[r.Name] = r
	}
	gh := byName["gh"]
	if gh.Scope != mcp.ScopeSystemAgent || gh.URL != "http://system_agent" {
		t.Fatalf("gh winner = %s/%s", gh.Scope, gh.URL)
	}
	if len(gh.ShadowedScopes) != 1 || gh.ShadowedScopes[0] != mcp.ScopeSystem {
		t.Fatalf("shadowed scopes = %v", gh.ShadowedScopes)
	}
	if other := byName["other"]; len(other.ShadowedScopes) != 0 {
		t.Fatalf("unshadowed registration carries shadows: %v", other.ShadowedScopes)
	}
}
