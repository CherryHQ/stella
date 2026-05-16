package credentials_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/credentials"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
)

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
