package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
)

func TestRequestOriginUsesOriginHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/users/me/oauth/acme/start", nil)
	req.Header.Set("Origin", "http://localhost:25678")

	if got := requestOrigin(req); got != "http://localhost:25678" {
		t.Fatalf("requestOrigin = %q, want http://localhost:25678", got)
	}
}

func TestRequestOriginFallsBackToRequestHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://localhost:25678/api/auth/oauth/acme/callback", nil)

	if got := requestOrigin(req); got != "http://localhost:25678" {
		t.Fatalf("requestOrigin = %q, want http://localhost:25678", got)
	}
}

func TestRequestOriginUsesForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/auth/oauth/acme/callback", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "stella.example.com")

	if got := requestOrigin(req); got != "https://stella.example.com" {
		t.Fatalf("requestOrigin = %q, want https://stella.example.com", got)
	}
}

// TestOAuthProviderRequiredBy verifies that the credentials-page hint maps each
// tool OAuth provider to the display names of the enabled tools that need it:
// multiple session envs of one tool collapse to a single entry, and disabled
// tools are excluded.
func TestOAuthProviderRequiredBy(t *testing.T) {
	shipped := &manifest.Manifest{
		Plugins: []manifest.ManifestPlugin{
			{
				ID:      "tool/acme-exporter",
				Enabled: true,
				ManifestPluginDefinition: manifest.ManifestPluginDefinition{
					Name:          "acme-exporter",
					DisplayName:   "Acme Exporter",
					OAuthProvider: "acme",
					SessionEnvs: []manifest.ManifestSessionEnv{
						{EnvVar: "ACME_EXPORTER_TOKEN", Source: "oauth.access_token"},
						{EnvVar: "ACME_EXPORTER_APP_ID", Source: "oauth.client_id"},
					},
				},
			},
			{
				ID:      "tool/gh",
				Enabled: true,
				ManifestPluginDefinition: manifest.ManifestPluginDefinition{
					Name:          "gh",
					DisplayName:   "GitHub CLI",
					OAuthProvider: "github",
					SessionEnvs: []manifest.ManifestSessionEnv{
						{EnvVar: "GH_TOKEN", Source: "oauth.access_token"},
					},
				},
			},
			{
				ID:      "tool/disabled",
				Enabled: false,
				ManifestPluginDefinition: manifest.ManifestPluginDefinition{
					Name:          "disabled",
					OAuthProvider: "acme",
					SessionEnvs: []manifest.ManifestSessionEnv{
						{EnvVar: "X", Source: "oauth.access_token"},
					},
				},
			},
		},
	}
	db := dbtest.New(t)
	catalog := plugin.NewCatalog()
	for _, declared := range shipped.Plugins {
		spec, err := json.Marshal(manifest.CLIPayload{OAuthProvider: declared.OAuthProvider, SessionEnvs: declared.SessionEnvs})
		if err != nil {
			t.Fatal(err)
		}
		def := plugin.Definition{ID: declared.Name, DisplayName: declared.DisplayName, Source: plugin.SourceBuiltin, Revision: 1, DefaultEnabled: declared.Enabled, Spec: spec}
		if def.DisplayName == "" {
			def.DisplayName = declared.Name
		}
		if err := catalog.Register(def); err != nil {
			t.Fatal(err)
		}
	}
	svc := plugin.NewService(db, nil, catalog, plugin.BackendPolicy{}, func(_ context.Context, mutate func() error) error { return mutate() })
	if err := svc.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewSystemAuthority("oauth-test")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := oauthProviderRequiredBy(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"Acme Exporter"}; !reflect.DeepEqual(got["acme"], want) {
		t.Errorf("acme RequiredBy = %v, want %v", got["acme"], want)
	}
	if want := []string{"GitHub CLI"}; !reflect.DeepEqual(got["github"], want) {
		t.Errorf("github RequiredBy = %v, want %v", got["github"], want)
	}
}

func TestOAuthProviderRequiredByUsesShippedCatalogWithoutHostRegistration(t *testing.T) {
	db := dbtest.New(t)
	definitions, err := manifest.BuiltinDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	catalog := plugin.NewCatalog()
	for _, def := range definitions {
		if err := catalog.Register(def); err != nil {
			t.Fatal(err)
		}
	}
	svc := plugin.NewService(db, nil, catalog, plugin.BackendPolicy{}, func(_ context.Context, mutate func() error) error { return mutate() })
	if err := svc.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewSystemAuthority("oauth-test")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := oauthProviderRequiredBy(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["github"]) == 0 || len(got["feishu"]) == 0 {
		t.Fatalf("shipped CLI OAuth dependencies missing: %v", got)
	}
}

func TestOAuthProviderRequiredByEmptySnapshot(t *testing.T) {
	got, err := oauthProviderRequiredBy(plugin.Snapshot{})
	if err != nil || len(got) != 0 {
		t.Errorf("RequiredBy(empty snapshot) = %v, %v, want empty", got, err)
	}
}
