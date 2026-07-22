package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

func TestWebhookEndpointAdminLifecycleAndRedaction(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()
	owner, nonAdminToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "webhook-owner", auth.RoleUser)
	seedWebhookEndpointChannel(t, env, "hook", "agent-a")

	// The Access admin gate runs before the channel lookup, including for a path
	// that names no channel at all.
	rr := doRequestWithSession(t, env.srv, nonAdminToken, http.MethodGet, "/api/channels/missing/webhook-endpoint", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	issue := func() map[string]any {
		t.Helper()
		rr := doRequest(t, env, http.MethodPost, "/api/channels/hook/webhook-endpoint", map[string]any{
			"owner_user_id": owner.ID,
			"provider":      "generic",
		})
		if rr.Code != http.StatusCreated {
			t.Fatalf("issue status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"token_hash", "provider_secret_ciphertext", "capability"} {
			if strings.Contains(rr.Body.String(), forbidden) {
				t.Fatalf("one-time response leaked %q: %s", forbidden, rr.Body.String())
			}
		}
		if url, _ := body["url"].(string); !strings.HasPrefix(url, "http://localhost:25678/webhooks/stella_whk_") {
			t.Fatalf("issued URL = %q", url)
		}
		return body
	}

	issued := issue()
	// Behavior and GitHub allowlists remain updateable while active.
	rr = doRequest(t, env, http.MethodPatch, "/api/channels/hook", map[string]any{"config": `{"provider":"generic","default_wait":true}`})
	if rr.Code != http.StatusOK {
		t.Fatalf("behavior update status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	// Provider, type, and agent are identity-bearing and must revoke first.
	rr = doRequest(t, env, http.MethodPatch, "/api/channels/hook", map[string]any{"config": `{"provider":"github","github_events":["push"],"github_repositories":["acme/repo"]}`})
	if rr.Code != http.StatusConflict {
		t.Fatalf("provider update with active endpoint status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodPatch, "/api/channels/hook", map[string]any{"type": "telegram", "config": `{"token":"telegram-token"}`})
	if rr.Code != http.StatusConflict {
		t.Fatalf("type update with active endpoint status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}
	rr = doRequest(t, env, http.MethodPost, "/api/channels/hook/webhook-endpoint", map[string]any{
		"owner_user_id": owner.ID,
		"provider":      "generic",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("second issue status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, env, http.MethodGet, "/api/channels/hook/webhook-endpoint", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	for _, forbidden := range []string{"url", "secret", "hash", "ciphertext", "stella_whk_"} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("stable metadata leaked %q: %s", forbidden, rr.Body.String())
		}
	}

	rr = doRequest(t, env, http.MethodPost, "/api/channels/hook/webhook-endpoint/rotate", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var rotated map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated["url"] == issued["url"] {
		t.Fatal("rotation returned the old URL")
	}

	rr = doRequest(t, env, http.MethodPatch, "/api/channels/hook", map[string]any{"agent_id": "agent-b", "config": `{"provider":"generic"}`})
	if rr.Code != http.StatusConflict {
		t.Fatalf("rebind with active endpoint status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, env, http.MethodDelete, "/api/channels/hook/webhook-endpoint", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
	if _, err := env.store.GetChannel(ctx, "hook"); err != nil {
		t.Fatalf("endpoint deletion removed channel: %v", err)
	}
	rr = doRequest(t, env, http.MethodPatch, "/api/channels/hook", map[string]any{"agent_id": "agent-b", "config": `{"provider":"generic"}`})
	if rr.Code != http.StatusOK {
		t.Fatalf("rebind after revoke status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	_ = issue()

	var endpointID string
	if err := env.db.QueryRow(ctx, "SELECT id FROM channel_webhook_endpoint WHERE channel_id = 'hook'").Scan(&endpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(ctx, "INSERT INTO channel_webhook_delivery (endpoint_id, provider, delivery_id) VALUES ($1, 'github', 'cascade')", endpointID); err != nil {
		t.Fatal(err)
	}
	rr = doRequest(t, env, http.MethodDelete, "/api/channels/hook", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("channel delete status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
	var endpoints, deliveries int
	if err := env.db.QueryRow(ctx, "SELECT count(*) FROM channel_webhook_endpoint").Scan(&endpoints); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(ctx, "SELECT count(*) FROM channel_webhook_delivery").Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if endpoints != 0 || deliveries != 0 {
		t.Fatalf("channel deletion did not cascade endpoint/delivery: %d/%d", endpoints, deliveries)
	}
}

func TestWebhookEndpointIssueValidatesOwnerAccessAndEnabledAgent(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()
	owner, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "restricted-owner", auth.RoleUser)
	if err := env.store.CreateAgent(ctx, config.Agent{ID: "restricted", Name: "restricted", Workspace: "/tmp", Scope: config.AgentScopeRestricted, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpsertChannel(ctx, config.Channel{ID: "guard", Type: pkgchannel.PlatformWebhook, AgentID: "restricted", Enabled: true, Config: `{"provider":"generic"}`}); err != nil {
		t.Fatal(err)
	}
	issueOwner := func(ownerID string) int {
		t.Helper()
		return doRequest(t, env, http.MethodPost, "/api/channels/guard/webhook-endpoint", map[string]any{"owner_user_id": ownerID, "provider": "generic"}).Code
	}
	issue := func() int { return issueOwner(owner.ID) }
	for name, ownerID := range map[string]string{
		"malformed": "not-a-uuid",
		"missing":   "00000000-0000-7000-8000-000000000001",
	} {
		t.Run(name+" owner", func(t *testing.T) {
			if got := issueOwner(ownerID); got != http.StatusBadRequest {
				t.Fatalf("%s owner issue status = %d, want 400", name, got)
			}
		})
	}
	if got := issue(); got != http.StatusBadRequest {
		t.Fatalf("unassigned owner issue status = %d, want 400", got)
	}
	if err := env.authStore.AssignAgent(ctx, owner.ID, "restricted"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpdateAgent(ctx, config.Agent{ID: "restricted", Name: "restricted", Workspace: "/tmp", Scope: config.AgentScopeRestricted, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if got := issue(); got != http.StatusBadRequest {
		t.Fatalf("disabled agent issue status = %d, want 400", got)
	}
	if _, err := env.db.Exec(ctx, "UPDATE agent SET enabled = true WHERE id = 'restricted'"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(ctx, "UPDATE auth_user SET is_active = false WHERE id = $1", owner.ID); err != nil {
		t.Fatal(err)
	}
	if got := issue(); got != http.StatusBadRequest {
		t.Fatalf("inactive owner issue status = %d, want 400", got)
	}
	if _, err := env.db.Exec(ctx, "UPDATE auth_user SET is_active = true WHERE id = $1", owner.ID); err != nil {
		t.Fatal(err)
	}
	if got := issue(); got != http.StatusCreated {
		t.Fatalf("validated issue status = %d, want 201", got)
	}
}

func TestChannelUpdatePreservesBindingConflictError(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()
	seedWebhookEndpointChannel(t, env, "seed", "agent-a")
	for _, ch := range []config.Channel{
		{ID: "telegram-a", Type: pkgchannel.PlatformTelegram, AgentID: "agent-a", Enabled: true, Config: `{"token":"a"}`},
		{ID: "telegram-b", Type: pkgchannel.PlatformTelegram, AgentID: "agent-b", Enabled: true, Config: `{"token":"b"}`},
	} {
		if err := env.store.UpsertChannel(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	rr := doRequest(t, env, http.MethodPatch, "/api/channels/telegram-b", map[string]any{
		"agent_id": "agent-a",
		"config":   `{"token":"updated"}`,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("binding conflict status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "agent is already bound to telegram channel telegram-a") {
		t.Fatalf("binding conflict body = %s", rr.Body.String())
	}
}

func TestGitHubWebhookEndpointRequiresVault(t *testing.T) {
	env := setupAdmin(t)
	owner, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "github-owner", auth.RoleUser)
	seedWebhookEndpointChannelWithConfig(t, env, "github-hook", "agent-a", `{"provider":"github","github_events":["push"],"github_repositories":["acme/repo"]}`)

	rr := doRequest(t, env, http.MethodPost, "/api/channels/github-hook/webhook-endpoint", map[string]any{
		"owner_user_id": owner.ID,
		"provider":      "github",
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("github without vault status = %d, want 503 (body: %s)", rr.Code, rr.Body.String())
	}
}

func seedWebhookEndpointChannel(t *testing.T, env *testEnv, channelID, agentID string) {
	t.Helper()
	seedWebhookEndpointChannelWithConfig(t, env, channelID, agentID, `{"provider":"generic"}`)
}

func seedWebhookEndpointChannelWithConfig(t *testing.T, env *testEnv, channelID, agentID, rawConfig string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"agent-a", "agent-b"} {
		if err := env.store.CreateAgent(ctx, config.Agent{ID: id, Name: id, Workspace: "/tmp", Scope: config.AgentScopeSystem, Enabled: true}); err != nil && !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("CreateAgent %q: %v", id, err)
		}
	}
	if err := env.store.UpsertChannel(ctx, config.Channel{
		ID: channelID, Type: pkgchannel.PlatformWebhook, AgentID: agentID, Enabled: true, Config: rawConfig,
	}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
}
