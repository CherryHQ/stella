package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CherryHQ/stella/internal/credential"
)

// The centralized gate has a small unit matrix; the live PostgreSQL HTTP
// lifecycle coverage is in provisioned_users_integration_test.go.
func TestRequireProvisioningBearerCredentialMatrix(t *testing.T) {
	for _, tc := range []struct {
		name      string
		principal *credential.Principal
		wantCode  int
		wantAllow bool
	}{
		{name: "interactive session", wantCode: http.StatusForbidden},
		{name: "personal PAT", principal: &credential.Principal{Kind: credential.KindPAT}, wantCode: http.StatusForbidden},
		{name: "OAuth", principal: &credential.Principal{Kind: credential.KindOAuth}, wantCode: http.StatusForbidden},
		{name: "provisioning token without id", principal: &credential.Principal{Kind: credential.KindProvisioning}, wantCode: http.StatusForbidden},
		{name: "provisioning token", principal: &credential.Principal{Kind: credential.KindProvisioning, CredentialID: "issuer-token"}, wantAllow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/provisioned-users", nil)
			r = r.WithContext(withAuthInfo(r.Context(), &AuthInfo{UserID: "issuer", principal: tc.principal}))
			w := httptest.NewRecorder()
			got := requireProvisioningBearer(w, r)
			if (got != nil) != tc.wantAllow {
				t.Fatalf("allowed = %v, want %v (body %q)", got != nil, tc.wantAllow, w.Body.String())
			}
			if !tc.wantAllow && w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}
