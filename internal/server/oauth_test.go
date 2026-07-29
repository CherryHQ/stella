package server

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/CherryHQ/stella/internal/manifestplugins"
	"github.com/CherryHQ/stella/internal/pluginhost"
)

func TestRequestOriginUsesOriginHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/users/me/oauth/feishu/start", nil)
	req.Header.Set("Origin", "http://localhost:25678")

	if got := requestOrigin(req); got != "http://localhost:25678" {
		t.Fatalf("requestOrigin = %q, want http://localhost:25678", got)
	}
}

func TestRequestOriginFallsBackToRequestHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://localhost:25678/api/auth/oauth/feishu/callback", nil)

	if got := requestOrigin(req); got != "http://localhost:25678" {
		t.Fatalf("requestOrigin = %q, want http://localhost:25678", got)
	}
}

func TestRequestOriginUsesForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/auth/oauth/feishu/callback", nil)
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
	host.RegisterManifestPlugins(&manifestplugins.Manifest{
		Plugins: []manifestplugins.ManifestPlugin{
			{
				ID:            "tool/feishu-exporter",
				Kind:          "tool",
				Name:          "feishu-exporter",
				DisplayName:   "Feishu Exporter",
				Enabled:       true,
				OAuthProvider: "feishu",
				SessionEnvs: []manifestplugins.ManifestSessionEnv{
					{EnvVar: "FEISHU_EXPORTER_TOKEN", Source: "oauth.access_token"},
					{EnvVar: "FEISHU_EXPORTER_APP_ID", Source: "oauth.client_id"},
				},
			},
			{
				ID:            "tool/gh",
				Kind:          "tool",
				Name:          "gh",
				DisplayName:   "GitHub CLI",
				Enabled:       true,
				OAuthProvider: "github",
				SessionEnvs: []manifestplugins.ManifestSessionEnv{
					{EnvVar: "GH_TOKEN", Source: "oauth.access_token"},
				},
			},
			{
				ID:            "tool/disabled",
				Kind:          "tool",
				Name:          "disabled",
				Enabled:       false,
				OAuthProvider: "feishu",
				SessionEnvs: []manifestplugins.ManifestSessionEnv{
					{EnvVar: "X", Source: "oauth.access_token"},
				},
			},
		},
	})

	got := oauthProviderRequiredBy(host)

	if want := []string{"Feishu Exporter"}; !reflect.DeepEqual(got["feishu"], want) {
		t.Errorf("feishu RequiredBy = %v, want %v", got["feishu"], want)
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
