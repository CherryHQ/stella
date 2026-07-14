package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"filippo.io/age"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz/policy"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/server"
	storepkg "github.com/CherryHQ/stella/internal/store"
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
	authorizer := policy.New(env.db)
	agents := agentaccess.NewService(storepkg.NewDBStore(env.db), appdb.NewAuthStore(env.db), authorizer)
	svc, err := vault.NewService(vaultDB, masterID.String(), authorizer, agents)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Wire the vault into the shared credentials service (same instance the
	// server holds) and rebuild the server with the vault + a vault-backed email
	// service, mirroring the composition root's single-instance wiring.
	env.credSvc.SetVaultService(svc)
	env.rebuild(t, func(d *server.Deps) {
		d.Vault = svc
		d.Email = email.NewService(svc, sqlc.New(env.db))
	})

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

func TestVaultSetRejectsSystemManagedName(t *testing.T) {
	env, _ := setupVaultEnv(t)

	rr := doRequest(t, env, "PUT", "/api/vault/OAUTH_FOO", map[string]string{"value": "test"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if got := parseResponse(t, rr).Error; got != "vault: name \"OAUTH_FOO\" is reserved for system-managed credentials" {
		t.Fatalf("error = %q, want system-managed reserved error", got)
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
		{"scope": "user", "value": "user"},
		{"scope": "user_agent", "agent_id": "agent-a", "value": "user-agent"},
	} {
		rr = doRequestWithSession(t, env.srv, regularToken, "PUT", "/api/vault/TOKEN", req)
		if rr.Code != http.StatusOK {
			t.Fatalf("regular set %s status = %d, want %d (body: %s)", req["scope"], rr.Code, http.StatusOK, rr.Body.String())
		}
	}

	envMap, err := svc.LoadEnvForAgent(ctx, regular.ID, "agent-a")
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
	env.credSvc.SetInvalidator(inv)

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
	env.credSvc.SetInvalidator(inv)

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
