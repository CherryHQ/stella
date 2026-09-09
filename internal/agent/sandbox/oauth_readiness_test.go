package sandbox

import (
	"testing"
	"time"

	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestPrepareOAuthPackagesChecksEachPackageIndependently(t *testing.T) {
	store := newStubOAuthVaultStore()
	registry := oauth.NewProviderRegistry()
	registry.Register(oauth.ProviderConfig{ID: "demo", VaultKey: "DEMO_OAUTH"})
	tokens := oauth.NewTokenManager(store)
	tokens.SetRegistry(registry)
	if err := oauth.SaveOAuthBundle(t.Context(), store, "user", "DEMO_OAUTH", oauth.OAuthBundle{
		AccessToken: "token", AccessExpiresAt: time.Now().Add(2 * time.Hour), GrantedScope: "read",
	}); err != nil {
		t.Fatal(err)
	}
	result := PrepareOAuthPackages(t.Context(), Config{UserID: "user", TokenManager: tokens}, []pkgplugins.PluginPackageRequirement{
		{PluginID: "allowed", OAuth: []pkgplugins.PluginOAuthRequirement{{Provider: "demo", Scopes: []string{"read"}, Bindings: []pkgplugins.PluginOAuthBinding{{Credential: "access_token", EnvVar: "DEMO_TOKEN"}}}}},
		{PluginID: "blocked", OAuth: []pkgplugins.PluginOAuthRequirement{{Provider: "demo", Scopes: []string{"write"}, Bindings: []pkgplugins.PluginOAuthBinding{{Credential: "access_token", EnvVar: "DEMO_TOKEN"}}}}},
	})
	if got := result.Status("allowed"); !got.Ready {
		t.Fatalf("allowed package = %+v, want ready", got)
	}
	if got := result.Status("blocked"); got.Ready || got.Reason == "" {
		t.Fatalf("blocked package = %+v, want bounded failure", got)
	}
}

func TestPrepareOAuthPackagesFailsClosedForMissingBinding(t *testing.T) {
	result := PrepareOAuthPackages(t.Context(), Config{UserID: "user"}, []pkgplugins.PluginPackageRequirement{{
		PluginID: "needs-oauth",
		OAuth:    []pkgplugins.PluginOAuthRequirement{{Provider: "demo", Bindings: []pkgplugins.PluginOAuthBinding{{Credential: "access_token"}}}},
	}})
	status := result.Status("needs-oauth")
	if status.Ready || status.Reason == "" {
		t.Fatalf("status = %+v, want unavailable", status)
	}
}

func TestPreparationResultMergeKeepsPriorFailure(t *testing.T) {
	base := pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{{PluginID: "demo", Reason: "OAuth unavailable"}}}
	merged := base.Merge(pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{{PluginID: "demo", Ready: true}}})
	if status := merged.Status("demo"); status.Ready || status.Reason != "OAuth unavailable" {
		t.Fatalf("merged status = %+v, want prior failure", status)
	}
}
