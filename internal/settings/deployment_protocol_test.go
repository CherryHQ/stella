package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/mcp"
)

func TestDeploymentMutatorRejectsOrdinaryAuthorityAtControlPlaneBoundary(t *testing.T) {
	mutator := NewDeploymentMutator(controlplane.NewService(nil, nil, nil, nil, nil))
	ordinary, err := authz.NewUserAuthority("user-1", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := mutator.ListProviders(ctx, ordinary); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("ListProviders = %v, want forbidden", err)
	}
	if _, err := mutator.GetEmbedding(ctx, ordinary); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("GetEmbedding = %v, want forbidden", err)
	}
	if err := mutator.CreateProvider(ctx, ordinary, config.Provider{ID: "x", Type: "x"}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("CreateProvider = %v, want forbidden", err)
	}
}

func TestSettingsViewsNeverSerializeDeploymentSecrets(t *testing.T) {
	provider, err := json.Marshal(providerView(config.Provider{
		ID: "openai", Type: "openai", APIKey: "api-key-secret", BaseURL: "https://api.example.test/v1?token=query-secret#fragment-secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	providerText := string(provider)
	for _, secret := range []string{"api-key-secret", "query-secret", "fragment-secret"} {
		if strings.Contains(providerText, secret) {
			t.Fatalf("provider view leaks %q: %s", secret, providerText)
		}
	}
	if !strings.Contains(providerText, `"has_api_key":true`) {
		t.Fatalf("provider view does not expose presence bit: %s", providerText)
	}

	mcpText, err := json.Marshal(mcpView(mcp.Registration{
		URL: "https://mcp.example.test/tools?token=query-secret#fragment-secret", CredentialRef: "MCP_TOKEN_SECRET", AuthType: mcp.AuthTypeBearer,
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"query-secret", "fragment-secret", "MCP_TOKEN_SECRET"} {
		if strings.Contains(string(mcpText), secret) {
			t.Fatalf("MCP view leaks %q: %s", secret, mcpText)
		}
	}
	if !strings.Contains(string(mcpText), `"has_secret":true`) {
		t.Fatalf("MCP view does not expose presence bit: %s", mcpText)
	}
}

func TestSettingsRejectsURLCarriedSecretsAtModelBoundary(t *testing.T) {
	for _, raw := range []string{
		"https://user:password@example.test/mcp",
		"https://example.test/mcp?api_key=secret",
		"https://example.test/mcp#bearer-secret",
	} {
		if err := validateSettingsURL(raw); err == nil {
			t.Fatalf("validateSettingsURL(%q) succeeded", raw)
		}
	}
	if err := validateSettingsURL("https://example.test/mcp"); err != nil {
		t.Fatalf("safe URL rejected: %v", err)
	}
}
