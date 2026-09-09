package server

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
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
// provider to the display names of enabled file-backed packages that need it.
func TestOAuthProviderRequiredBy(t *testing.T) {
	resources := []plugin.FileResource{
		fileOAuthResource("acme-exporter", "Acme Exporter", false, false, "acme", "acme", "github"),
		fileOAuthResource("fallback", "", false, false, "acme"),
		fileOAuthResource("disabled", "Disabled", true, false, "acme"),
		fileOAuthResource("forbidden", "Forbidden", false, true, "github"),
		{Package: nil},
	}
	got := oauthProviderRequiredBy(resources)

	if want := []string{"Acme Exporter", "fallback"}; !reflect.DeepEqual(got["acme"], want) {
		t.Errorf("acme RequiredBy = %v, want %v", got["acme"], want)
	}
	if want := []string{"Acme Exporter"}; !reflect.DeepEqual(got["github"], want) {
		t.Errorf("github RequiredBy = %v, want %v", got["github"], want)
	}
}

func TestOAuthProviderRequiredByEmptyResources(t *testing.T) {
	if got := oauthProviderRequiredBy(nil); len(got) != 0 {
		t.Errorf("RequiredBy(empty resources) = %v, want empty", got)
	}
}

func fileOAuthResource(name, displayName string, disabled, forbidden bool, providers ...string) plugin.FileResource {
	requirements := make([]agentpackage.OAuthRequirement, 0, len(providers))
	for _, provider := range providers {
		requirements = append(requirements, agentpackage.OAuthRequirement{Provider: provider})
	}
	return plugin.FileResource{
		Disabled:  disabled,
		Forbidden: forbidden,
		Package: &agentpackage.Package{
			Manifest: agentpackage.Manifest{Name: name},
			Extension: &agentpackage.StellaExtension{
				DisplayName: displayName,
				OAuth:       requirements,
			},
		},
	}
}
