package server

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
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
	host := pluginhost.New(nil)
	host.RegisterManifestPlugins(&manifest.Manifest{
		Plugins: []manifest.ManifestPlugin{
			{
				ID:      "tool/acme-exporter",
				Kind:    "tool",
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
				Kind:    "tool",
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
				Kind:    "tool",
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
	})

	got := oauthProviderRequiredBy(host)

	if want := []string{"Acme Exporter"}; !reflect.DeepEqual(got["acme"], want) {
		t.Errorf("acme RequiredBy = %v, want %v", got["acme"], want)
	}
	if want := []string{"GitHub CLI"}; !reflect.DeepEqual(got["github"], want) {
		t.Errorf("github RequiredBy = %v, want %v", got["github"], want)
	}
}

func TestOAuthProviderRequiredByNilHost(t *testing.T) {
	if got := oauthProviderRequiredBy(nil); got != nil {
		t.Errorf("RequiredBy(nil host) = %v, want nil", got)
	}
}
