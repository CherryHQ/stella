package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"filippo.io/age"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// oidcVaultDB wraps OIDCStore.GetVaultUser with sqlc.Queries for vault entry operations.
// This bridges the gap: OIDCStore reads age keys from auth_user, sqlc handles vault_entry.
type oidcVaultDB struct {
	*appdb.OIDCStore
	q *sqlc.Queries
}

func (d *oidcVaultDB) GetVaultEntryByScope(ctx context.Context, arg sqlc.GetVaultEntryByScopeParams) (sqlc.VaultEntry, error) {
	return d.q.GetVaultEntryByScope(ctx, arg)
}

func (d *oidcVaultDB) ListVaultEntriesByScope(ctx context.Context, arg sqlc.ListVaultEntriesByScopeParams) ([]sqlc.VaultEntry, error) {
	return d.q.ListVaultEntriesByScope(ctx, arg)
}

func (d *oidcVaultDB) ListVaultEntriesForRuntime(ctx context.Context, arg sqlc.ListVaultEntriesForRuntimeParams) ([]sqlc.VaultEntry, error) {
	return d.q.ListVaultEntriesForRuntime(ctx, arg)
}

func (d *oidcVaultDB) ListVaultEntriesForRuntimeFull(ctx context.Context, arg sqlc.ListVaultEntriesForRuntimeFullParams) ([]sqlc.VaultEntry, error) {
	return d.q.ListVaultEntriesForRuntimeFull(ctx, arg)
}

func (d *oidcVaultDB) ListVaultEntriesDeclarableForRuntime(ctx context.Context, arg sqlc.ListVaultEntriesDeclarableForRuntimeParams) ([]sqlc.VaultEntry, error) {
	return d.q.ListVaultEntriesDeclarableForRuntime(ctx, arg)
}

func (d *oidcVaultDB) CreateVaultExecSecretAudit(ctx context.Context, arg sqlc.CreateVaultExecSecretAuditParams) (sqlc.VaultExecSecretAudit, error) {
	return d.q.CreateVaultExecSecretAudit(ctx, arg)
}

func (d *oidcVaultDB) ListVaultExecSecretAuditByUser(ctx context.Context, arg sqlc.ListVaultExecSecretAuditByUserParams) ([]sqlc.VaultExecSecretAudit, error) {
	return d.q.ListVaultExecSecretAuditByUser(ctx, arg)
}

func (d *oidcVaultDB) ListVaultEntryAgentBindings(ctx context.Context, vaultEntryID string) ([]string, error) {
	return d.q.ListVaultEntryAgentBindings(ctx, vaultEntryID)
}

func (d *oidcVaultDB) ListVaultEntryProjectBindings(ctx context.Context, vaultEntryID string) ([]string, error) {
	return d.q.ListVaultEntryProjectBindings(ctx, vaultEntryID)
}

func (d *oidcVaultDB) ReplaceVaultEntryAgentBindings(ctx context.Context, arg sqlc.ReplaceVaultEntryAgentBindingsParams) error {
	return d.q.ReplaceVaultEntryAgentBindings(ctx, arg)
}

func (d *oidcVaultDB) ReplaceVaultEntryProjectBindings(ctx context.Context, arg sqlc.ReplaceVaultEntryProjectBindingsParams) error {
	return d.q.ReplaceVaultEntryProjectBindings(ctx, arg)
}

func (d *oidcVaultDB) UpsertVaultEntryByScope(ctx context.Context, arg sqlc.UpsertVaultEntryByScopeParams) (sqlc.VaultEntry, error) {
	return d.q.UpsertVaultEntryByScope(ctx, arg)
}

func (d *oidcVaultDB) DeleteVaultEntryByScope(ctx context.Context, arg sqlc.DeleteVaultEntryByScopeParams) error {
	return d.q.DeleteVaultEntryByScope(ctx, arg)
}

func setupVaultEnv(t *testing.T) (*testEnv, *vault.Service) {
	t.Helper()
	env := setupAdmin(t)

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	vaultDB := &oidcVaultDB{OIDCStore: env.oidcStore, q: sqlc.New(env.db)}
	svc, err := vault.NewService(vaultDB, masterID.String())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	env.srv.SetVaultService(svc)

	// Provision age keys for the admin user.
	pubKey, encPrivKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if err := env.oidcStore.UpdateUserAgeKeys(t.Context(), env.adminUser.ID, pubKey, encPrivKey); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}

	return env, svc
}

