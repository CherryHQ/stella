package connections_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/connections"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgdb "github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// --- helpers ---

func newService(t *testing.T) *connections.Service {
	t.Helper()
	flowStore := oauth.NewFlowStore()
	return connections.NewService(nil, nil, flowStore, "http://localhost:8080")
}

func userAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID(id), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func testProviderConfig(id, vaultKey string) oauth.ProviderConfig {
	return oauth.ProviderConfig{
		ID:       id,
		VaultKey: vaultKey,
		Flows: []oauth.ProviderFlowConfig{{
			Type:          "device_code",
			DeviceAuthURL: "https://example.com/device",
			TokenURL:      "https://example.com/token",
		}},
	}
}

func testProviderConfigWithCreds(id, vaultKey, clientID, clientSecret string) oauth.ProviderConfig {
	cfg := testProviderConfig(id, vaultKey)
	cfg.ClientID = clientID
	cfg.ClientSecret = clientSecret
	return cfg
}

// --- Service tests ---

func TestAddSecretInstruction(t *testing.T) {
	svc := newService(t)
	inst := svc.AddSecretInstruction("OPENAI_API_KEY", "access the OpenAI API")
	if inst.Name != "OPENAI_API_KEY" {
		t.Errorf("Name = %q; want OPENAI_API_KEY", inst.Name)
	}
	if !strings.Contains(inst.Command, "/config OPENAI_API_KEY") {
		t.Errorf("Command %q does not contain expected prefix", inst.Command)
	}
	if inst.Purpose != "access the OpenAI API" {
		t.Errorf("Purpose = %q; want 'access the OpenAI API'", inst.Purpose)
	}
}

func TestListVaultNilVault(t *testing.T) {
	svc := newService(t)
	_, err := svc.ListVault(context.Background(), "1")
	if err == nil {
		t.Error("expected error when vault is nil")
	}
}

func TestDeleteVaultEntryNilVault(t *testing.T) {
	svc := newService(t)
	err := svc.DeleteVaultEntry(context.Background(), "1", "FOO")
	if err == nil {
		t.Error("expected error when vault is nil")
	}
}

func TestGetProviderStatusesYAMLCredentials(t *testing.T) {
	svc := newService(t)

	registry := oauth.NewProviderRegistry()
	// github has YAML credentials — available without DB.
	registry.Register(testProviderConfigWithCreds("github", oauth.VaultKeyGitHub, "client-id", ""))
	// acme has no credentials — unavailable.
	registry.Register(testProviderConfig("acme", "ACME_OAUTH"))
	svc.SetRegistry(registry)

	statuses := svc.GetProviderStatuses(context.Background(), "1")
	if len(statuses) == 0 {
		t.Error("expected at least one provider status")
	}
	for _, ps := range statuses {
		switch ps.Provider {
		case "github":
			if !ps.Available {
				t.Errorf("github should be available with YAML client_id: %+v", ps)
			}
			if !ps.Configured {
				t.Errorf("github should be configured when YAML has client_id: %+v", ps)
			}
		case "acme":
			if ps.Available {
				t.Errorf("acme should be unavailable when no credentials configured: %+v", ps)
			}
			if ps.Configured {
				t.Errorf("acme should not be configured when no client_id: %+v", ps)
			}
			if ps.Unavailable == "" {
				t.Error("acme missing unavailable reason")
			}
		}
	}
}

func TestStartFlowNilVault(t *testing.T) {
	svc := newService(t)
	_, err := svc.StartFlow(context.Background(), "1", "github", nil)
	if err == nil {
		t.Error("expected error when vault is nil")
	}
}

func TestPollFlowUnknownFlow(t *testing.T) {
	svc := newService(t)
	_, _, err := svc.PollFlow(context.Background(), "1", "github", "nonexistent-flow-id")
	if err == nil {
		t.Error("expected error for unknown flow")
	}
}

func TestStartFlowUnsupportedProvider(t *testing.T) {
	svc := newService(t)
	_, err := svc.StartFlow(context.Background(), "1", "unsupported-provider", nil)
	if err == nil {
		t.Error("expected error for unsupported provider")
	}
}

func TestDisconnectNilVault(t *testing.T) {
	svc := newService(t)
	err := svc.Disconnect(context.Background(), "1", "github")
	if err == nil {
		t.Error("expected error when vault is nil")
	}
}

func TestDisconnectUnsupportedProvider(t *testing.T) {
	svc := newService(t)
	err := svc.Disconnect(context.Background(), "1", "badprovider")
	if err == nil {
		t.Error("expected error for unsupported provider")
	}
}

// A zero (invalid) Authority is refused up front, and a valid Authority carrying
// no user (a system agent) cannot bind a user capability.
func TestOAuthAccessRejectsInvalidAndSystemAuthority(t *testing.T) {
	svc := newService(t)
	if _, err := svc.Access(authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Access(zero) err=%v, want forbidden", err)
	}
	sysAuth, err := agentaccess.SystemAgentAuthority("test")
	if err != nil {
		t.Fatalf("SystemAgentAuthority: %v", err)
	}
	if _, err := svc.Access(sysAuth); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("Access(system) err=%v, want unauthenticated", err)
	}
}

