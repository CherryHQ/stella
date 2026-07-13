package connections_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
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
	return connections.NewService(nil, nil, flowStore, "http://localhost:8080", nil)
}

func userAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	rs, err := authz.NewRoleSet(authz.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	a, err := authz.NewUserAuthority(authz.UserID(id), rs, authz.GrantSet{})
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
	// lark has no credentials — unavailable.
	registry.Register(testProviderConfig("lark", oauth.VaultKeyLark))
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
		case "lark":
			if ps.Available {
				t.Errorf("lark should be unavailable when no credentials configured: %+v", ps)
			}
			if ps.Configured {
				t.Errorf("lark should not be configured when no client_id: %+v", ps)
			}
			if ps.Unavailable == "" {
				t.Error("lark missing unavailable reason")
			}
		}
	}
}

func TestStartFlowNilVault(t *testing.T) {
	svc := newService(t)
	_, err := svc.StartFlow(context.Background(), "1", "github")
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
	_, err := svc.StartFlow(context.Background(), "1", "unsupported-provider")
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

// countingAuthorizer proves the PEP opens exactly one Begin per use case.
type countingAuthorizer struct {
	authz.Authorizer
	begins int
}

func (a *countingAuthorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	a.begins++
	return a.Authorizer.Begin(ctx, authority)
}

func TestOAuthAccessEnforcesUserIdentity(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	flowStore := oauth.NewFlowStore()
	flowStore.Create(oauth.FlowStatus{Provider: oauth.ProviderGitHub, FlowID: "owner-flow", UserID: "owner", FlowType: "device_code"})
	svc := connections.NewService(nil, nil, flowStore, "http://localhost:8080", policy.New(db))

	// A zero (invalid) Authority is refused before any evaluation.
	if _, err := svc.Begin(ctx, authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Begin(zero) err=%v, want forbidden", err)
	}
	// A foreign user cannot poll the owner's flow (is_owner=false → denied).
	accForeign, err := svc.Begin(ctx, userAuthority(t, "foreign"))
	if err != nil {
		t.Fatalf("Begin foreign: %v", err)
	}
	if _, _, err := accForeign.PollFlow(ctx, "github", "owner-flow"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("foreign PollFlow err=%v, want forbidden", err)
	}
	// The owner polling a missing flow is opaque not-found.
	accOwner, err := svc.Begin(ctx, userAuthority(t, "owner"))
	if err != nil {
		t.Fatalf("Begin owner: %v", err)
	}
	if _, _, err := accOwner.PollFlow(ctx, "github", "missing-flow"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("owner PollFlow missing err=%v, want not found", err)
	}
}

// A delegated agent has the same connection access as its delegating user; the
// PEP opens exactly one Begin per use case.
func TestOAuthAgentActsAsUser(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	az := &countingAuthorizer{Authorizer: policy.New(db)}
	svc := connections.NewService(nil, nil, oauth.NewFlowStore(), "http://localhost:8080", az)

	authority, err := agentaccess.WorkerAgentAuthority("user-1", "agent-x")
	if err != nil {
		t.Fatalf("WorkerAgentAuthority: %v", err)
	}
	acc, err := svc.Begin(ctx, authority)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	before := az.begins
	// No registry configured → empty status list, but the list decision is allowed.
	if _, err := acc.Statuses(ctx); err != nil {
		t.Fatalf("agent Statuses: %v", err)
	}
	if az.begins != before {
		t.Fatalf("Statuses opened %d extra Begins, want 0 within one Access", az.begins-before)
	}
}

// A custom deny on the connection list overrides the owner built-in.
func TestOAuthCustomDenyHidesStatuses(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	ps := policy.NewService(policy.New(db))
	if _, _, err := ps.CreatePolicy(ctx, policy.PolicyInput{
		Name: "deny connection list", Resource: authz.ResourceConnection, Action: authz.ActionList,
		Effect: policy.EffectDeny, Subjects: policy.NewSubjectBuilder().Roles(authz.RoleUser).Build(),
	}); err != nil {
		t.Fatal(err)
	}
	svc := connections.NewService(nil, nil, oauth.NewFlowStore(), "http://localhost:8080", policy.New(db))
	acc, err := svc.Begin(ctx, userAuthority(t, "u1"))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := acc.Statuses(ctx); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("custom deny Statuses err=%v, want forbidden", err)
	}
}

func TestInvalidateUserNilInvalidator(t *testing.T) {
	svc := newService(t)
	if err := svc.InvalidateUser("42"); err != nil {
		t.Errorf("InvalidateUser with nil invalidator should be a no-op, got %v", err)
	}
}

type stubInvalidator struct{ called string }

func (s *stubInvalidator) InvalidateUser(userID string) error {
	s.called = userID
	return nil
}

func (s *stubInvalidator) InvalidateAgent(agentID string) error { return nil }

func (s *stubInvalidator) InvalidateAll() error { return nil }

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
		ProviderID: "lark", ClientID: "cid", ClientSecret: "csecret",
	})
	if err == nil {
		t.Error("expected error when DB is nil")
	}
}

func TestSetAndGetOAuthProviderConfig(t *testing.T) {
	db := dbtest.New(t)

	svc := connections.NewService(nil, pkgdb.New(db), oauth.NewFlowStore(), "http://localhost:8080", policy.New(db))
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