func TestVaultNotConfigured(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/vault", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestVaultCRUD(t *testing.T) {
	env, _ := setupVaultEnv(t)

	// List — empty.
	rr := doRequest(t, env, "GET", "/api/vault", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var wrapper struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(resp.Data, &wrapper); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entries := wrapper.Entries
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}

	// Set a secret.
	rr = doRequest(t, env, "PUT", "/api/vault/GITHUB_TOKEN", map[string]string{"value": "ghp_test123"})
	if rr.Code != http.StatusOK {
		t.Fatalf("set status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// List — one entry.
	rr = doRequest(t, env, "GET", "/api/vault", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d", rr.Code, http.StatusOK)
	}
	if err := json.Unmarshal(parseListItems(t, rr, "entries"), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0]["name"] != "GITHUB_TOKEN" {
		t.Errorf("name = %q, want GITHUB_TOKEN", entries[0]["name"])
	}

	// Delete.
	rr = doRequest(t, env, "DELETE", "/api/vault/GITHUB_TOKEN", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	// List — empty again.
	rr = doRequest(t, env, "GET", "/api/vault", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	entries = nil
	if err := json.Unmarshal(parseListItems(t, rr, "entries"), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after delete, got %d", len(entries))
	}
}

func TestVaultSetValidationError(t *testing.T) {
	env, _ := setupVaultEnv(t)

	rr := doRequest(t, env, "PUT", "/api/vault/invalid_name", map[string]string{"value": "test"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestVaultNotConfiguredPUT(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "PUT", "/api/vault/MY_KEY", map[string]string{"value": "v"})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestVaultNotConfiguredDELETE(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "DELETE", "/api/vault/MY_KEY", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestVaultSetEmptyValue(t *testing.T) {
	env, _ := setupVaultEnv(t)

	rr := doRequest(t, env, "PUT", "/api/vault/MY_KEY", map[string]string{"value": ""})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestVaultSetEmptyName(t *testing.T) {
	env, _ := setupVaultEnv(t)

	rr := doRequest(t, env, "PUT", "/api/vault/", map[string]string{"value": "v"})
	// Empty name in path — either 400 or 404 depending on router
	if rr.Code == http.StatusOK {
		t.Fatal("expected error for empty name, got 200")
	}
}

func TestVaultSetInvalidJSON(t *testing.T) {
	env, _ := setupVaultEnv(t)

	rr := doRequestWithSession(t, env.srv, env.bearerToken, "PUT", "/api/vault/MY_KEY", nil)
	// No body → JSON decode error or empty value
	if rr.Code == http.StatusOK {
		t.Fatal("expected error for nil body, got 200")
	}
}

func TestVaultEmailConfigValidation(t *testing.T) {
	env, _ := setupVaultEnv(t)

	t.Run("rejects malformed JSON", func(t *testing.T) {
		rr := doRequest(t, env, "PUT", "/api/vault/EMAIL_CONFIG", map[string]string{"value": "not-json"})
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
		}
	})

	t.Run("rejects invalid config", func(t *testing.T) {
		rr := doRequest(t, env, "PUT", "/api/vault/EMAIL_CONFIG", map[string]string{
			"value": `{"default":"missing","accounts":{}}`,
		})
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
		}
	})

	t.Run("accepts valid config", func(t *testing.T) {
		rr := doRequest(t, env, "PUT", "/api/vault/EMAIL_CONFIG", map[string]string{
			"value": `{"default":"work","accounts":{"work":{"imap_host":"imap.example.com","smtp_host":"smtp.example.com","username":"u","from":"u@example.com"}}}`,
		})
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
		}
	})
}

func TestScopedVaultPermissionsAndRuntimeResolution(t *testing.T) {
	env, svc := setupVaultEnv(t)
	ctx := context.Background()
	q := sqlc.New(env.db)
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "agent-a", Name: "Agent A", Model: "test/model", Workspace: "workspace", Sandbox: json.RawMessage("{}"), EnabledBuiltinSkills: json.RawMessage("[]"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "restricted-a", Name: "Restricted A", Model: "test/model", Workspace: "workspace", Sandbox: json.RawMessage("{}"), EnabledBuiltinSkills: json.RawMessage("[]"), Scope: "restricted", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent restricted: %v", err)
	}

	regular, regularToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "regular-vault", "user")
	pubKey, encPrivKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys regular: %v", err)
	}
	if err := env.oidcStore.UpdateUserAgeKeys(ctx, regular.ID, pubKey, encPrivKey); err != nil {
		t.Fatalf("UpdateUserAgeKeys regular: %v", err)
	}

	rr := doRequestWithSession(t, env.srv, regularToken, "PUT", "/api/vault/SYSTEM_TOKEN", map[string]string{"scope": "system", "value": "nope"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("regular system set status = %d, want %d (body: %s)", rr.Code, http.StatusForbidden, rr.Body.String())
	}
	rr = doRequestWithSession(t, env.srv, regularToken, "PUT", "/api/vault/PRIVATE_TOKEN", map[string]string{"scope": "user_agent", "agent_id": "restricted-a", "value": "nope"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("regular restricted agent set status = %d, want %d (body: %s)", rr.Code, http.StatusForbidden, rr.Body.String())
	}

	for _, req := range []map[string]string{
		{"scope": "system", "value": "system"},
		{"scope": "system_agent", "agent_id": "agent-a", "value": "system-agent"},
	} {
		rr = doRequest(t, env, "PUT", "/api/vault/TOKEN", req)
		if rr.Code != http.StatusOK {
			t.Fatalf("admin set %s status = %d, want %d (body: %s)", req["scope"], rr.Code, http.StatusOK, rr.Body.String())
		}
	}
	for _, req := range []map[string]any{
		{"scope": "user", "value": "user", "inject_always": true},
		{"scope": "user_agent", "agent_id": "agent-a", "value": "user-agent", "inject_always": true},
	} {
		rr = doRequestWithSession(t, env.srv, regularToken, "PUT", "/api/vault/TOKEN", req)
		if rr.Code != http.StatusOK {
			t.Fatalf("regular set %s status = %d, want %d (body: %s)", req["scope"], rr.Code, http.StatusOK, rr.Body.String())
		}
	}

	envMap, err := svc.LoadEnvForAgentProject(ctx, regular.ID, "agent-a", "")
	if err != nil {
		t.Fatalf("LoadEnvForAgent: %v", err)
	}
	if got := envMap["TOKEN"]; got != "user-agent" {
		t.Fatalf("TOKEN = %q, want user-agent", got)
	}
}

// TestScopedVaultGet verifies the scoped GET /api/vault/{name} endpoint returns
// the value written through the scoped PUT, the read path CLI/web callers use
// after the legacy /api/vault routes were removed (#452).
func TestScopedVaultGet(t *testing.T) {
	env, _ := setupVaultEnv(t)

	// Write via the scoped endpoint (default scope=user).
	rr := doRequest(t, env, "PUT", "/api/vault/API_KEY", map[string]string{"value": "secret-value"})
	if rr.Code != http.StatusOK {
		t.Fatalf("scoped set status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Read back via the scoped endpoint.
	rr = doRequest(t, env, "GET", "/api/vault/API_KEY?scope=user", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("scoped get status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(parseResponse(t, rr).Data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["name"] != "API_KEY" || got["value"] != "secret-value" {
		t.Fatalf("got %+v, want name=API_KEY value=secret-value", got)
	}

	// Missing entry → 404.
	rr = doRequest(t, env, "GET", "/api/vault/NOPE?scope=user", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("scoped get missing status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}
}

// TestScopedTokenVaultAgentBinding is a regression guard for the secret-isolation
// boundary: a sandbox (scoped) token bound to one agent must not reach another
// agent's user_agent secrets nor the admin-managed system scopes, even when the
// underlying user could otherwise access those agents (#452 / CR-001).
func TestScopedTokenVaultAgentBinding(t *testing.T) {
	t.Setenv("STELLA_SCOPED_TOKEN_SECRET", "test-scoped-token-secret-fixed")
	env, svc := setupVaultEnv(t)
	ctx := context.Background()
	q := sqlc.New(env.db)

	for _, id := range []string{"sa-agent-a", "sa-agent-b"} {
		if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
			ID: id, Name: id, Model: "test/model", Workspace: "workspace", Sandbox: json.RawMessage("{}"), EnabledBuiltinSkills: json.RawMessage("[]"), Scope: "system", Enabled: true,
		}); err != nil {
			t.Fatalf("CreateAgent %s: %v", id, err)
		}
	}

	regular, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "scoped-vault", "user")
	pubKey, encPrivKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if err := env.oidcStore.UpdateUserAgeKeys(ctx, regular.ID, pubKey, encPrivKey); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}

	// Token bound to sa-agent-a.
	tokenSvc := auth.NewTokenService(env.authStore)
	env.srv.SetTokenService(tokenSvc)
	token, err := tokenSvc.CreateScopedToken(ctx, regular.ID, "sa-agent-a", "sess-1", "")
	if err != nil {
		t.Fatalf("CreateScopedToken: %v", err)
	}

	scoped := func(method, path string, body any) *httptest.ResponseRecorder {
		return doRequestWithSession(t, env.srv, token, method, path, body)
	}

	// Cross-agent access is rejected on every verb.
	if rr := scoped("GET", "/api/vault/X?scope=user_agent&agent_id=sa-agent-b", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("cross-agent GET = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := scoped("PUT", "/api/vault/X?scope=user_agent&agent_id=sa-agent-b", map[string]string{"scope": "user_agent", "agent_id": "sa-agent-b", "value": "v"}); rr.Code != http.StatusForbidden {
		t.Fatalf("cross-agent PUT = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := scoped("DELETE", "/api/vault/X?scope=user_agent&agent_id=sa-agent-b", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("cross-agent DELETE = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	// System scopes are off-limits to sandbox tokens regardless of agent_id.
	if rr := scoped("GET", "/api/vault/X?scope=system", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("system GET = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := scoped("PUT", "/api/vault/X", map[string]string{"scope": "system", "value": "v"}); rr.Code != http.StatusForbidden {
		t.Fatalf("system PUT = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	// The token's own agent and its own-user scope remain reachable.
	if rr := scoped("PUT", "/api/vault/OWN", map[string]string{"scope": "user_agent", "agent_id": "sa-agent-a", "value": "v"}); rr.Code != http.StatusOK {
		t.Fatalf("own-agent PUT = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := scoped("GET", "/api/vault/MISSING?scope=user", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("own-user GET = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
}

// fakeRunnerInvalidator records invalidation calls so tests can assert that
// vault mutations propagate to the runner cache at the right scope.
type fakeRunnerInvalidator struct {
	calls   []string // user IDs from InvalidateUser
	agents  []string // agent IDs from InvalidateAgent
	allHits int      // InvalidateAll count
}

func (f *fakeRunnerInvalidator) InvalidateUser(userID string) error {
	f.calls = append(f.calls, userID)
	return nil
}

func (f *fakeRunnerInvalidator) InvalidateAgent(agentID string) error {
	f.agents = append(f.agents, agentID)
	return nil
}

func (f *fakeRunnerInvalidator) InvalidateAll() error {
	f.allHits++
	return nil
}

// TestVaultMutationsInvalidateUserRunners verifies that PUT and DELETE on a
// vault entry both close the user's live runners, so the next session reads
// the new secret instead of the snapshot baked into the previous sandbox env.
// Regression guard for stale secrets in scheduled jobs after key rotation.
func TestVaultMutationsInvalidateUserRunners(t *testing.T) {
	env, _ := setupVaultEnv(t)

	inv := &fakeRunnerInvalidator{}
	env.srv.CredentialsService().SetInvalidator(inv)

	// PUT triggers invalidate.
	rr := doRequest(t, env, "PUT", "/api/vault/MY_TOKEN", map[string]string{"value": "v1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("set status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if len(inv.calls) != 1 {
		t.Fatalf("after PUT: expected 1 invalidate call, got %d", len(inv.calls))
	}
	if inv.calls[0] != env.adminUser.ID {
		t.Errorf("after PUT: invalidated user = %q, want %q", inv.calls[0], env.adminUser.ID)
	}

	// PUT overwriting also triggers invalidate (covers rotate-in-place).
	rr = doRequest(t, env, "PUT", "/api/vault/MY_TOKEN", map[string]string{"value": "v2"})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if len(inv.calls) != 2 {
		t.Fatalf("after overwrite PUT: expected 2 invalidate calls, got %d", len(inv.calls))
	}

	// DELETE also triggers invalidate.
	rr = doRequest(t, env, "DELETE", "/api/vault/MY_TOKEN", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if len(inv.calls) != 3 {
		t.Fatalf("after DELETE: expected 3 invalidate calls, got %d", len(inv.calls))
	}
	if inv.calls[2] != env.adminUser.ID {
		t.Errorf("after DELETE: invalidated user = %q, want %q", inv.calls[2], env.adminUser.ID)
	}
}

// TestSystemVaultMutationsInvalidateRunners verifies that admin-managed system
// secrets, which merge into every agent's runtime env, invalidate at the right
// reach: system → all runners, system_agent → that agent's runners (CR-002).
func TestSystemVaultMutationsInvalidateRunners(t *testing.T) {
	env, _ := setupVaultEnv(t)
	if _, err := sqlc.New(env.db).CreateAgent(context.Background(), sqlc.CreateAgentParams{
		ID: "sys-agent", Name: "Sys", Model: "test/model", Workspace: "workspace", Sandbox: json.RawMessage("{}"), EnabledBuiltinSkills: json.RawMessage("[]"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	inv := &fakeRunnerInvalidator{}
	env.srv.CredentialsService().SetInvalidator(inv)

	// system scope → InvalidateAll on both PUT and DELETE.
	rr := doRequest(t, env, "PUT", "/api/vault/SYS_TOKEN", map[string]string{"scope": "system", "value": "v"})
	if rr.Code != http.StatusOK {
		t.Fatalf("system set status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, "DELETE", "/api/vault/SYS_TOKEN?scope=system", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("system delete status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if inv.allHits != 2 {
		t.Fatalf("system mutations: InvalidateAll hits = %d, want 2", inv.allHits)
	}

	// system_agent scope → InvalidateAgent for that agent only.
	rr = doRequest(t, env, "PUT", "/api/vault/SA_TOKEN", map[string]string{"scope": "system_agent", "agent_id": "sys-agent", "value": "v"})
	if rr.Code != http.StatusOK {
		t.Fatalf("system_agent set status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if len(inv.agents) != 1 || inv.agents[0] != "sys-agent" {
		t.Fatalf("system_agent set: InvalidateAgent calls = %v, want [sys-agent]", inv.agents)
	}

	// System scopes must never fall back to per-user invalidation.
	if len(inv.calls) != 0 {
		t.Fatalf("system scopes must not InvalidateUser, got %v", inv.calls)
	}
}

func TestVaultUpdateExisting(t *testing.T) {
	env, _ := setupVaultEnv(t)

	// Set initial value.
	rr := doRequest(t, env, "PUT", "/api/vault/MY_SECRET", map[string]string{"value": "v1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("set status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Update with new value.
	rr = doRequest(t, env, "PUT", "/api/vault/MY_SECRET", map[string]string{"value": "v2"})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d", rr.Code, http.StatusOK)
	}

	// List — still one entry.
	rr = doRequest(t, env, "GET", "/api/vault", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	var wrapper struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(resp.Data, &wrapper); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entries := wrapper.Entries
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after update, got %d", len(entries))
	}
}

// TestScopedTokenVaultGetIsAudited guards the declare-time escape hatch: a
// sandbox scoped token reading a secret value through GET /api/vault/{name}
// must leave a vault_exec_secret_audit row, exactly like the bash `secrets`
// param path. Cookie sessions stay unaudited.
func TestScopedTokenVaultGetIsAudited(t *testing.T) {
	t.Setenv("STELLA_SCOPED_TOKEN_SECRET", "test-scoped-token-secret-fixed")
	env, svc := setupVaultEnv(t)
	ctx := context.Background()
	q := sqlc.New(env.db)

	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "audit-agent", Name: "audit-agent", Model: "test/model", Workspace: "workspace", Sandbox: json.RawMessage("{}"), EnabledBuiltinSkills: json.RawMessage("[]"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	regular, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "audited-vault", "user")
	pubKey, encPrivKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if err := env.oidcStore.UpdateUserAgeKeys(ctx, regular.ID, pubKey, encPrivKey); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}
	if err := svc.Set(ctx, regular.ID, "AUDITED_KEY", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	tokenSvc := auth.NewTokenService(env.authStore)
	env.srv.SetTokenService(tokenSvc)
	token, err := tokenSvc.CreateScopedToken(ctx, regular.ID, "audit-agent", "sess-audit", "")
	if err != nil {
		t.Fatalf("CreateScopedToken: %v", err)
	}

	if rr := doRequestWithSession(t, env.srv, token, "GET", "/api/vault/AUDITED_KEY?scope=user", nil); rr.Code != http.StatusOK {
		t.Fatalf("scoped get = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	rows, err := q.ListVaultExecSecretAuditByUser(ctx, sqlc.ListVaultExecSecretAuditByUserParams{UserID: regular.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListVaultExecSecretAuditByUser: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.AgentID != "audit-agent" || r.SessionID != "sess-audit" || r.Name != "AUDITED_KEY" || r.CommandText != "api: vault get" {
		t.Fatalf("audit row = %+v, want agent/session/name/command match", r)
	}
}
