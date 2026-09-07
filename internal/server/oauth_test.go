package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/plugin"
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
	shipped := []struct {
		name        string
		displayName string
		enabled     bool
		payload     plugin.ResourcePayload
	}{
		{name: "acme-exporter", displayName: "Acme Exporter", enabled: true, payload: plugin.ResourcePayload{
			OAuth: []plugin.OAuthRequirement{{Provider: "acme"}}, SessionEnvs: []plugin.SessionEnvResource{
				{EnvVar: "ACME_EXPORTER_TOKEN", Source: "oauth.access_token"},
				{EnvVar: "ACME_EXPORTER_APP_ID", Source: "oauth.client_id"},
			},
		}},
		{name: "gh", displayName: "GitHub CLI", enabled: true, payload: plugin.ResourcePayload{
			OAuth: []plugin.OAuthRequirement{{Provider: "github"}}, SessionEnvs: []plugin.SessionEnvResource{{EnvVar: "GH_TOKEN", Source: "oauth.access_token"}},
		}},
		{name: "disabled", displayName: "disabled", enabled: false, payload: plugin.ResourcePayload{
			OAuth: []plugin.OAuthRequirement{{Provider: "acme"}}, SessionEnvs: []plugin.SessionEnvResource{{EnvVar: "X", Source: "oauth.access_token"}},
		}},
	}
	db := dbtest.New(t)
	catalog := plugin.NewCatalog()
	for _, declared := range shipped {
		for _, env := range declared.payload.SessionEnvs {
			declared.payload.OAuth[0].Bindings = append(declared.payload.OAuth[0].Bindings, plugin.OAuthBinding{Credential: strings.TrimPrefix(env.Source, "oauth."), EnvVar: env.EnvVar})
		}
		spec, err := json.Marshal(declared.payload)
		if err != nil {
			t.Fatal(err)
		}
		spec, err = plugin.PublishDefinitionSpec(spec)
		if err != nil {
			t.Fatal(err)
		}
		def := plugin.Definition{ID: declared.name, DisplayName: declared.displayName, Source: plugin.SourceBuiltin, Revision: 1, DefaultEnabled: declared.enabled, Spec: spec}
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
	definitions, err := plugin.BuiltinDefinitions()
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
