package credentials_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/credentials"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgdb "github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// --- helpers ---

func newService(t *testing.T) *credentials.Service {
	t.Helper()
	flowStore := oauth.NewFlowStore()
	return credentials.NewService(nil, nil, flowStore, "http://localhost:8080")
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

func TestOAuthAuthorizedMethodsEnforceUserIdentity(t *testing.T) {
	ctx := context.Background()
	flowStore := oauth.NewFlowStore()
	flowStore.Create(oauth.FlowStatus{Provider: oauth.ProviderGitHub, FlowID: "owner-flow", UserID: "owner", FlowType: "device_code"})
	svc := credentials.NewService(nil, nil, flowStore, "http://localhost:8080")

	if _, err := svc.As(authz.Identity{}).Statuses(ctx); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("Statuses unauth err=%v, want ErrUnauthenticated", err)
	}
	if _, err := svc.As(authz.Identity{}).StartFlow(ctx, "github"); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("StartFlow unauth err=%v, want ErrUnauthenticated", err)
	}
	if _, _, err := svc.As(authz.Identity{}).PollFlow(ctx, "github", "owner-flow"); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("PollFlow unauth err=%v, want ErrUnauthenticated", err)
	}
	if err := svc.As(authz.Identity{}).Disconnect(ctx, "github"); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("Disconnect unauth err=%v, want ErrUnauthenticated", err)
	}
	if _, _, err := svc.As(authz.Identity{UserID: "foreign"}).PollFlow(ctx, "github", "owner-flow"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("PollFlow foreign err=%v, want ErrForbidden", err)
	}
	if _, _, err := svc.As(authz.Identity{UserID: "owner"}).PollFlow(ctx, "github", "missing-flow"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("PollFlow missing err=%v, want ErrNotFound", err)
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
	err := svc.SetOAuthProviderConfig(context.Background(), credentials.OAuthProviderConfig{
		ProviderID: "lark", ClientID: "cid", ClientSecret: "csecret",
	})
	if err == nil {
		t.Error("expected error when DB is nil")
	}
}

func TestSetAndGetOAuthProviderConfig(t *testing.T) {
	db := dbtest.New(t)

	svc := credentials.NewService(nil, pkgdb.New(db), oauth.NewFlowStore(), "http://localhost:8080")
	ctx := context.Background()

	if err := svc.SetOAuthProviderConfig(ctx, credentials.OAuthProviderConfig{ProviderID: "github", ClientID: "my-client"}); err != nil {
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