// A foreign user cannot poll another user's flow: the persisted flow owner hides
// it as an opaque not-found. The owner polling a missing flow is also not-found.
func TestOAuthPollFlowOwnership(t *testing.T) {
	ctx := context.Background()
	flowStore := oauth.NewFlowStore()
	flowStore.Create(oauth.FlowStatus{Provider: oauth.ProviderGitHub, FlowID: "owner-flow", UserID: "owner", FlowType: "device_code"})
	svc := connections.NewService(nil, nil, flowStore, "http://localhost:8080")

	accForeign, err := svc.Access(userAuthority(t, "foreign"))
	if err != nil {
		t.Fatalf("Access foreign: %v", err)
	}
	if _, _, err := accForeign.PollFlow(ctx, "github", "owner-flow"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign PollFlow err=%v, want forbidden", err)
	}
	accOwner, err := svc.Access(userAuthority(t, "owner"))
	if err != nil {
		t.Fatalf("Access owner: %v", err)
	}
	if _, _, err := accOwner.PollFlow(ctx, "github", "missing-flow"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("owner PollFlow missing err=%v, want not found", err)
	}
	if _, _, err := accOwner.PollFlow(ctx, "google", "owner-flow"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("owner PollFlow wrong provider err=%v, want not found", err)
	}
}

// A delegated agent has the same connection access as its delegating user (an
// agent shares its user's connections).
func TestOAuthAgentActsAsUser(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	authority, err := agentaccess.WorkerAgentAuthority("user-1", "agent-x")
	if err != nil {
		t.Fatalf("WorkerAgentAuthority: %v", err)
	}
	acc, err := svc.Access(authority)
	if err != nil {
		t.Fatalf("Access: %v", err)
	}
	// No registry configured → empty status list, no error.
	if _, err := acc.Statuses(ctx); err != nil {
		t.Fatalf("agent Statuses: %v", err)
	}
}

func TestInvalidateUserNilInvalidator(t *testing.T) {
	svc := newService(t)
	if err := svc.InvalidateUser("42"); err != nil {
		t.Errorf("InvalidateUser with nil invalidator should be a no-op, got %v", err)
	}
}

type stubInvalidator struct {
	called    string
	allCalled int
}

func (s *stubInvalidator) InvalidateUser(userID string) error {
	s.called = userID
	return nil
}

func (s *stubInvalidator) InvalidateAgent(agentID string) error { return nil }

func (s *stubInvalidator) InvalidateAll() error {
	s.allCalled++
	return nil
}

func TestInvalidateUserCallsInvalidator(t *testing.T) {
	svc := newService(t)
	inv := &stubInvalidator{}
	svc.SetInvalidator(inv)
	if err := svc.InvalidateUser("99"); err != nil {
		t.Fatal(err)
	}
	if inv.called != "99" {
		t.Errorf("InvalidateUser called with %s, want 99", inv.called)
	}
}

func TestSetOAuthProviderConfigNilDB(t *testing.T) {
	svc := newService(t)
	err := svc.SetOAuthProviderConfig(context.Background(), connections.OAuthProviderConfig{
		ProviderID: "acme", ClientID: "cid", ClientSecret: "csecret",
	})
	if err == nil {
		t.Error("expected error when DB is nil")
	}
}

func TestSetAndGetOAuthProviderConfig(t *testing.T) {
	db := dbtest.New(t)

	svc := connections.NewService(nil, pkgdb.New(db), oauth.NewFlowStore(), "http://localhost:8080")
	ctx := context.Background()

	if err := svc.SetOAuthProviderConfig(ctx, connections.OAuthProviderConfig{ProviderID: "github", ClientID: "my-client"}); err != nil {
		t.Fatalf("SetOAuthProviderConfig: %v", err)
	}

	got, err := svc.GetOAuthProviderConfig(ctx, "github")
	if err != nil {
		t.Fatalf("GetOAuthProviderConfig: %v", err)
	}
	if got.ClientID != "my-client" {
		t.Fatalf("ClientID = %q, want %q", got.ClientID, "my-client")
	}
}

// A credential change (client_id) invalidates all runners; a scope-only change
// leaves existing sessions running (D4).
func TestSetOAuthProviderConfigInvalidatesOnCredentialChange(t *testing.T) {
	db := dbtest.New(t)
	svc := connections.NewService(nil, pkgdb.New(db), oauth.NewFlowStore(), "http://localhost:8080")
	inv := &stubInvalidator{}
	svc.SetInvalidator(inv)
	ctx := context.Background()

	// First-time config: no prior override, no secret → no invalidation.
	if err := svc.SetOAuthProviderConfig(ctx, connections.OAuthProviderConfig{ProviderID: "github", ClientID: "client-a"}); err != nil {
		t.Fatalf("initial set: %v", err)
	}
	if inv.allCalled != 0 {
		t.Fatalf("first-time set invalidated %d times, want 0", inv.allCalled)
	}

	// client_id change → InvalidateAll.
	if err := svc.SetOAuthProviderConfig(ctx, connections.OAuthProviderConfig{ProviderID: "github", ClientID: "client-b"}); err != nil {
		t.Fatalf("credential change set: %v", err)
	}
	if inv.allCalled != 1 {
		t.Fatalf("credential change invalidated %d times, want 1", inv.allCalled)
	}

	// scope-only change (same client_id, no secret) → no further invalidation.
	if err := svc.SetOAuthProviderConfig(ctx, connections.OAuthProviderConfig{ProviderID: "github", ClientID: "client-b", Scopes: []string{"repo", "read:org"}}); err != nil {
		t.Fatalf("scope-only set: %v", err)
	}
	if inv.allCalled != 1 {
		t.Errorf("scope-only change invalidated %d times, want it to stay at 1", inv.allCalled)
	}

	// The override round-trips with defaults exposed.
	got, err := svc.GetOAuthProviderConfig(ctx, "github")
	if err != nil {
		t.Fatalf("GetOAuthProviderConfig: %v", err)
	}
	if len(got.Scopes) != 2 || got.Scopes[0] != "repo" || got.Scopes[1] != "read:org" {
		t.Errorf("Scopes = %v, want [repo read:org]", got.Scopes)
	}
}
